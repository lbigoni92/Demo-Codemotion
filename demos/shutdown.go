package demos

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	"democodemotion/live"
)

// Shutdown fatto bene vs fatto in fretta.
//
// Kubernetes manda SIGTERM al pod e dopo terminationGracePeriodSeconds manda
// SIGKILL. In quella finestra il servizio dovrebbe:
//  1. smettere di accettare nuovo lavoro
//  2. lasciar finire il lavoro in corso, entro una deadline
//  3. cancellare (via context) ciò che non fa in tempo, in modo pulito
//  4. raccogliere gli errori (errors.Join) e uscire
//
// La versione "brutale" invece fa `<-sigCh; os.Exit(0)`: le richieste in volo
// vengono troncate a metà.

// request è una richiesta in volo: il worker la elabora in 20 passi da dur/20.
type request struct {
	id  int
	dur time.Duration
}

// Shutdown è la demo 05: un server con N worker e traffico continuo aspetta il
// SIGTERM (il pulsante, o da solo dopo 60 s), poi si spegne in modo brutale o
// graceful. Restituisce i contatori: completate, annullate, troncate, rifiutate.
func Shutdown(r *live.Run) any {
	mode := r.Str("mode", "graceful")
	deadline := time.Duration(r.Int("deadline", 3000, 500, 15000)) * time.Millisecond
	workersN := r.Int("workers", 4, 1, 8)

	r.Say("info", "Server avviato con %d worker. Arrivano richieste continuamente: premi «Invia SIGTERM» quando vuoi", workersN)
	r.State("phase", map[string]any{"name": "running"})

	// ctxWork: il contesto del lavoro. Si cancella solo alla scadenza della deadline
	// (graceful) o subito (brutale).
	ctxWork, cancelWork := context.WithCancel(r.Ctx)
	defer cancelWork()

	// queue senza buffer: una richiesta entra solo se un worker è libero di prenderla.
	// accepting è il «listener aperto»: dopo il SIGTERM diventa false e arrivano i 503.
	queue := make(chan request)
	var accepting atomic.Bool
	accepting.Store(true)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		errs    []error
		stats   = map[string]int{}
		inFlght atomic.Int32
	)
	count := func(k string) { mu.Lock(); stats[k]++; mu.Unlock() }

	// worker consuma la coda finché viene chiusa; ogni errore è una richiesta
	// interrotta, e finisce in errs per l'errors.Join finale.
	worker := func(w int) {
		defer wg.Done()
		for req := range queue {
			inFlght.Add(1)
			err := handle(ctxWork, r, w, req)
			inFlght.Add(-1)
			if err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
				if mode == "brutal" {
					count("troncate")
				} else {
					count("annullate")
				}
			} else {
				count("completate")
			}
		}
	}
	wg.Add(workersN)
	for w := 1; w <= workersN; w++ {
		go worker(w)
	}

	// generatore di traffico: una richiesta ogni 250–750 ms, di durata 1–5,5 s.
	// Se tutti i worker sono occupati per 150 ms la richiesta viene scartata,
	// come farebbe un load balancer con un backend saturo.
	genDone := make(chan struct{})
	stopGen := make(chan struct{})
	go func() {
		defer close(genDone)
		id := 0
		for {
			select {
			case <-stopGen:
				return
			case <-r.Ctx.Done():
				return
			case <-time.After(time.Duration(250+rand.IntN(500)) * time.Millisecond):
			}
			id++
			req := request{id: id, dur: time.Duration(1000+rand.IntN(4500)) * time.Millisecond}
			if !accepting.Load() {
				r.Log("warn", "listener", "richiesta #%d rifiutata: 503 Service Unavailable (server in chiusura)", id)
				r.State("req", map[string]any{"id": id, "status": "rejected"})
				count("rifiutate")
				continue
			}
			select {
			case queue <- req:
			case <-time.After(150 * time.Millisecond):
				r.Log("debug", "listener", "richiesta #%d: tutti i worker occupati, scartata dal load balancer", id)
			case <-stopGen:
				return
			}
		}
	}()

	// attendo il SIGTERM (bottone) — max 60 s
	select {
	case <-r.Ctx.Done():
		close(stopGen)
		<-genDone
		return nil
	case <-time.After(60 * time.Second):
		r.Log("info", "demo", "nessun SIGTERM in 60 s: lo invio io")
	case <-r.Control:
	}

	t0 := time.Now()
	r.State("phase", map[string]any{"name": "sigterm"})
	r.Log("warn", "os/signal", "ricevuto SIGTERM (%d richieste in corso)", inFlght.Load())

	// ── brutale ──
	// os.Exit non si può mostrare senza uccidere il server della demo: lo si
	// simula cancellando subito il context del lavoro, senza drain né deadline.
	if mode == "brutal" {
		r.Say("error", "Shutdown BRUTALE: os.Exit(0) subito. Le %d richieste in corso vengono troncate a metà", inFlght.Load())
		accepting.Store(false)
		cancelWork() // equivale a uccidere il processo: tutto si ferma ora
		close(stopGen)
		<-genDone
		close(queue)
		wg.Wait()
		r.State("phase", map[string]any{"name": "dead"})
		r.Log("error", "processo", "exit(0) dopo %v — nessun rollback, nessun log finale, connessioni DB lasciate aperte", time.Since(t0).Round(time.Millisecond))
		mu.Lock()
		defer mu.Unlock()
		return stats
	}

	// ── graceful ──
	r.Say("warn", "SIGTERM: 1) smetto di accettare richieste  2) aspetto le %d in corso al massimo %v", inFlght.Load(), deadline)
	accepting.Store(false)
	r.State("phase", map[string]any{"name": "draining", "deadline": deadline.Milliseconds()})
	r.Log("info", "server", "listener chiuso: le nuove richieste ricevono 503 (readiness probe → NotReady)")

	// Lo stesso pattern di k9slogger: wg.Wait() in una goroutine che chiude done,
	// poi select tra done e la scadenza del context con timeout.
	go func() {
		<-time.After(1500 * time.Millisecond) // il traffico continua ad arrivare per un po'
		close(stopGen)
	}()
	<-genDone
	close(queue)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	// La deadline parte dal SIGTERM: si sottrae il tempo già passato ad accettare
	// gli ultimi 1,5 s di traffico. Il context è nuovo (Background) perché deve
	// sopravvivere alla cancellazione di ctxWork.
	shutdownCtx, stop := context.WithTimeout(context.Background(), deadline-time.Since(t0))
	defer stop()
	select {
	case <-done:
		r.Log("ok", "server", "tutte le richieste in corso sono terminate entro la deadline")
	case <-shutdownCtx.Done():
		r.Log("warn", "server", "deadline di %v scaduta con %d richieste ancora in corso → cancelWork(): ricevono ctx.Done()", deadline, inFlght.Load())
		r.State("phase", map[string]any{"name": "cancelling"})
		cancelWork()
		<-done
	}

	mu.Lock()
	joined := errors.Join(errs...)
	mu.Unlock()
	r.State("phase", map[string]any{"name": "stopped"})
	if joined != nil {
		r.Log("warn", "server", "errors.Join dei worker:\n%v", joined)
	}
	r.Log("ok", "processo", "uscita pulita in %v: connessioni chiuse, log scaricati", time.Since(t0).Round(time.Millisecond))
	mu.Lock()
	defer mu.Unlock()
	r.Say("ok", "Shutdown graceful in %v: %d completate, %d annullate con rollback, %d rifiutate con 503",
		time.Since(t0).Round(time.Millisecond), stats["completate"], stats["annullate"], stats["rifiutate"])
	return stats
}

// handle elabora una richiesta: avanza a passi e controlla ctx.Done() a ogni passo.
// È questo controllo che rende possibile la cancellazione: un worker che non
// guarda il context non si ferma finché il processo non viene ucciso.
// Restituisce un errore che avvolge ctx.Err() (%w), così errors.Is funziona.
func handle(ctx context.Context, r *live.Run, w int, req request) error {
	src := fmt.Sprintf("worker-%d", w)
	r.Log("debug", src, "richiesta #%d presa in carico (durata stimata %v)", req.id, req.dur.Round(100*time.Millisecond))
	steps := 20
	for s := 1; s <= steps; s++ {
		p := (s - 1) * 100 / steps
		r.State("req", map[string]any{"id": req.id, "w": w, "status": "running", "p": p})
		select {
		case <-ctx.Done():
			r.State("req", map[string]any{"id": req.id, "w": w, "status": "aborted", "p": p})
			r.Log("error", src, "richiesta #%d interrotta al %d%%: %v", req.id, p, ctx.Err())
			return fmt.Errorf("richiesta #%d interrotta al %d%%: %w", req.id, p, ctx.Err())
		case <-time.After(req.dur / time.Duration(steps)):
		}
	}
	r.State("req", map[string]any{"id": req.id, "w": w, "status": "done", "p": 100})
	r.Log("ok", src, "richiesta #%d completata, transazione committata", req.id)
	return nil
}

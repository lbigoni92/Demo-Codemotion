package demos

import (
	"fmt"
	"math/rand/v2"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"democodemotion/live"
)

// Pipeline a stadi con i channel, raccontata come una pizzeria.
//
//	cassa ──ch──▶ Preparazione ──ch──▶ Forno ──ch──▶ Consegna ──▶ pizze consegnate
//	              (fan-out: 1 cuoco/goroutine per ingrediente, WaitGroup + atomic.Bool)
//
// Ogni stadio è una goroutine che fa `for p := range in { ...; out <- p }` e,
// quando l'input finisce, chiude il proprio output con `defer close(out)`:
// la chiusura "scorre" lungo la pipeline e fa terminare tutti gli stadi.

// pizza è il valore che viaggia nei channel. Si passa per copia: ogni stadio
// lavora sulla propria copia, quindi nessuno stato condiviso da proteggere.
type pizza struct {
	ID     int
	Failed bool // impostato dalla Preparazione quando un cuoco segnala un errore
}

// ingredienti, nell'ordine in cui vengono assegnati ai cuochi: con N ingredienti
// per pizza si usano i primi N. Il secondo (mozzarella) è quello che può finire.
var ingredienti = []string{"pomodoro", "mozzarella", "basilico", "funghi", "prosciutto", "olive", "carciofi", "salame"}

// pipelineCfg raccoglie i parametri scelti nel browser, già validati da r.Int.
type pipelineCfg struct {
	pizzas      int  // comande generate dalla cassa
	ingredients int  // goroutine lanciate per ogni pizza (fan-out)
	buffer      int  // capacità di ogni channel: 0 = rendezvous
	cooks       int  // capacità del semaforo: 0 = nessun limite al fan-out
	slowOven    bool // forno a più di 1 s per pizza: il collo di bottiglia
	failID      int  // pizza a cui manca un ingrediente (0 = nessuna)
}

// Pipeline è la demo 01. Struttura delle goroutine:
//   - cassa: produttore, chiude il primo channel quando finisce le comande
//   - Preparazione: per ogni pizza lancia un cuoco per ingrediente e aspetta tutti
//   - Forno: una pizza alla volta
//   - Consegna: il ciclo finale, nella goroutine della demo stessa
//   - un consumatore di errorCh e un campionatore per l'animazione
func Pipeline(r *live.Run) any {
	cfg := pipelineCfg{
		pizzas:      r.Int("pizzas", 8, 1, 40),
		ingredients: r.Int("ingredients", 3, 1, len(ingredienti)),
		buffer:      r.Int("buffer", 1, 0, 20),
		cooks:       r.Int("cooks", 1, 0, 16),
		slowOven:    r.Bool("slow"),
		failID:      r.Int("fail", 0, 0, 40),
	}
	start := time.Now()
	// NumGoroutine conta tutto il processo (server HTTP, altre run): si misura
	// la differenza rispetto a prima della demo.
	baseG := runtime.NumGoroutine()

	r.Say("info", "Apre la pizzeria: %d comande, %d ingredienti per pizza, banconi (buffer dei channel) da %d", cfg.pizzas, cfg.ingredients, cfg.buffer)
	if cfg.buffer == 0 {
		r.Log("info", "main", "channel NON bufferizzati: nessun bancone, ogni pizza passa di mano solo quando lo stadio dopo è pronto (rendezvous)")
	} else {
		r.Log("info", "main", "channel bufferizzati da %d: sul bancone c'è posto per %d pizze prima che lo stadio a monte si fermi", cfg.buffer, cfg.buffer)
	}
	if cfg.cooks > 0 {
		r.Log("info", "main", "fan-out limitato da un semaforo (chan struct{}): solo %d cuochi in cucina", cfg.cooks)
	} else {
		r.Log("warn", "main", "fan-out SENZA limite: pizze × ingredienti goroutine possono partire insieme")
	}

	r.State("config", map[string]any{"pizzas": cfg.pizzas, "ingredients": cfg.ingredients, "buffer": cfg.buffer})

	// Il semaforo per limitare la concorrenza: un channel bufferizzato di struct{}
	// vuote (zero byte). Chi entra invia un gettone, chi esce lo riprende: con il
	// buffer pieno l'invio blocca. Un channel nil significa «nessun limite».
	var sem chan struct{}
	if cfg.cooks > 0 {
		sem = make(chan struct{}, cfg.cooks)
	}

	// errorCh raccoglie gli errori da tutti i cuochi; una goroutine dedicata li
	// consuma, così chi segnala un errore non resta mai bloccato ad aspettare.
	// Si chiude solo dopo stages.Wait(), quando nessuno può più scriverci.
	errorCh := make(chan error, 16)
	var errCount atomic.Int32
	errDone := make(chan struct{})
	go func() {
		defer close(errDone)
		for err := range errorCh {
			errCount.Add(1)
			r.Log("error", "errorCh", "%v", err)
		}
	}()

	// Campionatore: ogni 120 ms legge goroutine vive, len/cap dei channel e
	// grembiuli in uso. len e cap su un channel sono letture istantanee, sicure
	// in concorrenza: servono solo a disegnare, non a decidere.
	var maxG atomic.Int64
	chans := map[string]chan pizza{
		"cassa→prep": make(chan pizza, cfg.buffer),
		"prep→forno": make(chan pizza, cfg.buffer),
		"forno→cons": make(chan pizza, cfg.buffer),
	}
	stopSampler := make(chan struct{})
	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		t := time.NewTicker(120 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stopSampler:
				return
			case <-r.Ctx.Done():
				return
			case <-t.C:
				g := int64(runtime.NumGoroutine() - baseG)
				if g > maxG.Load() {
					maxG.Store(g)
				}
				occ := map[string]any{}
				for k, ch := range chans {
					occ[k] = []int{len(ch), cap(ch)}
				}
				smp := map[string]any{"goroutines": g, "chans": occ}
				if sem != nil {
					smp["sem"] = []int{len(sem), cap(sem)} // grembiuli in uso / totali
				}
				r.State("sample", smp)
			}
		}
	}()

	// send prova un invio non bloccante: se il channel è pieno lo racconta
	// (backpressure) e poi fa l'invio bloccante vero. Restituisce false se la
	// run viene cancellata mentre aspetta: lo stadio deve allora uscire.
	send := func(stage, chName string, ch chan pizza, p pizza) bool {
		select {
		case ch <- p:
			return true
		default:
		}
		r.Log("warn", stage, "pizza #%d pronta ma il bancone %s è pieno (%d/%d): %s si blocca → backpressure", p.ID, chName, len(ch), cap(ch), stage)
		r.State("blocked", map[string]any{"stage": stage, "id": p.ID, "on": true})
		select {
		case ch <- p:
			r.State("blocked", map[string]any{"stage": stage, "id": p.ID, "on": false})
			return true
		case <-r.Ctx.Done():
			return false
		}
	}

	// ── stadio 0: cassa (sorgente delle comande) ──────────────────────
	go func() {
		out := chans["cassa→prep"]
		defer closeAndTell(r, "cassa→prep", out)
		for i := 1; i <= cfg.pizzas; i++ {
			p := pizza{ID: i}
			r.State("move", map[string]any{"id": i, "to": "src"})
			r.Log("debug", "cassa", "nuova comanda: pizza #%d", i)
			if !send("cassa", "cassa→prep", out, p) {
				return
			}
			r.State("move", map[string]any{"id": i, "to": "cassa→prep"})
			if !r.Sleep(60 * time.Millisecond) {
				return
			}
		}
		r.Log("info", "cassa", "comande finite → close(out): la Preparazione uscirà dal suo range")
	}()

	// stages conta le due goroutine che chiamano stages.Done(): Preparazione e
	// Forno. Serve per sapere quando nessun cuoco può più scrivere su errorCh.
	// La cassa non ne fa parte: quando la Consegna esce dal range, la catena di
	// close() garantisce che la cassa abbia già chiuso il suo output.
	var stages sync.WaitGroup
	stages.Add(2)

	// ── stadio 1: Preparazione, fan-out per ingrediente ──────────────
	go func() {
		defer stages.Done()
		in, out := chans["cassa→prep"], chans["prep→forno"]
		const stage = "Preparazione"
		// defer in ordine LIFO: prima il log, poi la chiusura, infine stages.Done()
		defer closeAndTell(r, "prep→forno", out)
		defer r.Log("info", stage, "comande chiuse e svuotate → close(out)")
		for p := range in {
			r.State("move", map[string]any{"id": p.ID, "to": stage})
			var (
				wg       sync.WaitGroup
				hasError atomic.Bool // flag condiviso tra i cuochi
			)
			// Add prima di lanciare le goroutine: se lo facessero loro, Wait
			// potrebbe vedere il contatore a zero prima che partano.
			wg.Add(cfg.ingredients)
			r.Log("info", stage, "pizza #%d: %d cuochi in parallelo, uno per ingrediente (wg.Add(%d))", p.ID, cfg.ingredients, cfg.ingredients)
			for f := 1; f <= cfg.ingredients; f++ {
				go func(f int) {
					defer wg.Done()
					ing := ingredienti[f-1]
					if sem != nil {
						r.State("ingredient", map[string]any{"id": p.ID, "f": f, "name": ing, "status": "wait"})
						// la goroutine esiste già: il semaforo limita chi lavora, non chi nasce
						select {
						case sem <- struct{}{}: // prendo un "grembiule"
						case <-r.Ctx.Done():
							return
						}
						defer func() { <-sem }() // lo restituisco
					}
					r.State("ingredient", map[string]any{"id": p.ID, "f": f, "name": ing, "status": "run"})
					if !r.Sleep(time.Duration(250+rand.IntN(350)) * time.Millisecond) {
						return
					}
					if p.ID == cfg.failID && f == 2 {
						hasError.Store(true)
						r.State("ingredient", map[string]any{"id": p.ID, "f": f, "name": ing, "status": "err"})
						errorCh <- fmt.Errorf("[%s] pizza #%d: %s finita in dispensa", stage, p.ID, ing)
						return
					}
					r.State("ingredient", map[string]any{"id": p.ID, "f": f, "name": ing, "status": "ok"})
				}(f)
			}
			wg.Wait() // tutti i cuochi hanno chiamato Done: hasError ora è stabile
			if hasError.Load() {
				p.Failed = true
				r.Log("error", stage, "pizza #%d: manca un ingrediente (hasError=true) → non va in forno", p.ID)
				r.State("move", map[string]any{"id": p.ID, "to": "failed"})
				continue
			}
			r.Log("ok", stage, "pizza #%d: tutti i %d ingredienti pronti (wg.Wait() sbloccato)", p.ID, cfg.ingredients)
			if !send(stage, "prep→forno", out, p) {
				return
			}
			r.State("move", map[string]any{"id": p.ID, "to": "prep→forno"})
		}
	}()

	// ── stadio 2: Forno, una pizza alla volta ─────────────────────────
	if cfg.slowOven {
		r.Log("warn", "Forno", "forno lento: guarda i banconi a monte riempirsi")
	}
	go func() {
		defer stages.Done()
		in, out := chans["prep→forno"], chans["forno→cons"]
		const stage = "Forno"
		defer closeAndTell(r, "forno→cons", out)
		defer r.Log("info", stage, "input chiuso e svuotato → close(out)")
		for p := range in {
			d := time.Duration(200+rand.IntN(250)) * time.Millisecond
			if cfg.slowOven {
				d = time.Duration(1100+rand.IntN(400)) * time.Millisecond
			}
			r.State("move", map[string]any{"id": p.ID, "to": stage, "ms": d.Milliseconds()})
			r.Log("debug", stage, "pizza #%d in cottura (%v)", p.ID, d.Round(10*time.Millisecond))
			if !r.Sleep(d) {
				return
			}
			if !send(stage, "forno→cons", out, p) {
				return
			}
			r.State("move", map[string]any{"id": p.ID, "to": "forno→cons"})
		}
	}()

	// ── ultimo stadio: Consegna (sequenziale) ─────────────────────────
	// Gira nella goroutine della demo: il range termina quando il Forno chiude
	// il suo output, cioè quando tutta la pipeline a monte ha finito.
	delivered := 0
	for p := range chans["forno→cons"] {
		r.State("move", map[string]any{"id": p.ID, "to": "Consegna"})
		r.Log("debug", "Consegna", "il rider parte con la pizza #%d", p.ID)
		if !r.Sleep(1200 * time.Millisecond) {
			break
		}
		delivered++
		r.State("move", map[string]any{"id": p.ID, "to": "done"})
		r.Log("ok", "Consegna", "pizza #%d consegnata ✔", p.ID)
	}
	// Ordine di chiusura: prima si aspettano gli stadi, poi si chiude errorCh
	// (nessuno ci scrive più), poi si ferma il campionatore.
	stages.Wait()
	close(errorCh) // tutti i produttori di errori sono terminati: ora si può chiudere
	<-errDone
	close(stopSampler)
	<-samplerDone

	if r.Ctx.Err() != nil {
		return nil
	}
	el := time.Since(start).Round(10 * time.Millisecond)
	r.Say("ok", "Servizio finito in %v: %d pizze consegnate, %d errori, picco di %d goroutine", el, delivered, errCount.Load(), maxG.Load())
	return map[string]any{"elapsed": el.String(), "delivered": delivered, "errors": errCount.Load(), "maxGoroutines": maxG.Load()}
}

// closeAndTell chiude l'output di uno stadio e lo comunica all'animazione:
// il bancone a valle viene marcato "chiuso" e il range dello stadio dopo terminerà.
// Va chiamata una sola volta e solo da chi scrive sul channel: chiudere due
// volte, o scrivere dopo la chiusura, è un panic.
func closeAndTell(r *live.Run, name string, out chan pizza) {
	close(out)
	r.State("closed", map[string]any{"ch": name})
}

package demos

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"

	"democodemotion/live"
)

// Lock distribuiti e scaling orizzontale.
//
// Più worker (su macchine diverse) si contendono i job prendendo un lock su
// Redis: SET job_lock:<id> <workerID> NX PX <ttl>. Il TTL serve a liberare il
// lock se il worker muore. Ma se il job dura PIÙ del TTL e nessuno rinnova il
// lock, questo scade mentre il job è ancora in corso e un altro worker
// prende lo stesso job → doppia esecuzione.
//
// Qui Redis è simulato in memoria (fakeRedis) con la stessa semantica.

// fakeEntry è un valore con la sua scadenza; exp zero = nessuna scadenza.
type fakeEntry struct {
	val string
	exp time.Time
}

// fakeRedis è un Redis in memoria con le regole che contano per la demo.
// Il mutex fa la parte del thread unico di Redis: un comando alla volta,
// e finché un comando lo tiene nessun altro client viene servito.
type fakeRedis struct {
	mu   sync.Mutex
	data map[string]fakeEntry
}

func newFakeRedis() *fakeRedis { return &fakeRedis{data: map[string]fakeEntry{}} }

// getLocked legge una chiave e la cancella se è scaduta, come la scadenza
// «pigra» di Redis. Va chiamata con f.mu già preso.
func (f *fakeRedis) getLocked(k string) (fakeEntry, bool) {
	e, ok := f.data[k]
	if ok && !e.exp.IsZero() && time.Now().After(e.exp) {
		delete(f.data, k)
		return fakeEntry{}, false
	}
	return e, ok
}

// SetNX = SET key val NX PX ttl: crea la chiave solo se non esiste, con
// scadenza. Verifica e scrittura avvengono sotto lo stesso lock: è atomico.
func (f *fakeRedis) SetNX(k, v string, ttl time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.getLocked(k); ok {
		return false
	}
	f.data[k] = fakeEntry{val: v, exp: time.Now().Add(ttl)}
	return true
}

// Extend rinnova il TTL solo se il lock è ancora mio. In Redis è uno script Lua
// (GET + PEXPIRE) eseguito in modo atomico, come fa mutex.Extend() di redsync.
func (f *fakeRedis) Extend(k, v string, ttl time.Duration) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.getLocked(k)
	if !ok || e.val != v {
		return false
	}
	e.exp = time.Now().Add(ttl)
	f.data[k] = e
	return true
}

// Unlock cancella il lock solo se è ancora mio (compare-and-delete, uno script
// Lua in Redis). Un semplice DEL cancellerebbe anche il lock preso da un altro
// worker dopo la scadenza del mio.
func (f *fakeRedis) Unlock(k, v string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.getLocked(k)
	if !ok || e.val != v {
		return false
	}
	delete(f.data, k)
	return true
}

// Set = SET key val, senza scadenza: serve a riempire Redis per KEYS/SCAN.
func (f *fakeRedis) Set(k, v string) {
	f.mu.Lock()
	f.data[k] = fakeEntry{val: v}
	f.mu.Unlock()
}

// Get = GET key: il comando che il client «misuratore» ripete ogni 20 ms.
func (f *fakeRedis) Get(k string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.getLocked(k)
	return e.val, ok
}

// perKeyCost simula il costo di Redis per esaminare una chiave.
const perKeyCost = 8 * time.Microsecond

// spin occupa la CPU per d (busy wait) invece di dormire: Redis lavora
// davvero su ogni chiave, e un time.Sleep di pochi microsecondi durerebbe
// molto di più per la granularità del timer.
func spin(d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
	}
}

// Keys = KEYS pattern: Redis è single-thread, quindi tiene occupato il server
// per tutta la scansione. Nessun altro comando viene servito nel frattempo:
// qui il mutex resta preso dall'inizio alla fine. Il pattern è semplificato a
// un prefisso.
func (f *fakeRedis) Keys(prefix string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for k := range f.data {
		spin(perKeyCost)
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out
}

// Scan = SCAN cursor MATCH pattern COUNT n: lavora a blocchi e tra un blocco
// e l'altro il server è libero di servire gli altri client. Qui il mutex viene
// rilasciato dopo ogni blocco; onBatch riceve quante chiavi sono state esaminate.
func (f *fakeRedis) Scan(prefix string, count int, onBatch func(n int)) []string {
	f.mu.Lock()
	keys := make([]string, 0, len(f.data))
	for k := range f.data {
		keys = append(keys, k)
	}
	f.mu.Unlock()
	var out []string
	for i := 0; i < len(keys); i += count {
		end := min(i+count, len(keys))
		f.mu.Lock()
		for _, k := range keys[i:end] {
			spin(perKeyCost)
			if strings.HasPrefix(k, prefix) {
				out = append(out, k)
			}
		}
		f.mu.Unlock()
		onBatch(end)
		time.Sleep(time.Millisecond) // il server serve altri client
	}
	return out
}

// locksSnapshot elenca i lock ancora validi, con proprietario e millisecondi
// rimasti, ordinati per chiave: è quello che il browser disegna nella colonna Redis.
func (f *fakeRedis) locksSnapshot(prefix string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	var out []map[string]any
	for k, e := range f.data {
		if strings.HasPrefix(k, prefix) && now.Before(e.exp) {
			out = append(out, map[string]any{"key": k, "owner": e.val, "ms": e.exp.Sub(now).Milliseconds()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["key"].(string) < out[j]["key"].(string) })
	return out
}

// ─────────────────────── demo A: TTL e doppia esecuzione ───────────────────────

// Locks è la demo 04, tab «TTL e doppia esecuzione»: N worker (goroutine che
// fanno la parte di macchine diverse) cercano job liberi con SET NX PX, li
// eseguono e rilasciano il lock. Restituisce quante esecuzioni doppie ci sono state.
func Locks(r *live.Run) any {
	workers := r.Int("workers", 3, 2, 6)
	jobs := r.Int("jobs", 5, 1, 10)
	ttl := time.Duration(r.Int("ttl", 1500, 300, 10000)) * time.Millisecond
	dur := time.Duration(r.Int("dur", 2600, 300, 15000)) * time.Millisecond
	extend := r.Bool("extend")

	rd := newFakeRedis()
	r.Say("info", "%d worker si contendono %d job. TTL del lock %v, durata di un job ~%v, rinnovo %s",
		workers, jobs, ttl, dur, map[bool]string{true: "ATTIVO", false: "ASSENTE"}[extend])
	if dur > ttl && !extend {
		r.Log("warn", "demo", "il job dura più del TTL e nessuno rinnova il lock: aspettati guai")
	}

	// Stato condiviso tra i worker, protetto da mu. completed fa la parte dello
	// stato dei job in Postgres: un job completato non viene più preso.
	var mu sync.Mutex
	completed := map[int]bool{}
	runs := map[int]int{}   // quante esecuzioni sono partite per ogni job (anche interrotte)
	active := map[int]int{} // quante esecuzioni sono in corso adesso: 2 o più = doppia
	doubles := 0

	stop := make(chan struct{})
	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-r.Ctx.Done():
				return
			case <-t.C:
				r.State("locks", map[string]any{"locks": rd.locksSnapshot("job_lock:"), "ttl": ttl.Milliseconds()})
			}
		}
	}()

	var wg sync.WaitGroup
	for w := 1; w <= workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			id := fmt.Sprintf("worker-%d", w)
			r.Sleep(time.Duration(rand.IntN(200)) * time.Millisecond)
			for r.Ctx.Err() == nil {
				// cerco un job non ancora completato (stato in Postgres)
				picked := 0
				for j := 1; j <= jobs; j++ {
					mu.Lock()
					done := completed[j]
					mu.Unlock()
					if done {
						continue
					}
					key := fmt.Sprintf("job_lock:%d", j)
					if rd.SetNX(key, id, ttl) {
						picked = j
						break
					}
				}
				if picked == 0 {
					mu.Lock()
					all := len(completed) == jobs
					mu.Unlock()
					if all {
						r.State("worker", map[string]any{"w": w, "status": "idle"})
						return
					}
					r.State("worker", map[string]any{"w": w, "status": "wait"})
					r.Sleep(150 * time.Millisecond)
					continue
				}
				runJob(r, rd, w, id, picked, ttl, dur, extend, &mu, completed, runs, active, &doubles)
			}
		}(w)
	}
	wg.Wait()
	close(stop)
	<-samplerDone
	r.State("locks", map[string]any{"locks": []any{}, "ttl": ttl.Milliseconds()})

	if r.Ctx.Err() != nil {
		return nil
	}
	if doubles > 0 {
		r.Say("error", "%d esecuzioni doppie! In produzione: pagamenti addebitati due volte, email duplicate, righe doppie", doubles)
	} else {
		r.Say("ok", "Tutti i %d job eseguiti esattamente una volta", jobs)
	}
	return map[string]any{"doubleExecutions": doubles, "runs": runs}
}

// runJob esegue il job j per il worker w, che ha appena ottenuto il lock.
// Se un'altra esecuzione dello stesso job è ancora in corso è una doppia
// esecuzione: il lock precedente era scaduto mentre il primo worker lavorava.
// Un job interrotto perché il lock è andato perso non conta: chi lo riprende
// dopo fa un nuovo tentativo, non una doppia esecuzione.
func runJob(r *live.Run, rd *fakeRedis, w int, id string, j int, ttl, dur time.Duration, extend bool,
	mu *sync.Mutex, completed map[int]bool, runs, active map[int]int, doubles *int) {
	key := fmt.Sprintf("job_lock:%d", j)
	mu.Lock()
	runs[j]++
	n := runs[j] // numero dell'esecuzione: identifica la sua barra nel browser
	active[j]++
	double := active[j] > 1
	if double {
		*doubles++
	}
	mu.Unlock()
	defer func() { mu.Lock(); active[j]--; mu.Unlock() }()
	if double {
		r.Log("error", id, "SET %s NX → OK: il lock era SCADUTO, prendo il job #%d che è GIÀ in esecuzione altrove → DOPPIA ESECUZIONE", key, j)
		r.State("double", map[string]any{"job": j, "w": w})
	} else {
		r.Log("info", id, "SET %s %s NX PX %d → OK, eseguo il job #%d", key, id, ttl.Milliseconds(), j)
	}

	// jobCtx è il context del lavoro: si cancella se la run viene fermata o se il
	// watchdog scopre che il lock non è più nostro.
	jobCtx, cancelJob := context.WithCancel(r.Ctx)
	defer cancelJob()

	// watchdog: rinnova il lock ogni ttl/3 finché il job è in corso (a un terzo
	// del TTL restano due tentativi prima della scadenza). Se il rinnovo fallisce
	// il lock è passato a qualcun altro: cancella jobCtx e il job si ferma.
	jobDone := make(chan struct{})
	var wdDone chan struct{}
	if extend {
		wdDone = make(chan struct{})
		go func() {
			defer close(wdDone)
			t := time.NewTicker(ttl / 3)
			defer t.Stop()
			for {
				select {
				case <-jobDone:
					return
				case <-r.Ctx.Done():
					return
				case <-t.C:
					if rd.Extend(key, id, ttl) {
						r.Log("debug", id, "watchdog: mutex.Extend() su %s → TTL di nuovo a %v", key, ttl)
					} else {
						r.Log("error", id, "watchdog: Extend fallito, il lock non è più mio: interrompo il job #%d", j)
						cancelJob()
						return
					}
				}
			}
		}()
	}

	d := dur + time.Duration(rand.IntN(400))*time.Millisecond
	steps := 20
	for s := 1; s <= steps; s++ {
		r.State("job", map[string]any{"job": j, "w": w, "p": s * 100 / steps, "run": n})
		r.State("worker", map[string]any{"w": w, "status": "busy", "job": j})
		// ogni passo controlla jobCtx: è così che il job si accorge del lock perso
		select {
		case <-jobCtx.Done():
			close(jobDone)
			if wdDone != nil {
				<-wdDone
			}
			if r.Ctx.Err() == nil { // non è la run fermata: il lock è andato perso
				r.State("job", map[string]any{"job": j, "w": w, "p": s * 100 / steps, "run": n, "lost": true})
				r.Log("warn", id, "job #%d fermato al %d%% senza commit: lo riprenderà chi ha il lock", j, s*100/steps)
			}
			return
		case <-time.After(d / time.Duration(steps)):
		}
	}
	// ferma il watchdog e aspetta che sia uscito prima di rilasciare il lock:
	// un Extend dopo l'Unlock ricreerebbe la scadenza su una chiave non più nostra
	close(jobDone)
	if wdDone != nil {
		<-wdDone
	}

	mu.Lock()
	completed[j] = true
	mu.Unlock()
	r.State("job", map[string]any{"job": j, "w": w, "p": 100, "run": n, "done": true})
	if rd.Unlock(key, id) {
		r.Log("ok", id, "job #%d completato, DEL %s (compare-and-delete: il lock era ancora mio)", j, key)
	} else {
		r.Log("warn", id, "job #%d completato, ma ReleaseLock fallisce: \"lock was not held or expired\" — il lock era scaduto o di un altro", j)
	}
}

// ─────────────────────── demo B: KEYS vs SCAN ───────────────────────

// KeysScan è la demo 04, tab «KEYS vs SCAN»: riempie Redis con n chiavi (una su
// 50 è un heartbeat di worker) e cerca i worker con KEYS o con SCAN, mentre un
// altro client misura la latenza reale di un GET ogni 20 ms.
func KeysScan(r *live.Run) any {
	n := r.Int("keys", 50000, 1000, 300000)
	mode := r.Str("mode", "keys")
	rd := newFakeRedis()
	for i := 0; i < n; i++ {
		if i%50 == 0 {
			rd.Set(fmt.Sprintf("worker_heartbeat:w%d", i), "1")
		} else {
			rd.Set(fmt.Sprintf("session:%d", i), "x")
		}
	}
	rd.Set("config:feature", "on")
	r.Say("info", "Redis con %d chiavi. Un client misura la latenza di un GET ogni 20 ms mentre eseguo %s",
		n, map[string]string{"keys": "KEYS worker_heartbeat:*", "scan": "SCAN 0 MATCH worker_heartbeat:* COUNT 500"}[mode])

	// maxLat è scritto solo dalla goroutine misuratrice e letto dopo <-probeDone:
	// la chiusura del channel garantisce che la lettura veda l'ultimo valore.
	stop := make(chan struct{})
	probeDone := make(chan struct{})
	var maxLat time.Duration
	go func() {
		defer close(probeDone)
		for {
			select {
			case <-stop:
				return
			case <-r.Ctx.Done():
				return
			default:
			}
			t0 := time.Now()
			rd.Get("config:feature")
			lat := time.Since(t0)
			if lat > maxLat {
				maxLat = lat
			}
			r.State("probe", map[string]any{"ms": float64(lat.Microseconds()) / 1000})
			if lat > 100*time.Millisecond {
				r.Log("error", "client API", "GET config:feature ha impiegato %v: Redis era bloccato", lat.Round(time.Millisecond))
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	r.Sleep(500 * time.Millisecond)
	t0 := time.Now()
	var found []string
	r.State("scan", map[string]any{"status": "start", "total": n})
	if mode == "keys" {
		r.Log("warn", "worker", "KEYS worker_heartbeat:* → O(N) sull'intero keyspace, server bloccato fino alla fine")
		found = rd.Keys("worker_heartbeat:")
		r.State("scan", map[string]any{"status": "progress", "done": n, "total": n})
	} else {
		r.Log("info", "worker", "SCAN a blocchi da 500: tra un blocco e l'altro Redis serve gli altri client")
		found = rd.Scan("worker_heartbeat:", 500, func(done int) {
			r.State("scan", map[string]any{"status": "progress", "done": done, "total": n})
		})
	}
	el := time.Since(t0)
	r.State("scan", map[string]any{"status": "end"})
	r.Log("ok", "worker", "trovati %d worker attivi in %v", len(found), el.Round(time.Millisecond))
	r.Sleep(600 * time.Millisecond)
	close(stop)
	<-probeDone
	if r.Ctx.Err() != nil {
		return nil
	}
	if mode == "keys" {
		r.Say("error", "KEYS: latenza massima degli altri client %v. Con Redis condiviso, tutti i servizi si fermano.", maxLat.Round(time.Millisecond))
	} else {
		r.Say("ok", "SCAN: latenza massima degli altri client %v. Più lento in totale, ma nessuno resta bloccato.", maxLat.Round(time.Millisecond))
	}
	return map[string]any{"found": len(found), "elapsed": el.String(), "maxLatency": maxLat.String()}
}

// Package live gestisce le "run" delle demo: ogni run è una simulazione Go vera
// che gira sul server e manda al browser eventi in streaming (Server-Sent Events).
//
// Tipi di evento:
//   - log   → riga del pannello log (livello, componente, messaggio)
//   - state → dati per le animazioni (Msg = tipo di stato, Data = payload)
//   - say   → frase "narrata" mostrata in grande sopra l'animazione
//   - done  → fine della run, con eventuale riepilogo
package live

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// Event è il messaggio inviato al browser, serializzato in JSON in una riga
// "data:" dello stream SSE.
type Event struct {
	Type  string `json:"type"`
	Level string `json:"level,omitempty"`
	Src   string `json:"src,omitempty"`
	Msg   string `json:"msg,omitempty"`
	Data  any    `json:"data,omitempty"`
	T     int64  `json:"t"` // millisecondi dall'inizio della run
}

// Run è una singola esecuzione di una demo: nasce con la richiesta GET
// /api/run/{demo} e muore quando la demo restituisce o il browser chiude lo stream.
// Le demo la usano da più goroutine: Log, State e Say sono sicure in concorrenza.
type Run struct {
	ID      string
	Ctx     context.Context // cancellato quando il browser chiude lo stream
	Control chan string     // comandi dal browser (es. "sigterm", "gc"); buffer 8
	Params  url.Values      // query string della richiesta, letta con Int, Bool e Str

	out      chan Event
	finished chan struct{} // chiuso quando la demo ha finito (out NON viene mai chiuso)
	start    time.Time
}

// emit invia un evento senza mai bloccare la simulazione per sempre:
// se il browser se n'è andato (ctx cancellato) o la run è finita, l'evento
// viene scartato.
//
// Nota: r.out non viene MAI chiuso. Goroutine rimaste indietro (es. worker
// ancora vivi quando l'utente preme «Ferma») potrebbero ancora chiamare emit:
// un send su un channel chiuso sarebbe un panic in goroutine, cioè proprio
// il crash che la demo 03 racconta.
func (r *Run) emit(e Event) {
	e.T = time.Since(r.start).Milliseconds()
	select {
	case r.out <- e:
	case <-r.Ctx.Done():
	case <-r.finished:
	}
}

// Log scrive una riga nel pannello log. level: debug|info|ok|warn|error
func (r *Run) Log(level, src, format string, args ...any) {
	r.emit(Event{Type: "log", Level: level, Src: src, Msg: fmt.Sprintf(format, args...)})
}

// State invia dati per le animazioni.
func (r *Run) State(kind string, data any) {
	r.emit(Event{Type: "state", Msg: kind, Data: data})
}

// Say mostra una frase "parlante" in sovrimpressione e la ripete nel log.
func (r *Run) Say(level, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.emit(Event{Type: "say", Level: level, Msg: msg})
}

// Sleep attende d oppure termina prima se la run viene cancellata.
// Restituisce false se la run è stata cancellata.
func (r *Run) Sleep(d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-r.Ctx.Done():
		return false
	}
}

// Elapsed restituisce il tempo trascorso dall'inizio della run.
func (r *Run) Elapsed() time.Duration { return time.Since(r.start) }

// DemoFunc è la firma di ogni demo.
type DemoFunc func(r *Run) (summary any)

// Hub tiene il registro delle demo e delle run attive.
type Hub struct {
	mu    sync.Mutex          // protegge runs: HandleRun e HandleControl girano in goroutine diverse
	demos map[string]DemoFunc // scritta solo all'avvio, poi solo letta: niente lock
	runs  map[string]*Run     // run attive, indicizzate per ID, per instradare i comandi
}

// NewHub crea un Hub vuoto.
func NewHub() *Hub {
	return &Hub{demos: map[string]DemoFunc{}, runs: map[string]*Run{}}
}

// Register associa una demo a un nome. Va chiamata prima di avviare il server.
func (h *Hub) Register(name string, fn DemoFunc) { h.demos[name] = fn }

// newID genera 12 caratteri esadecimali casuali: l'ID che il browser usa per
// mandare comandi alla propria run.
func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// HandleRun: GET /api/run/{demo}?param=... → stream SSE della simulazione.
func (h *Hub) HandleRun(w http.ResponseWriter, req *http.Request) {
	fn, ok := h.demos[req.PathValue("demo")]
	if !ok {
		http.Error(w, "demo sconosciuta", http.StatusNotFound)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming non supportato", http.StatusInternalServerError)
		return
	}

	// Il contesto della richiesta si chiude quando il browser chiude l'EventSource:
	// la simulazione lo riceve e si ferma. È lo stesso meccanismo dei servizi reali.
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()

	run := &Run{
		ID: newID(), Ctx: ctx, Control: make(chan string, 8), Params: req.URL.Query(),
		out: make(chan Event, 2048), finished: make(chan struct{}), start: time.Now(),
	}
	h.mu.Lock()
	h.runs[run.ID] = run
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.runs, run.ID)
		h.mu.Unlock()
	}()

	// intestazioni dello stream SSE: il browser riceve i messaggi man mano che arrivano
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	write := func(e Event) {
		b, _ := json.Marshal(e)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	// primo evento: l'ID della run, che il browser userà per /api/control
	write(Event{Type: "run", Data: map[string]string{"id": run.ID}})
	flusher.Flush()

	// La demo gira in una goroutine sua; questa goroutine (quella della
	// richiesta HTTP) fa solo da scrittore dello stream.
	go func() {
		defer close(run.finished)
		defer func() {
			// Anche qui: un panic in una goroutine non è coperto da nessun middleware,
			// quindi lo recuperiamo esplicitamente per non far morire il server della demo.
			if p := recover(); p != nil {
				run.emit(Event{Type: "log", Level: "error", Src: "server", Msg: fmt.Sprintf("panic nella demo: %v", p)})
			}
		}()
		summary := fn(run)
		run.emit(Event{Type: "done", Data: summary})
	}()

	for {
		select {
		case e := <-run.out:
			write(e)
			// svuota in blocco gli eventi già pronti, poi un solo Flush
		drain:
			for n := 0; n < 200; n++ {
				select {
				case e2 := <-run.out:
					write(e2)
				default:
					break drain
				}
			}
			flusher.Flush()
		case <-run.finished:
			// la demo è finita: invio quel che resta in coda ed esco
			for {
				select {
				case e := <-run.out:
					write(e)
				default:
					flusher.Flush()
					return
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

// HandleControl: POST /api/control/{id}?cmd=... → invia un comando a una run attiva.
// L'invio non blocca mai: se la demo non legge i comandi e la coda è piena,
// il browser riceve 429 invece di tenere appesa la richiesta.
func (h *Hub) HandleControl(w http.ResponseWriter, req *http.Request) {
	h.mu.Lock()
	run, ok := h.runs[req.PathValue("id")]
	h.mu.Unlock()
	if !ok {
		http.Error(w, "run non attiva", http.StatusNotFound)
		return
	}
	select {
	case run.Control <- req.URL.Query().Get("cmd"):
		w.WriteHeader(http.StatusAccepted)
	default:
		http.Error(w, "coda comandi piena", http.StatusTooManyRequests)
	}
}

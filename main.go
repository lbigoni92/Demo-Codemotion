// Demo Codemotion Milano 2026 — Go in produzione, raccontato con esempi vivi.
//
// Avvio:   go run .
// Docker:  docker compose up --build
// Apri:    http://localhost:8080
//
// Il programma è un unico server HTTP:
//   - GET /api/run/{demo}: avvia una simulazione e ne trasmette gli eventi (SSE)
//   - POST /api/control/{id}: manda un comando a una simulazione in corso
//   - GET /api/info: versione di Go, piattaforma e id di avvio (per il live reload)
//   - GET /api/llm: stato del modello locale (demo 06)
//   - GET /api/aitriage/log: il file di log generato dalla demo 06
//   - GET /healthz: usato dall'HEALTHCHECK di Docker
//   - /: il frontend statico (web/)
//
// Lo stesso eseguibile, lanciato con -child recover, fa da processo figlio
// per la demo 03 (vedi demos.RunRecoverChild).
//
// Nessuna dipendenza esterna: solo la standard library. Il frontend è incluso
// nel binario con embed, quindi funziona anche senza rete.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"democodemotion/demos"
	"democodemotion/live"
)

// webFS contiene la cartella web/ compilata dentro il binario: niente file da
// copiare accanto all'eseguibile, niente CDN.
//
//go:embed web
var webFS embed.FS

// envOr restituisce la variabile d'ambiente key, o def se è vuota. Serve a dare
// ai flag un default configurabile da Docker senza toccare la riga di comando.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	defPort, _ := strconv.Atoi(envOr("DEMO_PORT", "8080"))
	port := flag.Int("port", defPort, "porta HTTP (env DEMO_PORT)")
	host := flag.String("host", envOr("DEMO_HOST", "127.0.0.1"), "indirizzo di ascolto; in un container usare 0.0.0.0 (env DEMO_HOST)")
	noOpen := flag.Bool("no-browser", os.Getenv("DEMO_NO_BROWSER") == "1", "non aprire il browser all'avvio (env DEMO_NO_BROWSER=1)")
	healthcheck := flag.Bool("healthcheck", false, "controlla che il server risponda ed esce (per l'HEALTHCHECK di Docker)")
	child := flag.String("child", "", "uso interno: modalità processo figlio")
	flag.Parse()

	// Le due modalità speciali escono prima di aprire il server.
	if *child == "recover" {
		demos.RunRecoverChild()
		return
	}
	if *healthcheck {
		os.Exit(runHealthcheck(*port))
	}

	// Il nome registrato è quello usato nell'URL: /api/run/pipeline, /api/run/memory, …
	hub := live.NewHub()
	hub.Register("pipeline", demos.Pipeline)
	hub.Register("memory", demos.Memory)
	hub.Register("recover", demos.Recover)
	hub.Register("locks", demos.Locks)
	hub.Register("keysscan", demos.KeysScan)
	hub.Register("shutdown", demos.Shutdown)
	hub.Register("aitriage", demos.AITriage)

	// /.dockerenv esiste nei container Docker; DEMO_IN_CONTAINER copre gli altri runtime.
	inContainer := fileExists("/.dockerenv") || os.Getenv("DEMO_IN_CONTAINER") == "1"
	// boot identifica questa istanza del server: se cambia (Air ricompila e
	// riavvia dopo una modifica) il browser si ricarica da solo.
	boot := strconv.FormatInt(time.Now().UnixNano(), 36)
	static, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/run/{demo}", hub.HandleRun)
	mux.HandleFunc("POST /api/control/{id}", hub.HandleControl)
	mux.HandleFunc("GET /api/info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		fmt.Fprintf(w, `{"go":%q,"os":%q,"arch":%q,"cpus":%d,"container":%t,"boot":%q}`,
			runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.NumCPU(), inContainer, boot)
	})
	mux.HandleFunc("GET /api/llm", demos.LLMStatus)            // il modello locale della demo 06 è pronto?
	mux.HandleFunc("GET /api/aitriage/log", demos.AITriageLog) // il file di log generato dall'ultima run 06
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "ok") })
	mux.Handle("/", http.FileServerFS(static))

	// Listen separato da Serve: se la porta è occupata lo diciamo subito, con un
	// messaggio chiaro, prima di stampare il banner e aprire il browser.
	ln, err := net.Listen("tcp", net.JoinHostPort(*host, strconv.Itoa(*port)))
	if err != nil {
		log.Fatalf("impossibile ascoltare su %s:%d (porta occupata? prova con -port 8081): %v", *host, *port, err)
	}
	url := "http://localhost:" + strconv.Itoa(*port)
	fmt.Println("==================================================")
	fmt.Println("  Demo Codemotion - Go in produzione")
	fmt.Printf("  in ascolto su %s:%d\n", *host, *port)
	if inContainer {
		fmt.Println("  (nel container: apri la porta pubblicata dal compose)")
	} else {
		fmt.Println("  " + url)
	}
	fmt.Println("  Ctrl+C per uscire")
	fmt.Println("==================================================")
	if !*noOpen && !inContainer {
		// piccolo ritardo: il server deve essere in Serve quando il browser chiede la pagina
		go func() { time.Sleep(400 * time.Millisecond); openBrowser(url) }()
	}

	// Graceful shutdown, come nella demo 05: SIGTERM (docker stop) o Ctrl+C
	// cancellano baseCtx, così anche gli stream SSE aperti si chiudono subito,
	// poi Shutdown aspetta le richieste in corso entro una deadline.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	baseCtx, cancelBase := context.WithCancel(context.Background())
	srv := &http.Server{
		Handler: mux,
		// limita i client che mandano gli header un byte alla volta (Slowloris)
		ReadHeaderTimeout: 5 * time.Second,
		// ogni richiesta eredita baseCtx: cancellarlo chiude subito anche gli
		// stream SSE, che altrimenti terrebbero Shutdown in attesa fino alla deadline
		BaseContext: func(net.Listener) context.Context { return baseCtx },
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	case <-ctx.Done():
		log.Println("segnale ricevuto: chiudo le simulazioni in corso e spengo il server")
		cancelBase()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown non completato entro la deadline: %v", err)
		}
		log.Println("uscita pulita")
	}
}

// runHealthcheck chiama /healthz sul server locale e restituisce l'exit code
// per Docker: 0 sano, 1 malato. Esiste perché l'immagine scratch non ha curl.
func runHealthcheck(port int) int {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(fmt.Sprintf("http://127.0.0.1:%d/healthz", port))
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck fallito:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.StatusCode)
		return 1
	}
	return 0
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// openBrowser apre url nel browser predefinito. Su Linux senza un display
// (server, WSL senza GUI) non fa nulla. Gli errori sono ignorati: aprire il
// browser è una comodità, non un requisito.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return
		}
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

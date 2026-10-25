package demos

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"democodemotion/live"
)

// recover() non attraversa le goroutine.
//
// Un middleware di recover protegge solo la goroutine che esegue l'handler.
// Se l'handler lancia `go func(){ ... panic ... }()`, quel panic non risale a
// nessuno: il runtime stampa lo stack e termina TUTTO il processo (exit code 2).
//
// Per mostrarlo davvero senza far morire il server della demo, la simulazione
// avvia un PROCESSO FIGLIO (lo stesso eseguibile con -child recover) che fa
// girare un piccolo server HTTP con il middleware. Il padre gli manda le
// richieste e legge il suo stdout/stderr.

// ─────────────────────────── processo figlio ───────────────────────────
//
// Il figlio parla con il padre su stdout, una riga per messaggio:
//   READY|<porta>|<pid>     il server è in ascolto
//   LOG|<livello>|<testo>   una riga da mostrare nel pannello log
// Lo stack trace di un panic non recuperato lo scrive il runtime su stderr.

// childLog scrive una riga LOG| su stdout, che il padre inoltra al browser.
func childLog(level, format string, args ...any) {
	fmt.Printf("LOG|%s|%s\n", level, fmt.Sprintf(format, args...))
}

// RecoverMiddleware avvolge un handler con defer + recover(): un panic
// nell'handler diventa una risposta 500 e il processo resta vivo. Protegge
// solo la goroutine che esegue l'handler: non le goroutine che l'handler lancia.
func RecoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				childLog("warn", "middleware: recovered panic su %s: %v → rispondo 500, il processo resta vivo", req.URL.Path, p)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, req)
	})
}

// SafeGo avvia fn in una nuova goroutine che ha il proprio defer + recover():
// è l'unico modo di proteggere il processo da un panic dentro una goroutine.
// Il panic viene loggato e la goroutine termina; il processo continua.
func SafeGo(name string, fn func()) {
	go func() {
		defer func() {
			if p := recover(); p != nil {
				childLog("warn", "SafeGo(%s): recovered panic nella goroutine: %v", name, p)
			}
		}()
		fn()
	}()
}

// RunRecoverChild è il main del processo figlio (flag -child recover): un server
// HTTP su una porta libera di 127.0.0.1 con due rotte. /work?mode=… provoca il
// panic richiesto, /health dice se il processo è ancora vivo. Non ritorna:
// termina quando il padre lo uccide o quando un panic non recuperato lo fa morire.
func RunRecoverChild() {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		childLog("ok", "GET /health → 200, sono vivo (pid %d)", os.Getpid())
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/work", func(w http.ResponseWriter, req *http.Request) {
		mode := req.URL.Query().Get("mode")
		childLog("info", "handler /work avviato (mode=%s) nella goroutine della richiesta", mode)
		switch mode {
		case "handler":
			var m map[string]int
			m["boom"] = 1 // panic: assignment to entry in nil map
		case "goroutine":
			go func() {
				childLog("info", "export in background avviato con `go func()`")
				time.Sleep(900 * time.Millisecond)
				var rows []string
				childLog("error", "accesso a rows[3] su slice vuota…")
				_ = rows[3] // panic: index out of range
			}()
			childLog("ok", "handler: risposta 202 Accepted, l'export continua in background")
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, "accepted")
		case "safego":
			SafeGo("export", func() {
				childLog("info", "export in background avviato con SafeGo()")
				time.Sleep(900 * time.Millisecond)
				var rows []string
				childLog("error", "accesso a rows[3] su slice vuota…")
				_ = rows[3]
			})
			childLog("ok", "handler: risposta 202 Accepted, l'export continua in background")
			w.WriteHeader(http.StatusAccepted)
			fmt.Fprint(w, "accepted")
		default:
			fmt.Fprint(w, "ok")
		}
	})
	// porta 0: la sceglie il sistema operativo, così più run non si scontrano
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("READY|%d|%d\n", ln.Addr().(*net.TCPAddr).Port, os.Getpid())
	// ErrorLog nil: http.Server usa il logger standard (su stderr)
	srv := &http.Server{Handler: RecoverMiddleware(mux), ErrorLog: nil}
	_ = srv.Serve(ln)
}

// ─────────────────────────── processo padre ───────────────────────────

// Recover è la demo 03: avvia il processo figlio, gli manda una richiesta che
// provoca un panic, poi controlla con /health se è sopravvissuto. Restituisce
// la modalità, se il figlio è vivo e il suo exit code.
func Recover(r *live.Run) any {
	mode := r.Str("mode", "handler")
	exe, err := os.Executable()
	if err != nil {
		r.Log("error", "demo", "impossibile trovare l'eseguibile: %v", err)
		return nil
	}

	// CommandContext uccide il figlio se la run viene fermata o dopo 15 s:
	// nessun processo orfano, qualunque cosa succeda.
	ctx, cancel := context.WithTimeout(r.Ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-child", "recover")
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()

	r.State("proc", map[string]any{"status": "starting"})
	r.Say("info", "Avvio un processo figlio con un server HTTP protetto da RecoverMiddleware")
	if err := cmd.Start(); err != nil {
		r.Log("error", "demo", "avvio processo figlio fallito: %v", err)
		return nil
	}

	// Due lettori in parallelo: stdout porta il protocollo READY/LOG, stderr lo
	// stack trace del panic. Letti in sequenza, uno dei due pipe potrebbe
	// riempirsi e bloccare il figlio.
	ready := make(chan string, 1)
	var readers sync.WaitGroup
	readers.Add(2)
	go func() {
		defer readers.Done()
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "READY|"):
				p := strings.Split(line, "|")
				r.State("proc", map[string]any{"status": "running", "pid": p[2]})
				r.Log("ok", "figlio", "server in ascolto su 127.0.0.1:%s (pid %s)", p[1], p[2])
				ready <- p[1]
			case strings.HasPrefix(line, "LOG|"):
				p := strings.SplitN(line, "|", 3)
				r.Log(p[1], "figlio", "%s", p[2])
				if strings.Contains(p[2], "recovered panic") {
					r.State("step", map[string]any{"name": "recovered"})
				}
			default:
				r.Log("info", "figlio", "%s", line)
			}
		}
	}()
	go func() {
		defer readers.Done()
		sc := bufio.NewScanner(stderr)
		first := true
		for sc.Scan() {
			line := sc.Text()
			if first && strings.HasPrefix(line, "panic:") {
				r.State("step", map[string]any{"name": "crash"})
				r.Say("error", "%s — nessun middleware può intercettarlo: il runtime termina il processo", line)
				first = false
			}
			r.Log("error", "stderr", "%s", line)
		}
	}()

	var port string
	select {
	case port = <-ready:
	case <-ctx.Done():
		return nil
	}
	base := "http://127.0.0.1:" + port
	client := &http.Client{Timeout: 3 * time.Second}

	call := func(path string) (int, error) {
		resp, err := client.Get(base + path)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode, nil
	}

	// 1) richiesta che provoca il panic (le pause lasciano il tempo alle animazioni)
	r.Sleep(900 * time.Millisecond)
	r.State("step", map[string]any{"name": "request", "path": "/work?mode=" + mode})
	r.Log("info", "client", "GET /work?mode=%s", mode)
	code, err := call("/work?mode=" + mode)
	if err != nil {
		r.Log("error", "client", "richiesta fallita: %v", err)
	} else {
		r.State("step", map[string]any{"name": "response", "code": code})
		r.Log(map[bool]string{true: "warn", false: "ok"}[code >= 500], "client", "risposta: HTTP %d", code)
	}

	// 2) attendo che l'eventuale goroutine in background esploda
	r.Sleep(2200 * time.Millisecond)

	// 3) il processo è ancora vivo?
	r.State("step", map[string]any{"name": "health"})
	r.Log("info", "client", "GET /health (il processo è ancora vivo?)")
	code, err = call("/health")
	alive := err == nil && code == 200
	if alive {
		r.State("step", map[string]any{"name": "alive"})
		switch mode {
		case "handler":
			r.Say("ok", "Il middleware ha recuperato il panic dell'handler: 500 al client, processo vivo")
		case "safego":
			r.Say("ok", "SafeGo ha recuperato il panic nella goroutine: processo vivo")
		}
	} else {
		r.State("step", map[string]any{"name": "dead"})
		r.Log("error", "client", "/health non risponde: %v", err)
	}

	// chiusura del figlio: se è vivo lo uccidiamo noi, se è morto raccogliamo
	// il suo exit code (2 per un panic non recuperato)
	if alive {
		_ = cmd.Process.Kill()
	}
	// Prima si finisce di leggere stdout e stderr, poi si chiama Wait: Wait chiude
	// i pipe, e chiamarlo prima perderebbe le ultime righe (proprio lo stack trace).
	// I lettori terminano da soli: quando il figlio esce, i pipe arrivano a EOF.
	readers.Wait()
	waitErr := cmd.Wait()
	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	if !alive {
		r.State("proc", map[string]any{"status": "dead", "code": exitCode})
		r.Say("error", "Processo figlio MORTO con exit code %d. In Kubernetes: CrashLoopBackOff e tutte le richieste in corso perse", exitCode)
	} else {
		r.State("proc", map[string]any{"status": "stopped"})
		r.Log("info", "demo", "processo figlio fermato dalla demo (%v)", waitErr)
	}
	return map[string]any{"mode": mode, "alive": alive, "exitCode": exitCode}
}

# Demo Codemotion · Go in produzione

Sito web interattivo per il tavolo Tech Expert su Go (Codemotion Milano 2026).
Sei storie vere di Go in produzione: ogni pagina esegue **codice Go vero** sul
laptop e lo racconta passo per passo, con animazioni guidate dagli eventi del
codice e log in tempo reale.

Requisiti completi, decisioni e stato di verifica: [REQUISITI.md](REQUISITI.md).

## Avvio

### Senza Docker
- **Con Go 1.24+:** `go run .` (si apre il browser su <http://localhost:8080>).
- Porta diversa: `go run . -port 8081`. Senza aprire il browser: `-no-browser`.

### Con Docker / Docker Compose
- Da terminale, poi apri <http://localhost:8080>:

  ```
  docker compose up --build -d     # avvia
  docker compose logs -f           # log del server
  docker compose down              # ferma (SIGTERM → shutdown graceful)
  ```

- Senza compose: `docker build -t demo-codemotion .` e poi
  `docker run --rm -p 127.0.0.1:8080:8080 demo-codemotion`.
- Per mostrarla da altri dispositivi sulla stessa rete (es. il telefono di chi è al tavolo):
  PowerShell `$env:DEMO_BIND="0.0.0.0"; docker compose up -d`. Di default la porta è
  pubblicata solo su `127.0.0.1`, così sulla wifi della conferenza non è esposta.
- Porta diversa: `$env:DEMO_PORT="8081"; docker compose up -d`.

Il compose include `docker-compose.llm.yml`: il **modello AI** della demo 06
(llama.cpp server con Qwen3-1.7B, ~1,1 GB scaricato una volta sola in un volume)
gira nel container `llm`, su una rete interna senza internet e senza porte
pubblicate. Il primo `docker compose up` lo scarica: fallo prima dell'evento.
Per un modello più preciso ma più lento (es. il Qwen3-8B di k9slogger) cambia
le variabili `LLM_MODEL_*` e il comando in `docker-compose.llm.yml`.

L'immagine è multi-stage: `golang:1.24-alpine` compila ed esegue `go vet` e
`go test` (se falliscono la build si ferma), il runtime è `scratch` con il solo
binario statico (~10 MB), utente non root `65532`, filesystem in sola lettura,
`cap_drop: ALL`, `no-new-privileges` e `mem_limit: 1g`. Non c'è curl nell'immagine:
l'`HEALTHCHECK` usa il binario stesso (`/demo-codemotion -healthcheck`, che
chiama `/healthz`). `docker compose stop` manda SIGTERM: il server cancella le
simulazioni in corso, chiude gli stream e fa `http.Server.Shutdown` con deadline.
È lo stesso pattern della demo 05, e lo stop dura meno di un secondo invece di
aspettare il SIGKILL.

### Sviluppo con hot reload (Air)
- `docker compose -f docker-compose.dev.yml up --build`, poi apri <http://localhost:8080>.
- Modifichi un `.go` o un file in `web/` dall'IDE sul PC. [Air](https://github.com/air-verse/air)
  (installato con `go install github.com/air-verse/air@latest`) ricompila e riavvia,
  e **il browser si ricarica da solo** restando sulla stessa pagina. Se è in corso una
  simulazione, aspetta che finisca.
- Se la build fallisce, l'errore compare nei log del container e resta in esecuzione la
  versione precedente.
- File coinvolti: `Dockerfile.dev` (golang:1.27-alpine + Air: `air@latest` richiede
  Go ≥ 1.26), `docker-compose.dev.yml` (sorgente montato in `/app`, volumi di cache per
  `go build` e moduli), `.air.toml`.
- Dettagli che contano su Windows: `poll = true` (Docker Desktop non propaga gli eventi
  del filesystem nei volumi montati), `include_ext` con `html/css/js` (il frontend è
  dentro il binario con `embed`), `send_interrupt = true` e `stop_signal: SIGINT`
  (shutdown graceful a ogni riavvio; con SIGTERM Air lascerebbe orfana la demo).
- Senza Docker, con Go e Air installati: `air -c .air.toml`.
- Il binario di sviluppo va in `tmp/` (ignorato da git e da Docker, cancellato all'uscita).

Configurazione (flag o variabili d'ambiente):

| Flag | Variabile | Default | Note |
|---|---|---|---|
| `-host` | `DEMO_HOST` | `127.0.0.1` | nel container `0.0.0.0` |
| `-port` | `DEMO_PORT` | `8080` | |
| `-no-browser` | `DEMO_NO_BROWSER=1` | apre il browser | nel container non apre mai il browser |
| `-healthcheck` | | | controlla `/healthz` ed esce con 0/1 |

## Le pagine

| # | Pagina | Cosa mostra | Cosa è reale e cosa è simulato |
|---|---|---|---|
| 01 | Pipeline a channel | Una pizzeria: Cassa → Preparazione → Forno → Consegna, un cuoco (goroutine) per ingrediente con `WaitGroup` + `atomic.Bool`, backpressure, semaforo, `errorCh`, chiusura che scorre a valle | Tutto reale: goroutine e channel veri, `runtime.NumGoroutine()` |
| 02 | Heap Go vs RSS | Ogni formula apre una VM Lua via cgo: `runtime.MemStats` non la vede, Kubernetes sì. GOMEMLIMIT, ordine LIFO delle `defer`, OOMKilled | Heap Go **reale** (`runtime.ReadMemStats`, `debug.SetMemoryLimit`); memoria C della VM **simulata** con un contatore (niente cgo) |
| 03 | recover e goroutine | Il middleware recupera il panic dell'handler, ma non quello di una `go func()`; `SafeGo` | **Reale**: viene avviato un processo figlio che muore davvero con exit code 2 |
| 04 | Lock distribuiti | `SET NX PX`, TTL più corto del job → doppia esecuzione, release fallita, watchdog `Extend()`; KEYS vs SCAN | Redis simulato in memoria con la stessa semantica; latenze misurate davvero |
| 05 | Graceful shutdown | SIGTERM brutale (`os.Exit`) vs graceful: stop ingressi, drain con deadline, `cancel()` del context, `errors.Join` | Reale: worker, context e timeout veri (il SIGTERM è il bottone) |
| 06 | Go + AI | Go genera un file di 10.000 righe di log con 8 bug nascosti, lo legge, sanifica e raggruppa; un LLM locale in un altro container propone i bug in streaming; Go valida, un umano decide, e la verifica dice quanti bug sono stati trovati | **Reale**: file, pipeline Go e modello (Qwen3-1.7B su llama.cpp, container `llm` senza internet). Senza modello: regole fisse, dichiarate |

Ogni pagina ha: spiegazione «punto per punto» che si accende durante la run (il pulsante ⓘ di ogni passo apre il dettaglio «sotto il cofano»),
scenari pronti (un clic avvia o riavvia da capo), riquadro «Cosa guardare», animazione, frase del narratore nei momenti chiave, snippet del
codice e pannello log filtrabile.

Tastiera: `1`–`6` pagine, `0` home, `R` avvia o ricomincia, `S` ferma, `L` mostra/nasconde il log.

## Struttura

```
main.go            server HTTP, embed del frontend, shutdown graceful, healthcheck, processo figlio
Dockerfile         build multi-stage → immagine scratch non root
Dockerfile.dev     immagine di sviluppo con Air · docker-compose.dev.yml · .air.toml
docker-compose.yml avvio con porta su 127.0.0.1, read-only, limite di memoria
live/              run, eventi SSE (log / state / say / done), comandi dal browser
demos/             una simulazione per file, commentata in italiano
  pipeline.go  memory.go  recover.go  locks.go  shutdown.go  aitriage.go
  demos_test.go    test: lock, sanificazione, fingerprint, SafeGo
web/               index.html, app.css, core.js, pages.js (vanilla JS, nessuna libreria)
```

Ogni demo è una funzione `func(r *live.Run) any` che chiama `r.Log`, `r.State` e
`r.Say`. Il browser riceve gli eventi via Server-Sent Events (`/api/run/{demo}`)
e manda comandi come SIGTERM, GC o approva/scarta su `/api/control/{id}`.
Chiudere la pagina cancella il context della richiesta e ferma la simulazione.

## Test e build

```
go vet ./...
go test -race ./...
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o demo-codemotion.exe .
```

## Una chicca da raccontare

Durante lo sviluppo, premere «Ferma» a metà della demo 05 faceva morire l'intero
server. Una goroutine rimasta viva scriveva su un channel già chiuso:
`panic: send on closed channel`, in una goroutine, quindi nessun recover. È
esattamente la storia della demo 03. La correzione è in `live/live.go`: il
channel degli eventi non viene mai chiuso e la fine della run è segnalata da un
channel `finished` separato.

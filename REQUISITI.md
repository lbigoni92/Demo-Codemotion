# Requisiti · Demo Codemotion «Go in produzione»

Documento dei requisiti del progetto allo stato attuale (24 ottobre 2026).
Raccoglie quello che è stato richiesto, le decisioni prese durante lo sviluppo
con la loro motivazione e lo stato di verifica di ciascun punto.

**Legenda dell'origine:** **[R]** richiesto esplicitamente · **[D]** deciso durante lo sviluppo, con motivazione.
**Legenda della verifica:** ✅ verificato · ⚠️ non verificabile dall'ambiente di sviluppo, da provare sul PC.

---

## 1. Contesto e obiettivo

| ID | Requisito | Origine |
|---|---|---|
| CTX-01 | Supporto al tavolo Tech Expert su Go a **Codemotion Milano, 28 ottobre 2026, 15:30–17:30, tavolo 6**. | [R] |
| CTX-02 | Il progetto simula i punti emersi dall'analisi dei repo `apparound.spm.icm`, `apparound.spm.icm.pipeline`, `Apparound.Background.Job` e `k9slogger`, e li **spiega punto per punto**. | [R] |
| CTX-03 | Il codice aziendale **non** viene mostrato né copiato: la demo ricostruisce i pattern in forma generica e anonimizzata. k9slogger, che è un progetto personale, può essere citato. | [D] codice proprietario con logica di business |
| CTX-04 | Posizione: `C:\Users\lbigoni\Documents\LucaBigoni\Demo Codemotion`. | [R] |

## 2. Requisiti generali dell'interfaccia

| ID | Requisito | Origine | Stato |
|---|---|---|---|
| UI-01 | Sito web con **interfaccia grafica moderna**. | [R] | ✅ |
| UI-02 | **Menu a sinistra** con una voce per ogni funzionalità, più una pagina introduttiva. | [R] | ✅ |
| UI-03 | Per ogni pagina un'interfaccia che **fa capire cosa accade in ogni caso, con animazioni**. | [R] | ✅ |
| UI-04 | Per ogni funzionalità **compaiono a video i log** opportuni, in tempo reale. | [R] | ✅ |
| UI-05 | Tutto **«parlante» e interattivo**: un narratore in alto racconta i momenti chiave, i passi della spiegazione si accendono durante la simulazione e i parametri sono modificabili. | [R] | ✅ |
| UI-06 | Ogni pagina contiene: parametri, «scenari pronti» da un clic, riquadro «Cosa guardare» (cambia con lo scenario scelto), animazione, spiegazione «Punto per punto» con un pulsante ⓘ per passo che apre il dettaglio «sotto il cofano» (runtime Go, kernel, Redis, Kubernetes), riquadro «Da portarsi a casa», snippet del codice Go con le righe chiave evidenziate. | [D] | ✅ |
| UI-07 | Pannello log in basso: livelli debug/info/ok/warn/error filtrabili, ricerca testuale, «segui» automatico, pulizia, separatore per ogni run, massimo 3000 righe. | [D] | ✅ |
| UI-08 | Scorciatoie da tastiera: `0`–`6` per le pagine, `R` avvia, `S` ferma, `L` mostra o nasconde il log. | [D] | ✅ |
| UI-09 | Lingua dell'interfaccia, dei log e dei commenti nel codice: **italiano**. | [D] lingua del presentatore e del pubblico | ✅ |
| UI-10 | Layout adattivo: a 1366×768 la colonna laterale va sotto l'animazione e la pipeline scorre in orizzontale; quando parte una simulazione, l'animazione viene portata in vista. | [D] i portatili da conferenza hanno schermi piccoli | ✅ |
| UI-11 | Ricarica automatica del browser quando il server riparte con un codice nuovo (id di avvio esposto da `/api/info`), restando sulla stessa pagina; se una simulazione è in corso, si aspetta che finisca. | [D] necessaria per l'hot reload | ✅ |
| UI-12 | Ogni pagina dichiara cosa è **reale** e cosa è **simulato**. | [D] onestà con il pubblico | ✅ |
| UI-13 | Ogni scenario si può **rilanciare senza uscire dalla pagina**: il pulsante principale non si disabilita mai («↻ Ricomincia» durante la run, «↻ Rilancia» alla fine), uno scenario pronto riparte sempre da capo, il tasto `R` fa lo stesso. Le animazioni asincrone della run precedente vengono scartate (`Runner.gen`). | [R] | ✅ |

## 3. Requisiti funzionali per pagina

Tutte le simulazioni girano **davvero in Go** sul server. Le animazioni sono guidate
dagli eventi emessi dal codice, non da timer nel browser.

### 3.1 Pagina 01 · Pipeline a channel

| ID | Requisito | Stato |
|---|---|---|
| PIP-01 | Analogia della pizzeria (il dominio reale non viene mostrato): Cassa → Preparazione → Forno → Consegna collegati da channel («banconi»); ogni stadio è una goroutine con `for p := range in` e `defer close(out)`. | ✅ |
| PIP-02 | Fan-out nella Preparazione: un cuoco (goroutine) per ingrediente, con `sync.WaitGroup` e un flag `atomic.Bool` per gli errori. | ✅ |
| PIP-03 | Buffer dei channel configurabile (0 = rendezvous); animazione dell'occupazione di ogni channel. | ✅ |
| PIP-04 | Backpressure visibile: lo stadio bloccato su un channel pieno viene evidenziato e loggato. | ✅ |
| PIP-05 | Limite opzionale del fan-out tramite semaforo `chan struct{}`. | ✅ |
| PIP-06 | Ingrediente finito: l'errore viaggia su `errorCh`, la pizza viene scartata e non va in forno. | ✅ |
| PIP-07 | Goroutine vive misurate con `runtime.NumGoroutine()`, con grafico e picco. | ✅ |
| PIP-08 | Parametri: pizze 1–20 (8), ingredienti 1–6 (3), posti sul bancone 0–6 (0), cuochi 0–8 (0 = illimitati), «Forno lento», «Pizza #3: mozzarella finita». Scenari: *Rendezvous*, *Forno lento: collo di bottiglia*, *Manca un ingrediente*, *Pochi cuochi: semaforo*. | ✅ |
| PIP-09 | Grafica: pizze come gettoni che si muovono tra i reparti (pallide da crude, colorate dopo il forno), posti del buffer visibili sui banconi con `len/cap` reale, una riga per cuoco con lo stato della goroutine (aspetta un grembiule, prepara, pronto, finita), grembiuli in uso, barra di cottura con la durata reale, timbro `close()` sul bancone quando il canale viene chiuso, reparto rosso quando è bloccato. Gli eventi «sul bancone» e «nel reparto» arrivano da goroutine diverse: il browser ignora gli spostamenti all'indietro. | ✅ |

### 3.2 Pagina 02 · Heap Go vs RSS

| ID | Requisito | Stato |
|---|---|---|
| MEM-01 | Ogni formula apre una «VM Lua» la cui memoria nativa non è visibile al GC; grafico Heap Go vs memoria C vs RSS stimato, con il limite del pod e GOMEMLIMIT come linee di riferimento. | ✅ |
| MEM-02 | Heap Go **reale** (`runtime.ReadMemStats`), GOMEMLIMIT **reale** (`debug.SetMemoryLimit`, ripristinato a fine run); memoria C **simulata** con un contatore, per non richiedere cgo. | ✅ |
| MEM-03 | Due viste a confronto: «cosa vede Go» e «cosa vede Kubernetes». | ✅ |
| MEM-04 | Bug attivabile «manca `defer L.Close()`»: la memoria C non viene mai liberata. | ✅ |
| MEM-05 | Pulsante «Forza `runtime.GC()`»: l'heap Go scende, la memoria C no. | ✅ |
| MEM-06 | Animazione della pila delle `defer` in ordine LIFO, ricavata dalle `defer` reali eseguite. | ✅ |
| MEM-07 | Superamento del limite del pod → sovrimpressione «OOMKilled» (exit code 137), chiudibile con un clic. | ✅ |
| MEM-08 | Parametri: formule 10–120 (40), parallelismo 1–12 (6), memoria per VM 8–48 MiB (24), limite pod 256–2048 MiB (512), GOMEMLIMIT 0–512 MiB (0 = disattivo), bug leak. Scenari: *Tutto ok*, *Leak della VM*, *GOMEMLIMIT non basta*, *Pod troppo piccolo*. | ✅ |
| MEM-09 | Le run della pagina sono serializzate da un mutex, perché contatori e `debug.SetMemoryLimit` sono globali al processo: un rilancio a metà aspetta che la run precedente chiuda le sue VM e ripristini il limite. | ✅ |

### 3.3 Pagina 03 · recover e goroutine

| ID | Requisito | Stato |
|---|---|---|
| REC-01 | Avvio di un **processo figlio reale** (lo stesso eseguibile con `-child recover`) con un server HTTP protetto da `RecoverMiddleware`. | ✅ |
| REC-02 | Scenario *panic nell'handler*: il middleware lo recupera, il client riceve 500, `/health` risponde 200. | ✅ |
| REC-03 | Scenario *panic in una goroutine*: il processo muore con **exit code 2**; stack trace reale mostrato a video; `/health` → connessione rifiutata. | ✅ (Linux e container) ⚠️ (Windows) |
| REC-04 | Scenario *SafeGo*: la goroutine ha il proprio `defer recover()`, il processo resta vivo. | ✅ |
| REC-05 | Animazione del pacchetto client → middleware → handler/goroutine, con il fulmine del panic, lo «scudo» che intercetta, il processo che diventa rosso e la risposta HTTP. | ✅ |

### 3.4 Pagina 04 · Lock distribuiti

| ID | Requisito | Stato |
|---|---|---|
| LCK-01 | Redis **simulato in memoria** con la stessa semantica: `SET NX PX`, TTL, `Extend` e `Unlock` compare-and-delete. | ✅ (test) |
| LCK-02 | Tab *TTL e doppia esecuzione*: N worker contendono M job; se il job dura più del TTL il lock scade, un altro worker lo prende → **doppia esecuzione**; il rilascio fallisce con «lock was not held or expired». | ✅ |
| LCK-03 | Lock scaduto mentre il job è ancora in corso mostrato come «SCADUTO mentre Wn lavora ancora». | ✅ |
| LCK-04 | Watchdog opzionale che chiama `Extend()` ogni TTL/3: nessuna doppia esecuzione. | ✅ |
| LCK-05 | Tab *KEYS vs SCAN*: un client misura la latenza reale di un GET ogni 20 ms mentre si esegue `KEYS` (blocca tutto) oppure `SCAN` a blocchi da 500. | ✅ |
| LCK-06 | Parametri TTL: worker 2–5 (3), job 1–8 (5), TTL 500–5000 ms (1500), durata del job 500–6000 ms (2600), watchdog. KEYS/SCAN: 10.000–300.000 chiavi (150.000), comando. | ✅ |

### 3.5 Pagina 05 · Graceful shutdown

| ID | Requisito | Stato |
|---|---|---|
| SHD-01 | Server simulato con N worker e traffico continuo; pulsante **«Invia SIGTERM»**, inviato automaticamente dopo 60 s. | ✅ |
| SHD-02 | Strategia *brutale* (`<-sigCh; os.Exit`): le richieste in corso vengono troncate. | ✅ |
| SHD-03 | Strategia *graceful*: stop agli ingressi (503), drain con deadline (`wg` + `done` + `select` con `context.WithTimeout`), allo scadere `cancel()` del context con rollback, poi `errors.Join` degli errori. | ✅ |
| SHD-04 | Fasi animate (Running → SIGTERM → Draining → Cancel context → Uscita pulita / Processo ucciso), barra della deadline, corsie dei worker, richieste rifiutate e contatori. | ✅ |
| SHD-05 | Parametri: strategia, deadline 1000–8000 ms (3000), worker 2–6 (4). | ✅ |

### 3.6 Pagina 06 · Go + AI: triage dei log

| ID | Requisito | Stato |
|---|---|---|
| AI-01 | Il modello è **vero**: un LLM locale (Qwen3-1.7B Q4_K_M su llama.cpp server) in un **container separato** (`llm`), raggiunto via API compatibile OpenAI. Si può cambiare modello (es. Qwen3-8B di k9slogger) modificando `docker-compose.llm.yml`. Motivazione del modello piccolo: su questo PC Qwen3-8B genera ~3 token/s (un'analisi richiederebbe 3–4 minuti), Qwen3-1.7B ~13–16 token/s. [R] | ✅ (24 ottobre: 6 bug su 8 trovati, ~40 s in tutto) |
| AI-02 | A ogni run Go **genera un file di log** di 10.000 righe (1.000–20.000), un'ora di 4 servizi nel formato `timestamp livello servizio pod trace messaggio`, con **8 bug nascosti** nel rumore (panic e CrashLoopBackOff, deadlock Postgres, pool esaurito, lock scaduto e doppie esecuzioni, token scaduto, OOMKilled, goroutine leak, password Redis errata). Il file si apre da `GET /api/aitriage/log`. [R] | ✅ (test) |
| AI-03 | Go legge il file riga per riga (`bufio.Scanner`), fa il parsing e **sanifica prima di tutto** (email, token Bearer, password); le righe malformate si contano e si saltano. | ✅ (test) |
| AI-04 | Guardrail: le righe con prompt injection sono marcate come dato non fidato ed **escluse dal prompt**. Il container del modello sta su una rete Docker `internal: true` (niente internet), senza porte pubblicate, in sola lettura, `cap_drop: ALL`; il modello non ha tool. | ✅ |
| AI-05 | Impronta (livello + servizio + messaggio con UUID e numeri normalizzati): 10.000 righe diventano qualche decina di gruppi; al modello vanno solo WARN ed ERROR, al massimo N gruppi (5–30, default 20). Timeline di WARN ed ERROR per minuto. | ✅ (test) |
| AI-06 | Chiamata in **streaming** con output vincolato da **JSON schema** (grammatica di llama.cpp) e thinking disattivato; la risposta compare nel browser mentre viene generata, con token al secondo. Il prompt inviato è visibile. Cancellare la run interrompe la generazione. Il modello risponde in inglese (un modello da 1,7 miliardi di parametri scrive un inglese tecnico molto migliore del suo italiano); gli id dei gruppi scritti come «g7» vengono normalizzati in «g07». Misurato: prompt ~1.000 token in ~3 s, risposta ~550 token in ~35 s. | ✅ |
| AI-07 | Go **valida** le proposte: i gruppi citati devono esistere, una proposta senza prove valide viene scartata. Approva / Scarta per ogni proposta, una sola decisione ciascuna, attesa massima 2 minuti. | ✅ |
| AI-08 | **Verifica onesta**: le proposte vengono confrontate con gli 8 bug iniettati e la pagina mostra quanti ne sono stati trovati, quali mancano e quante proposte non corrispondono a bug nascosti. La verità non viene mai mandata al modello. | ✅ |
| AI-09 | Se il modello non è raggiungibile (es. `go run .` senza Docker) o risponde male, le proposte arrivano da **regole fisse** e la pagina lo dichiara. Il badge in alto mostra lo stato del modello (`GET /api/llm`). | ✅ |
| AI-10 | Nomi di pod e servizi generici (`api-…`, `pipeline-…`, `worker-…`, `auth-…`), non quelli reali. | ✅ |

## 4. Requisiti tecnici

| ID | Requisito | Origine | Stato |
|---|---|---|---|
| TEC-01 | Backend in Go, **solo standard library**; nessuna dipendenza esterna in `go.mod` (Go 1.24). | [D] funziona offline, niente supply chain | ✅ |
| TEC-02 | Frontend vanilla HTML/CSS/JS **senza CDN né font esterni**, incluso nel binario con `embed`. | [D] il wifi in conferenza è inaffidabile | ✅ |
| TEC-03 | Streaming degli eventi con Server-Sent Events: `GET /api/run/{demo}`; comandi su `POST /api/control/{id}`; `GET /api/info`, `GET /healthz`. | [D] | ✅ |
| TEC-04 | Chiudere lo stream (pagina cambiata, «Ferma») cancella il context e ferma la simulazione. | [D] | ✅ |
| TEC-05 | Il channel degli eventi **non viene mai chiuso**; la fine della run è segnalata da un channel `finished` separato. | [D] risolve un panic reale trovato in collaudo (`send on closed channel` in una goroutine) | ✅ |
| TEC-06 | Panic nelle demo recuperati, così il server non si ferma; parametri limitati lato server (es. pizze ≤ 40, chiavi ≤ 300.000). | [D] | ✅ |
| TEC-07 | Configurazione tramite flag o variabili d'ambiente: `-host`/`DEMO_HOST` (default `127.0.0.1`), `-port`/`DEMO_PORT` (8080), `-no-browser`/`DEMO_NO_BROWSER=1`, `-healthcheck`. | [D] | ✅ |
| TEC-08 | Shutdown graceful del server su SIGINT/SIGTERM: cancellazione degli stream aperti e `http.Server.Shutdown` con deadline di 5 s. | [D] coerenza con la pagina 05; `docker stop` in ~250 ms con exit 0 | ✅ |
| TEC-09 | In locale, all'avvio si apre il browser; nel container mai. | [D] | ✅ |
| TEC-10 | Log del server in ASCII (nessun carattere di disegno). | [D] la console Windows con codepage legacy li mostrerebbe male | ✅ |

## 5. Esecuzione e distribuzione

### 5.1 Avvio locale

| ID | Requisito | Stato |
|---|---|---|
| RUN-01 | Avvio locale con `go run .` (oppure un binario compilato con `go build -trimpath -ldflags "-s -w"`). Gli script `.bat` e l'exe precompilato sono stati rimossi: l'avvio di riferimento è Docker Compose. | ✅ |
| RUN-02 | Server in ascolto su `127.0.0.1`, quindi non esposto sulla rete della conferenza. | ✅ |

### 5.2 Container (produzione) · [R] «si avvii su un container anche con docker compose»

| ID | Requisito | Stato |
|---|---|---|
| DCK-01 | `Dockerfile` multi-stage: `golang:1.24-alpine` esegue `go vet` e `go test` (se falliscono la build si ferma) e compila un binario statico. | ✅ (build completa con Docker Desktop, 24 ottobre) |
| DCK-02 | Runtime `scratch`: solo il binario (~10 MB), utente non root `65532`, nessuna shell. | ✅ |
| DCK-03 | `HEALTHCHECK` eseguito dal binario stesso (`-healthcheck` → `/healthz`), perché nell'immagine non c'è curl. | ✅ (healthy) |
| DCK-04 | `docker-compose.yml`: porta pubblicata su `127.0.0.1` (configurabile con `DEMO_BIND` e `DEMO_PORT`), `read_only`, `cap_drop: ALL`, `no-new-privileges`, `mem_limit: 1g`, `restart: unless-stopped`, `stop_grace_period: 10s`. | ✅ |
| DCK-05 | Tutte le demo funzionano nel container, compreso il processo figlio della pagina 03 (filesystem in sola lettura, utente non root). | ✅ |
| DCK-06 | Avvio con `docker compose up --build -d`, poi browser su <http://localhost:8080>. | ✅ (Docker Desktop su Windows) |
| DCK-08 | `docker-compose.llm.yml`, incluso da entrambi i compose: servizio `llm-model` che scarica il modello (1,1 GB) una volta sola in un volume e ne verifica lo sha256, servizio `llm` (llama.cpp server, `mem_limit: 3g`, healthcheck su `/health`). Il container della demo ha un `tmpfs` su `/tmp` per il file di log. Immagine di llama.cpp fissata per digest. Verificato che il container `llm` non raggiunge internet. | ✅ |
| DCK-07 | `.dockerignore` esclude exe, `tmp/`, log, `.git` e le cartelle degli IDE. | ✅ |

### 5.3 Sviluppo con hot reload · [R] «auto rebuild se si tocca codice, `github.com/air-verse/air@latest`»

| ID | Requisito | Stato |
|---|---|---|
| DEV-01 | `Dockerfile.dev` con `go install github.com/air-verse/air@latest` su **`golang:1.27-alpine`**. | ⚠️ immagine non scaricabile qui; provata con un'immagine equivalente |
| DEV-02 | Motivazione di Go 1.27: `air@latest` (v1.67.4) richiede **Go ≥ 1.26**. È la stessa immagine usata in ICM. | [D] ✅ verificato sul go.mod di Air |
| DEV-03 | `docker-compose.dev.yml`: sorgente montato in `/app`, volumi di cache per `go build` e i moduli, `init: true`, `mem_limit: 1g`, porta su `127.0.0.1`. | ✅ |
| DEV-04 | `.air.toml`: rebuild per `go`, `html`, `css`, `js` (il frontend è incluso con `embed`); esclusi `tmp`, `.git`, gli IDE e i `_test.go`. | ✅ |
| DEV-05 | `poll = true` (intervallo di 500 ms). | [D] Docker Desktop su Windows non propaga gli eventi del filesystem nei volumi · ⚠️ da provare su Windows |
| DEV-06 | `send_interrupt = true`, `kill_delay = 500ms`: shutdown graceful a ogni riavvio senza rallentare i rebuild. | ✅ |
| DEV-07 | `stop_on_error = false`: se la build fallisce resta su la versione precedente (verificato: HTTP 200 durante l'errore). | ✅ |
| DEV-08 | `stop_signal: SIGINT` nel compose di sviluppo. | [D] con SIGTERM Air lascia orfano il processo della demo (verificato) · ✅ |
| DEV-09 | Binario di sviluppo in `tmp/`, ignorato da git e da Docker e cancellato all'uscita (`clean_on_exit`). | ✅ |
| DEV-10 | Avvio con `docker compose -f docker-compose.dev.yml up --build`. | ⚠️ da provare su Windows |
| DEV-11 | Un solo meccanismo di sviluppo: Air. La bozza basata su Docker Compose Watch è stata rimossa. | [D] ✅ |

## 6. Qualità e collaudo

| ID | Requisito | Stato |
|---|---|---|
| QA-01 | `go vet ./...` pulito, `gofmt` applicato. | ✅ |
| QA-02 | Test Go (`demos/demos_test.go`): semantica dei lock, sanificazione, fingerprint, SafeGo; eseguiti anche con `-race`. | ✅ |
| QA-03 | Collaudo end-to-end con un browser automatico: tutte le pagine e tutti gli scenari, a 1600×1000 e 1366×768, **zero errori JavaScript**. | ✅ |
| QA-04 | Stress test: ogni demo avviata e interrotta a metà più volte e run in parallelo, con server compilato con `-race`: **zero panic, zero data race**. | ✅ |
| QA-05 | Collaudo end-to-end ripetuto contro il container di produzione. | ✅ |
| QA-06 | Collaudo dell'hot reload: modifica di un `.go`, di un file `web/`, errore di compilazione e correzione, ricarica automatica del browser. | ✅ |

## 7. Da verificare sul PC prima del 28 ottobre

1. `go run .` → si apre il browser e funzionano tutte le pagine (in particolare la 03 su Windows: exit code 2).
2. Con Docker Desktop avviato: `docker compose up --build -d` → il container risulta *healthy*. ✅ fatto il 24 ottobre.
3. `docker compose -f docker-compose.dev.yml up --build` → modificare un testo in `web/index.html` e controllare che il browser si ricarichi da solo (verifica del polling sui volumi Windows).
4. Provare tutto **senza rete**: la demo deve funzionare offline. Fanno eccezione le build Docker e il primo avvio, che scaricano immagini e modello AI (~1,1 GB): farli prima dell'evento.
5. Pagina 06 con il modello: tempo di risposta sul portatile dell'evento (dipende dalla CPU) e numero di bug trovati.

## 8. Fuori ambito (allo stato attuale)

- Nessuna chiamata verso servizi esterni: il modello AI gira in locale, in un container senza internet.
- Nessun Redis, Postgres o Kubernetes reali: dove servono sono simulati in memoria.
- Nessun cgo e nessuna VM Lua reale (memoria C simulata).
- Nessuna autenticazione: la demo è pensata per `localhost`. Per esporla in rete va attivata esplicitamente con `DEMO_BIND=0.0.0.0`.
- La cartella non è un repository git (nessuna `.git`): il `.gitignore` è pronto per quando lo diventerà.

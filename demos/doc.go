// Package demos contiene le simulazioni mostrate dal sito, una per file:
//
//   - pipeline.go: demo 01, pipeline a channel, fan-out e backpressure (la pizzeria)
//   - memory.go: demo 02, heap Go contro RSS: memoria C, GOMEMLIMIT, OOMKilled
//   - recover.go: demo 03, recover() non attraversa le goroutine (processo figlio vero)
//   - locks.go: demo 04, lock distribuiti con TTL, doppie esecuzioni, KEYS contro SCAN
//   - shutdown.go: demo 05, SIGTERM brutale contro graceful shutdown con deadline
//   - aitriage.go: demo 06, triage dei log con un modello locale e i suoi guardrail
//
// Ogni demo è una live.DemoFunc: riceve una *live.Run, legge i parametri con
// r.Int, r.Bool e r.Str, racconta quello che fa con r.Log (pannello log),
// r.State (dati per l'animazione) e r.Say (narratore), e si ferma appena
// r.Ctx viene cancellato. Le attese passano da r.Sleep o da un select su
// r.Ctx.Done(), così «Ferma» e «Ricomincia» interrompono subito anche una demo bloccata.
//
// Dove qualcosa è simulato (Redis, la memoria C delle VM Lua, il modello AI)
// lo dice il commento in testa al file. Il resto è Go vero: goroutine,
// channel, context, processi e misure del runtime.
package demos

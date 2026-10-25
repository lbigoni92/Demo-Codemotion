package demos

import (
	"math/rand/v2"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"democodemotion/live"
)

// Heap Go vs RSS: il GC di Go non vede tutta la memoria del processo.
//
// Nel servizio reale ogni formula apre una VM Lua via cgo (lua.NewState()):
// la memoria della VM è allocata in C, fuori dall'heap Go. runtime.MemStats la
// ignora, GOMEMLIMIT non la governa, ma Kubernetes la conta eccome (RSS) e
// quando si supera il limite del pod arriva l'OOMKill.
//
// In questa demo l'heap Go è VERO (allocazioni reali, misurate con
// runtime.ReadMemStats); la memoria nativa della VM è SIMULATA con un contatore,
// per non dover compilare con cgo.

// nativeBytes è la memoria C simulata, in byte: quella che in produzione
// allocherebbe lua.NewState() con malloc e che runtime.MemStats non vede.
var nativeBytes atomic.Int64

// luaVMs conta le VM aperte in questo momento (i quadratini nel browser).
var luaVMs atomic.Int64

// memoryRun serializza le run: contatori e debug.SetMemoryLimit sono globali
// al processo, quindi un rilancio aspetta che la run precedente abbia chiuso
// le sue VM e ripristinato il limite.
var memoryRun sync.Mutex

// fakeLuaVM rappresenta lo stato C di una VM Lua. Non alloca nulla davvero:
// sposta solo il contatore nativeBytes, così il grafico mostra la memoria C
// senza dover compilare con cgo.
type fakeLuaVM struct{ size int64 }

// newFakeLuaVM simula lua.NewState(): mb MiB di memoria fuori dall'heap Go.
func newFakeLuaVM(mb int) *fakeLuaVM {
	vm := &fakeLuaVM{size: int64(mb) << 20}
	nativeBytes.Add(vm.size)
	return vm
}

// Close simula L.Close(): restituisce la memoria C. Se nessuno la chiama, quella
// memoria resta occupata e il GC non può farci nulla.
func (vm *fakeLuaVM) Close() { nativeBytes.Add(-vm.size) }

// Memory è la demo 02: esegue formule in parallelo, ognuna con una VM Lua
// simulata e dati veri nell'heap Go, e confronta ogni 150 ms quello che vede
// Go (runtime.MemStats) con quello che vedrebbe Kubernetes (RSS stimato).
func Memory(r *live.Run) any {
	formulas := r.Int("formulas", 40, 5, 200)
	conc := r.Int("conc", 6, 1, 16)
	vmMB := r.Int("vmmb", 24, 4, 64)
	leak := r.Bool("leak")
	limitMB := r.Int("gomemlimit", 0, 0, 2048)
	podMB := r.Int("podmb", 512, 128, 4096)

	memoryRun.Lock()
	defer memoryRun.Unlock()
	nativeBytes.Store(0)
	luaVMs.Store(0)
	runtime.GC()

	// GOMEMLIMIT impostato davvero, per tutto il processo: va ripristinato a fine run.
	prevLimit := debug.SetMemoryLimit(-1) // legge il valore attuale senza cambiarlo
	if limitMB > 0 {
		debug.SetMemoryLimit(int64(limitMB) << 20)
		r.Log("info", "runtime", "debug.SetMemoryLimit(%d MiB) → equivalente a GOMEMLIMIT=%dMiB: il GC lavora di più quando l'heap Go si avvicina al limite", limitMB, limitMB)
	} else {
		r.Log("info", "runtime", "GOMEMLIMIT non impostato: il GC segue solo GOGC (default 100)")
	}
	defer debug.SetMemoryLimit(prevLimit)

	r.Say("info", "Eseguo %d formule, %d in parallelo. Ogni formula: VM Lua (~%d MiB nativi) + dati caricati nell'heap Go", formulas, conc, vmMB)
	if leak {
		r.Log("warn", "codice", "modalità BUG: manca `defer L.Close()` → la memoria nativa delle VM non viene mai liberata")
	}
	r.State("limits", map[string]any{"podMB": podMB, "gomemlimitMB": limitMB})

	var oom atomic.Bool
	stopSampler := make(chan struct{})
	samplerDone := make(chan struct{})
	var peakRSS atomic.Int64
	// sample misura la memoria: heap Go reale da ReadMemStats (che ferma il mondo
	// per un istante), memoria C dal contatore, RSS stimato come somma più ~18 MiB
	// di binario e runtime. Su Linux il valore vero sarebbe VmRSS in /proc/self/status.
	sample := func() (goMB, natMB, rssMB float64, numGC uint32) {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		goB := int64(m.HeapInuse + m.StackInuse)
		nat := nativeBytes.Load()
		rss := goB + nat + 18<<20 // + ~18 MiB di binario/runtime
		if rss > peakRSS.Load() {
			peakRSS.Store(rss)
		}
		return float64(m.HeapAlloc) / (1 << 20), float64(nat) / (1 << 20), float64(rss) / (1 << 20), m.NumGC
	}
	go func() {
		defer close(samplerDone)
		t := time.NewTicker(150 * time.Millisecond)
		defer t.Stop()
		lastGC := uint32(0)
		warned := false
		for {
			select {
			case <-stopSampler:
				return
			case <-r.Ctx.Done():
				return
			case cmd := <-r.Control:
				if cmd == "gc" {
					r.Log("info", "runtime", "runtime.GC() forzato dall'utente")
					runtime.GC()
					debug.FreeOSMemory()
					_, nat, _, _ := sample()
					r.Log("warn", "runtime", "GC completato: l'heap Go scende, ma i %.0f MiB nativi restano lì. Il GC non sa che esistono.", nat)
				}
			case <-t.C:
				goMB, natMB, rssMB, numGC := sample()
				if numGC != lastGC && lastGC != 0 {
					r.Log("debug", "GC", "ciclo GC #%d completato (heap Go ora %.1f MiB)", numGC, goMB)
				}
				lastGC = numGC
				r.State("mem", map[string]any{
					"go": goMB, "native": natMB, "rss": rssMB, "numGC": numGC,
					"vms": luaVMs.Load(), "goroutines": runtime.NumGoroutine(),
				})
				if !warned && rssMB > float64(podMB)*0.8 {
					warned = true
					r.Say("warn", "RSS a %.0f MiB: oltre l'80%% del limite del pod (%d MiB). runtime.MemStats vede solo %.0f MiB!", rssMB, podMB, goMB)
				}
				if rssMB > float64(podMB) && !oom.Load() {
					oom.Store(true)
					r.Say("error", "OOMKilled: RSS %.0f MiB > limite pod %d MiB. Kubernetes uccide il container.", rssMB, podMB)
					r.State("oom", map[string]any{"rss": rssMB})
				}
			}
		}
	}()

	// Esecuzione delle formule con concorrenza limitata (semaforo). Il ciclo si
	// ferma anche all'OOM: nella realtà il processo sarebbe già stato ucciso.
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var keep [][]byte // con il leak teniamo anche un po' di heap Go (es. cache che cresce)
	var keepMu sync.Mutex
	logDefers := true // solo la prima formula racconta le sue defer, per non inondare il log

	for i := 1; i <= formulas && !oom.Load() && r.Ctx.Err() == nil; i++ {
		select {
		case sem <- struct{}{}:
		case <-r.Ctx.Done():
		}
		if r.Ctx.Err() != nil {
			break
		}
		wg.Add(1)
		verbose := logDefers
		logDefers = false
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			runFormula(r, i, vmMB, leak, verbose, &keep, &keepMu)
		}(i)
	}
	wg.Wait()
	r.Sleep(300 * time.Millisecond)
	goMB, natMB, rssMB, numGC := sample()
	close(stopSampler)
	<-samplerDone

	// la «cache» del leak va rilasciata a fine run, altrimenti il prossimo
	// rilancio partirebbe con l'heap già pieno
	keepMu.Lock()
	keep = nil
	keepMu.Unlock()
	if r.Ctx.Err() != nil {
		return nil
	}
	if !oom.Load() {
		if leak {
			r.Say("warn", "Finito, ma restano %.0f MiB nativi mai liberati: dopo qualche run il pod va in OOM", natMB)
		} else {
			r.Say("ok", "Finito senza OOM. Heap Go %.0f MiB, nativo %.0f MiB, RSS stimato %.0f MiB, %d cicli di GC", goMB, natMB, rssMB, numGC)
		}
	}
	nativeBytes.Store(0)
	return map[string]any{"peakRSS_MiB": peakRSS.Load() >> 20, "numGC": numGC, "oom": oom.Load()}
}

// runFormula riproduce l'ordine delle defer del codice reale: dichiarate in
// quest'ordine, all'uscita vengono eseguite al contrario (LIFO).
func runFormula(r *live.Run, i, vmMB int, leak, verbose bool, keep *[][]byte, keepMu *sync.Mutex) {
	src := "formula"
	defer func() { // 1ª dichiarata → eseguita per ULTIMA
		if verbose {
			r.Log("info", src, "defer #1 (dichiarata per prima) → registro telemetria. Eseguita per ULTIMA (LIFO)")
		}
	}()
	defer func() { // 2ª dichiarata
		luaVMs.Add(-1)
		if verbose {
			r.Log("info", src, "defer #2 → DecLuaVM()")
		}
	}()
	luaVMs.Add(1)

	L := newFakeLuaVM(vmMB + rand.IntN(8)) // lua.NewState()
	if !leak {
		defer func() { // 3ª dichiarata → eseguita per PRIMA
			L.Close()
			if verbose {
				r.Log("info", src, "defer #3 (dichiarata per ultima) → L.Close(). Eseguita per PRIMA (LIFO)")
			}
		}()
	}
	if verbose {
		r.Log("info", src, "F%d: lua.NewState() → %d MiB allocati in C, invisibili a runtime.MemStats", i, L.size>>20)
	}

	// I dati caricati dal DB (transazioni, componenti...) vivono nell'heap Go: allocazione vera.
	data := make([]byte, (4+rand.IntN(8))<<20)
	for j := 0; j < len(data); j += 4096 {
		data[j] = byte(j)
	}
	r.Log("debug", src, "F%d: caricate transazioni → %d MiB nell'heap Go", i, len(data)>>20)
	r.State("formula", map[string]any{"i": i, "status": "run"})
	if !r.Sleep(time.Duration(300+rand.IntN(500)) * time.Millisecond) {
		return
	}
	if leak {
		keepMu.Lock()
		*keep = append(*keep, append([]byte(nil), data[:256<<10]...)) // una piccola cache che cresce
		keepMu.Unlock()
	}
	r.State("formula", map[string]any{"i": i, "status": "ok"})
	_ = data
}

package demos

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"democodemotion/live"
)

// TestWatchdogStopsJobWhenLockIsLost: un altro worker si prende il lock mentre
// il job è in corso. Il primo Extend del watchdog fallisce e il job deve
// fermarsi senza segnarsi completato, invece di continuare fino alla fine.
func TestWatchdogStopsJobWhenLockIsLost(t *testing.T) {
	var mu sync.Mutex
	completed := map[int]bool{}
	runs, active := map[int]int{}, map[int]int{}
	doubles := 0

	hub := live.NewHub()
	hub.Register("lost", func(r *live.Run) any {
		rd := newFakeRedis()
		ttl := 300 * time.Millisecond
		rd.SetNX("job_lock:1", "worker-1", ttl)
		// il lock passa a worker-2: il prossimo Extend di worker-1 fallisce
		rd.mu.Lock()
		rd.data["job_lock:1"] = fakeEntry{val: "worker-2", exp: time.Now().Add(time.Minute)}
		rd.mu.Unlock()
		runJob(r, rd, 1, "worker-1", 1, ttl, 3*time.Second, true, &mu, completed, runs, active, &doubles)
		return nil
	})
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/run/{demo}", hub.HandleRun)

	start := time.Now()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/run/lost", nil)) // ritorna a demo finita
	if el := time.Since(start); el > 1500*time.Millisecond {
		t.Fatalf("il job doveva fermarsi al primo Extend fallito (~100 ms), è durato %v", el)
	}
	if completed[1] {
		t.Fatal("un job fermato per lock perso non deve risultare completato")
	}
	if active[1] != 0 {
		t.Fatalf("esecuzioni attive dopo l'uscita: %d, attese 0", active[1])
	}
	if !strings.Contains(rec.Body.String(), `"lost":true`) {
		t.Fatal("manca l'evento lost per il browser")
	}
}

// TestFakeRedisLockSemantics verifica che il Redis simulato si comporti come
// quello vero sui lock: un solo vincitore, scadenza, rilascio e rinnovo solo
// da parte del proprietario. Se queste regole saltano, la demo 04 mente.
func TestFakeRedisLockSemantics(t *testing.T) {
	rd := newFakeRedis()
	if !rd.SetNX("job_lock:1", "w1", 50*time.Millisecond) {
		t.Fatal("il primo SET NX deve riuscire")
	}
	if rd.SetNX("job_lock:1", "w2", 50*time.Millisecond) {
		t.Fatal("il secondo SET NX deve fallire finché il lock è valido")
	}
	if rd.Unlock("job_lock:1", "w2") {
		t.Fatal("un altro worker non deve poter cancellare il lock")
	}
	time.Sleep(70 * time.Millisecond)
	if !rd.SetNX("job_lock:1", "w2", 50*time.Millisecond) {
		t.Fatal("dopo il TTL il lock deve essere libero")
	}
	if rd.Unlock("job_lock:1", "w1") {
		t.Fatal("w1 non possiede più il lock: Unlock deve fallire")
	}
	if !rd.Extend("job_lock:1", "w2", time.Second) || !rd.Unlock("job_lock:1", "w2") {
		t.Fatal("il proprietario deve poter estendere e rilasciare")
	}
}

// TestSanitizeMasksSecrets verifica che email, password e token Bearer non
// sopravvivano alla sanificazione.
func TestSanitizeMasksSecrets(t *testing.T) {
	in := "login mario@cliente.it password=Sup3r! Bearer eyJ.abc.def"
	out, hits := sanitize(in)
	for _, leak := range []string{"mario@cliente.it", "Sup3r!", "eyJ.abc.def"} {
		if strings.Contains(out, leak) {
			t.Fatalf("segreto non mascherato %q in %q", leak, out)
		}
	}
	if len(hits) != 3 {
		t.Fatalf("attesi 3 tipi mascherati, trovati %v", hits)
	}
}

// TestFingerprintGroupsSimilarLines verifica che righe diverse solo per UUID e
// numeri finiscano nello stesso gruppo, e righe diverse in gruppi diversi.
func TestFingerprintGroupsSimilarLines(t *testing.T) {
	a := fingerprint("export 6d46b5f4-60a7-6cc8-d8f9-de4cbe3de5cf failed after 864ms")
	b := fingerprint("export 011fabfa-989e-5508-12d1-cc9a145f21e2 failed after 1004ms")
	c := fingerprint("auth to redis failed")
	if a != b {
		t.Fatal("righe uguali a meno di UUID e numeri devono avere la stessa impronta")
	}
	if a == c {
		t.Fatal("righe diverse devono avere impronte diverse")
	}
}

// TestSafeGoRecovers: se SafeGo non recuperasse il panic, il processo di test
// morirebbe con exit code 2 invece di fallire con un messaggio.
func TestSafeGoRecovers(t *testing.T) {
	done := make(chan struct{})
	SafeGo("test", func() {
		defer close(done)
		var s []int
		_ = s[1]
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("la goroutine non è terminata")
	}
}

// TestGenerateLog: il file ha il numero di righe richiesto, le uniche righe
// malformate sono quelle del bug «malformed» (e il parser le riconosce tutte),
// gli altri bug ci sono tutti e restano riconoscibili dopo la sanificazione,
// e nessun segreto sopravvive alla sanificazione.
func TestGenerateLog(t *testing.T) {
	var buf strings.Builder
	start := time.Date(2026, 10, 28, 14, 0, 0, 0, time.UTC)
	res, err := generateLog(&buf, 10000, 42, true, start)
	if err != nil {
		t.Fatal(err)
	}
	if res.Lines != 10000 {
		t.Fatalf("righe: %d, attese 10000", res.Lines)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 10000 {
		t.Fatalf("righe nel testo: %d", len(lines))
	}
	seen := map[string]int{}
	var prev time.Time
	for i, l := range lines {
		pl, why := parseLineWhy(l)
		if why != "" {
			if why != "righe fuse" && why != "troncata" {
				t.Fatalf("riga %d malformata in modo inatteso (%s): %q", i+1, why, l)
			}
			seen["malformed"]++
			continue
		}
		if pl.ts.Before(prev) {
			t.Fatalf("riga %d fuori ordine", i+1)
		}
		prev = pl.ts
		clean, _ := sanitize(pl.msg)
		for _, leak := range []string{"@cliente.it", "Sup3r", "eyJhbGci"} {
			if strings.Contains(clean, leak) {
				t.Fatalf("segreto %q sopravvissuto alla sanificazione: %q", leak, clean)
			}
		}
		if b := bugOf(clean); b != "" {
			seen[b]++
		}
	}
	for _, b := range bugCatalog {
		if seen[b.id] != b.count {
			t.Errorf("bug %s: %d righe riconosciute, attese %d", b.id, seen[b.id], b.count)
		}
	}
}

// TestNormGroupID: «g7», «G07» e «g07» indicano lo stesso gruppo; un id che
// non ha quella forma resta com'è e la validazione lo scarterà.
func TestNormGroupID(t *testing.T) {
	for in, want := range map[string]string{"g7": "g07", "G07": "g07", " g12 ": "g12", "g007": "g07", "gruppo 3": "gruppo 3", "g123": "g123"} {
		if got := normGroupID(in); got != want {
			t.Errorf("normGroupID(%q) = %q, atteso %q", in, got, want)
		}
	}
}

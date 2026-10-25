package demos

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Client minimale per il modello locale della demo 06: llama.cpp server con la
// sua API compatibile OpenAI (/v1/chat/completions). Solo standard library.
//
// Configurazione da variabili d'ambiente, impostate dal docker-compose:
//
//	DEMO_LLM_URL    es. http://llm:8080 (vuota = nessun modello: si usano le regole)
//	DEMO_LLM_MODEL  nome del modello, solo informativo per llama.cpp

type llmConfig struct {
	URL   string
	Model string
}

func llmFromEnv() llmConfig {
	return llmConfig{
		URL:   strings.TrimRight(os.Getenv("DEMO_LLM_URL"), "/"),
		Model: envOrDefault("DEMO_LLM_MODEL", "local"),
	}
}

func envOrDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// health controlla che il server del modello risponda e abbia finito di
// caricare il modello (llama.cpp risponde 503 a /health finché carica).
func (c llmConfig) health(ctx context.Context) error {
	if c.URL == "" {
		return errors.New("DEMO_LLM_URL non impostata")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.URL+"/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("/health risponde %d (modello ancora in caricamento?)", resp.StatusCode)
	}
	return nil
}

// LLMStatus è l'handler di GET /api/llm: la pagina 06 lo chiama all'apertura
// per mostrare se il modello locale è pronto.
func LLMStatus(w http.ResponseWriter, req *http.Request) {
	c := llmFromEnv()
	st := map[string]any{"url": c.URL, "model": c.Model, "ok": false}
	if err := c.health(req.Context()); err != nil {
		st["error"] = err.Error()
	} else {
		st["ok"] = true
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(st)
}

// llmClient non riusa le connessioni: con richieste lunghe e intervallate,
// il keep-alive porta a scrivere su connessioni che il server ha già chiuso
// («server closed idle connection», EOF). Nessun timeout sul client: la
// durata la decide il context, uno stream può durare minuti.
var llmClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

type chatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// llmStats sono i tempi misurati da llama.cpp e restituiti nell'ultimo chunk.
type llmStats struct {
	PromptTokens    int     `json:"prompt_n"`
	PromptMS        float64 `json:"prompt_ms"`
	PredictedTokens int     `json:"predicted_n"`
	PredictedMS     float64 `json:"predicted_ms"`
}

// streamChat manda i messaggi al modello e legge la risposta in streaming:
// onDelta riceve i pezzi di testo man mano che vengono generati.
//
// schema vincola l'output con una grammatica (response_format json_schema):
// llama.cpp può generare solo token che rispettano lo schema, quindi il JSON
// è sempre ben formato. Il «thinking» di Qwen3 è disattivato: per un triage
// servono risposte brevi e veloci.
//
// Cancellare ctx (pagina cambiata, Ricomincia) chiude la connessione e
// llama.cpp interrompe la generazione.
func (c llmConfig) streamChat(ctx context.Context, msgs []chatMsg, schema any, maxTokens int, onDelta func(string)) (string, llmStats, error) {
	body, _ := json.Marshal(map[string]any{
		"model":       c.Model,
		"messages":    msgs,
		"stream":      true,
		"temperature": 0.2,
		"max_tokens":  maxTokens,
		"response_format": map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"name": "triage", "schema": schema},
		},
		"chat_template_kwargs": map[string]any{"enable_thinking": false},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", llmStats{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Un solo nuovo tentativo sugli errori di rete: llama.cpp chiude le
	// connessioni inattive e una richiesta può partire su una appena chiusa.
	resp, err := llmClient.Do(req)
	if err != nil && ctx.Err() == nil {
		req, _ = http.NewRequestWithContext(ctx, http.MethodPost, c.URL+"/v1/chat/completions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err = llmClient.Do(req)
	}
	if err != nil {
		return "", llmStats{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		return "", llmStats{}, fmt.Errorf("il modello risponde %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	// Lo stream è SSE: righe "data: {json}" e alla fine "data: [DONE]".
	var (
		full  strings.Builder
		stats llmStats
	)
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Timings *llmStats `json:"timings"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue // un chunk illeggibile non deve far perdere tutta la risposta
		}
		if chunk.Timings != nil {
			stats = *chunk.Timings
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				full.WriteString(ch.Delta.Content)
				onDelta(ch.Delta.Content)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return full.String(), stats, err
	}
	return full.String(), stats, ctx.Err()
}

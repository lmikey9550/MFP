package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type modelInfo struct {
	ID string `json:"id"`
}

type modelList struct {
	Data []modelInfo `json:"data"`
}

func main() {
	port := getenv("MOCK_PORT", "4000")
	name := getenv("MOCK_NAME", "mock-primary")
	models := strings.Split(getenv("MOCK_MODELS", "provider-model-a,provider-model-b"), ",")
	failKeyword := getenv("MOCK_FAIL_KEYWORD", "[failover]")
	failModel := getenv("MOCK_FAIL_MODEL", "provider-model-a")

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		out := modelList{Data: make([]modelInfo, 0, len(models))}
		for _, model := range models {
			out.Data = append(out.Data, modelInfo{ID: strings.TrimSpace(model)})
		}
		writeJSON(w, http.StatusOK, out)
	})

	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		handleCompletion(w, r, name, failKeyword, failModel)
	})
	mux.HandleFunc("/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		handleResponseAPI(w, r, name, failKeyword, failModel)
	})
	mux.HandleFunc("/v1/completions", func(w http.ResponseWriter, r *http.Request) {
		handleTextCompletion(w, r, name, failKeyword, failModel)
	})
	mux.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		handleEmbeddings(w, r, name)
	})
	mux.HandleFunc("/v1/rerank", func(w http.ResponseWriter, r *http.Request) {
		handleRerank(w, r, name)
	})
	mux.HandleFunc("/v1/reranks", func(w http.ResponseWriter, r *http.Request) {
		handleRerank(w, r, name)
	})
	mux.HandleFunc("/v1/images/generations", func(w http.ResponseWriter, r *http.Request) {
		handleImageGeneration(w, r, name)
	})
	mux.HandleFunc("/v1/images/edits", func(w http.ResponseWriter, r *http.Request) {
		handleImageEdit(w, r, name)
	})
	mux.HandleFunc("/v1/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		handleTranscription(w, r, name)
	})
	mux.HandleFunc("/v1/audio/speech", func(w http.ResponseWriter, r *http.Request) {
		handleSpeech(w, r, name)
	})
	mux.HandleFunc("/v1/moderations", func(w http.ResponseWriter, r *http.Request) {
		handleModeration(w, r, name)
	})
	mux.HandleFunc("/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		handleAnthropicMessages(w, r, name, failKeyword, failModel)
	})
	mux.HandleFunc("/v1/messages/count_tokens", func(w http.ResponseWriter, r *http.Request) {
		handleAnthropicCountTokens(w, r)
	})

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("%s listening on :%s", name, port)
	log.Fatal(server.ListenAndServe())
}

func handleCompletion(w http.ResponseWriter, r *http.Request, providerName, failKeyword, failModel string) {
	var payload struct {
		Model    string `json:"model"`
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
		Stream bool `json:"stream"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if shouldFail(payload.Model, payload.Messages, failKeyword, failModel) {
		writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", providerName+" forced failover")
		return
	}
	if payload.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "stream_unsupported", "stream unsupported")
			return
		}
		fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-mock\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"delta\":{\"content\":\"hello from %s\"},\"index\":0}]}\n\n", providerName)
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":       "chatcmpl-mock",
		"object":   "chat.completion",
		"model":    payload.Model,
		"provider": providerName,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": fmt.Sprintf("hello from %s using %s", providerName, payload.Model),
				},
				"finish_reason": "stop",
			},
		},
	})
}

func handleResponseAPI(w http.ResponseWriter, r *http.Request, providerName, failKeyword, failModel string) {
	var payload struct {
		Model string `json:"model"`
		Input any    `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.Contains(fmt.Sprint(payload.Input), failKeyword) && payload.Model == failModel {
		writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", providerName+" forced failover")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":          "resp-mock",
		"object":      "response",
		"model":       payload.Model,
		"output_text": fmt.Sprintf("hello from %s using %s", providerName, payload.Model),
	})
}

func handleTextCompletion(w http.ResponseWriter, r *http.Request, providerName, failKeyword, failModel string) {
	var payload struct {
		Model  string `json:"model"`
		Prompt any    `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if strings.Contains(fmt.Sprint(payload.Prompt), failKeyword) && payload.Model == failModel {
		writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", providerName+" forced failover")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":       "cmpl-mock",
		"object":   "text_completion",
		"model":    payload.Model,
		"provider": providerName,
		"choices":  []map[string]any{{"index": 0, "text": fmt.Sprintf("hello from %s using %s", providerName, payload.Model), "finish_reason": "stop"}},
	})
}

func handleEmbeddings(w http.ResponseWriter, r *http.Request, providerName string) {
	var payload struct {
		Model string `json:"model"`
		Input any    `json:"input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"object":   "list",
		"model":    payload.Model,
		"data":     []map[string]any{{"object": "embedding", "index": 0, "embedding": []float64{0.1, 0.2, 0.3}}},
		"usage":    map[string]any{"prompt_tokens": len(fmt.Sprint(payload.Input)), "total_tokens": len(fmt.Sprint(payload.Input))},
		"provider": providerName,
	})
}

func handleRerank(w http.ResponseWriter, r *http.Request, providerName string) {
	var payload struct {
		Model     string   `json:"model"`
		Query     string   `json:"query"`
		Documents []string `json:"documents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"model":    payload.Model,
		"provider": providerName,
		"results":  []map[string]any{{"index": 0, "relevance_score": 1.0}},
	})
}

func handleImageGeneration(w http.ResponseWriter, r *http.Request, providerName string) {
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"created": time.Now().Unix(), "model": payload.Model, "provider": providerName, "data": []map[string]any{{"url": "https://example.invalid/mock.png"}}})
}

func handleImageEdit(w http.ResponseWriter, r *http.Request, providerName string) {
	handleImageGeneration(w, r, providerName)
}

func handleTranscription(w http.ResponseWriter, r *http.Request, providerName string) {
	var payload struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)
	writeJSON(w, http.StatusOK, map[string]any{"text": "mock transcription from " + providerName, "model": payload.Model})
}

func handleSpeech(w http.ResponseWriter, r *http.Request, providerName string) {
	var payload struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)
	w.Header().Set("Content-Type", "audio/mpeg")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("mock speech from " + providerName + " using " + payload.Model))
}

func handleModeration(w http.ResponseWriter, r *http.Request, providerName string) {
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": "modr-mock", "model": payload.Model, "provider": providerName, "results": []map[string]any{{"flagged": false}}})
}

func handleAnthropicMessages(w http.ResponseWriter, r *http.Request, providerName, failKeyword, failModel string) {
	var payload struct {
		Model    string `json:"model"`
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if shouldFail(payload.Model, payload.Messages, failKeyword, failModel) {
		writeError(w, http.StatusTooManyRequests, "rate_limit_exceeded", providerName+" forced failover")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "msg_mock",
		"type":    "message",
		"role":    "assistant",
		"model":   payload.Model,
		"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("hello from %s using %s", providerName, payload.Model)}},
	})
}

func handleAnthropicCountTokens(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Model string `json:"model"`
	}
	_ = json.NewDecoder(r.Body).Decode(&payload)
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": 10, "model": payload.Model})
}

func shouldFail(model string, messages []struct {
	Content any "json:\"content\""
}, failKeyword, failModel string) bool {
	if model != failModel {
		return false
	}
	for _, message := range messages {
		if strings.Contains(fmt.Sprint(message.Content), failKeyword) {
			return true
		}
	}
	return false
}

func writeError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func getenv(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

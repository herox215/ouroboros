// Package openaitest provides a fake OpenAI API for tests.
package openaitest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"ouroboros/internal/openai"
)

// Key is the only API key the fake server accepts.
const Key = "sk-good"

// Models are the models the fake server offers.
var Models = []string{"gpt-4o", "gpt-4o-mini", "o3-mini", "tts-1", "whisper-1"}

// NewServer starts a fake API and returns its base URL. Chat replies have the
// form "[model] turns=N last=TEXT", where N counts the non-system messages.
func NewServer(t testing.TB) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /models", func(w http.ResponseWriter, r *http.Request) {
		data := make([]map[string]string, len(Models))
		for i, m := range Models {
			data[i] = map[string]string{"id": m}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	mux.HandleFunc("POST /chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model    string           `json:"model"`
			Messages []openai.Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		reply := fmt.Sprintf("[%s] turns=%d last=%s", in.Model, len(in.Messages)-1, in.Messages[len(in.Messages)-1].Content)
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": openai.Message{Role: "assistant", Content: reply}}}})
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+Key {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

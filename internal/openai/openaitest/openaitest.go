// Package openaitest provides a fake OpenAI-compatible API for tests.
package openaitest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/openai"
)

// Key is the API key NewServer accepts.
const Key = "sk-good"

// Models are the models the fake server offers.
var Models = []string{"gpt-4o", "gpt-4o-mini", "o3-mini", "tts-1", "whisper-1"}

// NewServer starts a fake API that requires Key and returns its base URL.
// Chat replies have the form "[model] turns=N last=TEXT", where N counts the
// non-system messages. A message "@PEER TEXT" is answered with a call to
// ask_peer, "@" alone with a call to list_peers and "#ID" with a call to
// explain_error, and "!TOOL ARGS" with a call to TOOL with the arguments ARGS
// (JSON, "{}" if missing), each if that tool is offered.
// The message "?system" is answered with the system prompt.
func NewServer(t testing.TB) string {
	return NewServerWithKey(t, Key)
}

// NewServerWithKey is like NewServer but requires key; "" disables authentication.
func NewServerWithKey(t testing.TB, key string) string {
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
			Tools    []openai.Tool    `json:"tools"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		last := in.Messages[len(in.Messages)-1]
		msg := openai.Message{Role: "assistant", Content: fmt.Sprintf("[%s] turns=%d last=%s", in.Model, len(in.Messages)-1, last.Content)}
		if last.Content == "?system" {
			msg.Content = in.Messages[0].Content
		}
		call, ok := toolCall(last)
		offered := func(t openai.Tool) bool { return t.Function.Name == call.Function.Name }
		if ok && slices.ContainsFunc(in.Tools, offered) {
			msg = openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{call}}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg}}})
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if key != "" && r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// toolCall is the tool call a user message of the form "@PEER TEXT", "@",
// "#ID" or "!TOOL ARGS" asks for.
func toolCall(msg openai.Message) (openai.ToolCall, bool) {
	call := openai.ToolCall{ID: "call-1", Type: "function", Function: openai.FunctionCall{Name: "list_peers", Arguments: "{}"}}
	if id, ok := strings.CutPrefix(msg.Content, "#"); ok && msg.Role == "user" {
		args, _ := json.Marshal(map[string]string{"id": id})
		call.Function = openai.FunctionCall{Name: "explain_error", Arguments: string(args)}
		return call, true
	}
	if rest, ok := strings.CutPrefix(msg.Content, "!"); ok && msg.Role == "user" {
		name, args, _ := strings.Cut(rest, " ")
		if args == "" {
			args = "{}"
		}
		call.Function = openai.FunctionCall{Name: name, Arguments: args}
		return call, true
	}
	rest, ok := strings.CutPrefix(msg.Content, "@")
	if !ok || msg.Role != "user" {
		return openai.ToolCall{}, false
	}
	if peer, text, _ := strings.Cut(rest, " "); peer != "" {
		args, _ := json.Marshal(map[string]string{"peer": peer, "message": text})
		call.Function = openai.FunctionCall{Name: "ask_peer", Arguments: string(args)}
	}
	return call, true
}

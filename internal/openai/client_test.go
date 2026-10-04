package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync/atomic"
	"testing"
)

func newServer(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL+"/", "sk-test")
}

func TestModelsSortedAndAuthenticated(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("unexpected request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"data":[{"id":"b"},{"id":"a"},{"id":"c"}]}`))
	})
	got, err := c.Models(context.Background())
	if err != nil || !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestComplete(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model    string    `json:"model"`
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || in.Model != "gpt" || len(in.Messages) != 1 {
			t.Errorf("unexpected request %s %s %+v", r.Method, r.URL.Path, in)
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`))
	})
	got, err := c.Complete(context.Background(), "gpt", []Message{{Role: "user", Content: "hello"}}, nil)
	if err != nil || got.Content != "hi" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestNoKeySendsNoAuthorization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Authorization"); h != "" {
			t.Errorf("unexpected Authorization %q", h)
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "").Models(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteNoChoices(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[]}`))
	})
	if _, err := c.Complete(context.Background(), "gpt", nil, nil); err == nil {
		t.Fatal("want error")
	}
}

func TestAPIError(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"bad key"}}`))
	})
	_, err := c.Models(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.Unauthorized() || apiErr.Message != "bad key" {
		t.Fatalf("got %v", err)
	}
	if apiErr.Error() != "openai: HTTP 401: bad key" {
		t.Fatalf("message %q", apiErr.Error())
	}
}

func TestServerErrorIsNotUnauthorized(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	_, err := c.Models(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Unauthorized() {
		t.Fatalf("got %v", err)
	}
}

func TestNetworkError(t *testing.T) {
	c := New("http://127.0.0.1:1", "k")
	if _, err := c.Models(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func TestSessionAndUserAgentHeaders(t *testing.T) {
	var sessions []string
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != UserAgent {
			t.Errorf("User-Agent = %q", ua)
		}
		sessions = append(sessions, r.Header.Get("X-Opencode-Session"))
		w.Write([]byte(`{"data":[]}`))
	})
	ctx := context.Background()
	c.Models(ctx)
	c.Models(ctx)
	c.Models(WithSession(ctx, "ctx-1"))
	if sessions[0] == "" || sessions[0] != sessions[1] || sessions[2] != "ctx-1" {
		t.Fatalf("sessions = %q", sessions)
	}
}

// A tool is offered to the model as a function with its schema, as it is.
func TestNewTool(t *testing.T) {
	const schema = `{"type":"object","properties":{"peer":{"type":"string"}}}`
	tool := NewTool("ask_peer", "Asks a peer.", schema)
	got, err := json.Marshal(tool)
	want := `{"type":"function","function":{"name":"ask_peer","description":"Asks a peer.","parameters":` + schema + `}}`
	if err != nil || string(got) != want {
		t.Fatalf("got %s, %v\nwant %s", got, err, want)
	}
}

// The model gets the tools it may call, and its calls come back.
func TestCompleteWithTools(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Tools []Tool `json:"tools"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if len(in.Tools) != 1 || in.Tools[0].Type != "function" || in.Tools[0].Function.Name != "greet" || string(in.Tools[0].Function.Parameters) != `{"type":"object"}` {
			t.Errorf("unexpected tools %+v", in.Tools)
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"greet","arguments":"{}"}}]}}]}`))
	})
	got, err := c.Complete(context.Background(), "gpt", []Message{{Role: "user", Content: "hello"}}, []Tool{NewTool("greet", "Greets.", `{"type":"object"}`)})
	if err != nil || len(got.ToolCalls) != 1 || got.ToolCalls[0] != (ToolCall{ID: "call-1", Type: "function", Function: FunctionCall{Name: "greet", Arguments: "{}"}}) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// A completion the API refuses is the error of the API, not an empty reply.
func TestCompleteAPIError(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"slow down"}}`))
	})
	got, err := c.Complete(context.Background(), "gpt", []Message{{Role: "user", Content: "hello"}}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests || apiErr.Message != "slow down" || apiErr.Unauthorized() {
		t.Fatalf("got %v", err)
	}
	if got.Content != "" || got.Role != "" || got.ToolCalls != nil {
		t.Fatalf("got %+v", got)
	}
}

// A request that cannot be made is not sent.
func TestRequestThatCannotBeMade(t *testing.T) {
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}],"data":[]}`))
	}))
	defer srv.Close()
	ctx := context.Background()

	// A schema that is not JSON cannot be put into a request.
	_, err := New(srv.URL, "").Complete(ctx, "gpt", nil, []Tool{NewTool("greet", "Greets.", "{")})
	var marshalErr *json.MarshalerError
	if !errors.As(err, &marshalErr) {
		t.Fatalf("invalid schema: %v", err)
	}
	// Neither is there a request to an address that is none.
	_, err = New(srv.URL+"\x7f", "").Models(ctx)
	var urlErr *url.Error
	if !errors.As(err, &urlErr) || urlErr.Op != "parse" {
		t.Fatalf("invalid address: %v", err)
	}
	if n := asked.Load(); n != 0 {
		t.Fatalf("the API was asked %d times", n)
	}
}

// Both a rejected and a forbidden key are a matter of the key.
func TestUnauthorized(t *testing.T) {
	for status, want := range map[int]bool{
		http.StatusUnauthorized:        true,
		http.StatusForbidden:           true,
		http.StatusNotFound:            false,
		http.StatusPaymentRequired:     false,
		http.StatusInternalServerError: false,
	} {
		if got := (&APIError{Status: status}).Unauthorized(); got != want {
			t.Errorf("Unauthorized() with HTTP %d = %v", status, got)
		}
	}
}

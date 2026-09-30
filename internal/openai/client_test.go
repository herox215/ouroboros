package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
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

func TestChat(t *testing.T) {
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
	got, err := c.Chat(context.Background(), "gpt", []Message{{Role: "user", Content: "hello"}})
	if err != nil || got != "hi" {
		t.Fatalf("got %q, %v", got, err)
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

func TestChatNoChoices(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[]}`))
	})
	if _, err := c.Chat(context.Background(), "gpt", nil); err == nil {
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

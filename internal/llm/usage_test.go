package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/credentials"
	"ouroboros/internal/openai"
)

func TestBookUsage(t *testing.T) {
	store := credentials.Open(t.TempDir())
	saved, err := store.Put(credentials.Access{Provider: "quota", Credentials: Credentials{About: "own access"}})
	if err != nil {
		t.Fatal(err)
	}
	want := Usage{Windows: []UsageWindow{{Name: "weekly", UsedPercent: 120, ResetsAt: time.Now()}},
		Balances: []UsageBalance{{Currency: "USD", Amount: "1.00"}}}
	ctx := t.Context()
	failure := errors.New("quota failed")
	var returned error
	p := Provider{Name: "quota", Usage: func(got context.Context, a Access) (Usage, error) {
		c, err := a.Credentials()
		if got != ctx || err != nil || c.About != "own access" {
			t.Fatalf("wrong context or access: %+v, %v", c, err)
		}
		return want, returned
	}}
	b := Book{Store: store, Providers: func() []Provider { return []Provider{p} }}
	if got, err := b.Usage(ctx, saved.ID); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("usage: %+v, %v", got, err)
	}
	returned = failure
	if _, err := b.Usage(ctx, saved.ID); !errors.Is(err, failure) {
		t.Fatalf("provider error: %v", err)
	}
	p.Usage = nil
	if got, err := b.Usage(ctx, saved.ID); !reflect.DeepEqual(got, Usage{}) || !errors.Is(err, ErrUsageUnavailable) {
		t.Fatalf("unavailable: %+v, %v", got, err)
	}
	if _, err := b.Usage(ctx, "missing"); !errors.Is(err, credentials.ErrGone) {
		t.Fatalf("missing access: %v", err)
	}
	p.Name = "other"
	if _, err := b.Usage(ctx, saved.ID); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("missing provider: %v", err)
	}
}

const usageKey = "sk-usage-test"

func TestFetchUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/usage" || r.Header.Get("Authorization") != "Bearer "+usageKey ||
			r.Header.Get("User-Agent") != openai.UserAgent || r.Header.Get("Accept") != "application/json" {
			t.Errorf("usage request: %s %s, headers %v", r.Method, r.URL.Path, r.Header)
		}
		fmt.Fprint(w, `{"left":1}`)
	}))
	defer srv.Close()
	if body, err := FetchUsage(t.Context(), "test usage", srv.URL+"/v1/usage", usageKey); err != nil || string(body) != `{"left":1}` {
		t.Fatalf("body %q, %v", body, err)
	}
}

// An answer of as many bytes as are taken is taken whole, and a longer one
// is refused.
func TestFetchUsageOfTheLongestAnswer(t *testing.T) {
	for _, size := range []int{usageLimit, usageLimit + 1} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, strings.Repeat("x", size))
		}))
		body, err := FetchUsage(t.Context(), "test usage", srv.URL, usageKey)
		srv.Close()
		if size == usageLimit && (err != nil || len(body) != size) {
			t.Fatalf("the longest answer: %d bytes, %v", len(body), err)
		}
		if size > usageLimit && (body != nil || err == nil || err.Error() != "test usage response is too large") {
			t.Fatalf("an answer too long: %d bytes, %v", len(body), err)
		}
	}
}

// A key that is refused is told as that, another status by its number, and
// neither the answer, where it redirects to nor a URL that cannot be read
// shows in the error.
func TestFetchUsageErrors(t *testing.T) {
	for _, status := range []int{401, 403, 429, 500, 302} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/secret/"+usageKey)
				w.WriteHeader(status)
				fmt.Fprint(w, "server body "+usageKey)
			}))
			defer srv.Close()
			body, err := FetchUsage(t.Context(), "test usage", srv.URL, usageKey)
			if err == nil || body != nil || strings.Contains(err.Error(), usageKey) || strings.Contains(err.Error(), "server body") {
				t.Fatalf("unsafe or missing error: %q, %v", body, err)
			}
			rejected := status == 401 || status == 403
			if errors.Is(err, ErrKeyRejected) != rejected || !rejected && err.Error() != fmt.Sprintf("cannot fetch test usage: HTTP %d", status) {
				t.Fatalf("classification: %v", err)
			}
		})
	}
	if body, err := FetchUsage(t.Context(), "test usage", "://"+usageKey, usageKey); body != nil || err == nil || err.Error() != "cannot create test usage request" {
		t.Fatalf("a URL that cannot be read: %q, %v", body, err)
	}
}

func TestFetchUsageCancellation(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := FetchUsage(ctx, "test usage", srv.URL, usageKey); done <- err }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not end the request")
	}
}

// transport answers a request itself, for what no server does.
type transport func(*http.Request) (*http.Response, error)

func (f transport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// useTransport makes f the transport of every client that uses the default
// one, until the test ends.
func useTransport(t *testing.T, f transport) {
	old := http.DefaultTransport
	http.DefaultTransport = f
	t.Cleanup(func() { http.DefaultTransport = old })
}

// brokenBody is a response body that breaks off: each read runs read and
// fails with the key in the error.
type brokenBody struct{ read func() }

func (b brokenBody) Read([]byte) (int, error) {
	b.read()
	return 0, errors.New("connection reset by " + usageKey)
}
func (brokenBody) Close() error { return nil }

// A transport that fails and a body that breaks off are not shown as they
// are, as either may hold the key; a body that breaks off as the call is
// canceled ends with the cancellation.
func TestFetchUsageThatBreaksOff(t *testing.T) {
	const url = "http://usage.invalid/usage"
	useTransport(t, func(*http.Request) (*http.Response, error) { return nil, errors.New("dial " + usageKey) })
	if body, err := FetchUsage(t.Context(), "test usage", url, usageKey); err == nil || body != nil || err.Error() != "cannot fetch test usage" {
		t.Fatalf("transport error: %q, %v", body, err)
	}
	useTransport(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: brokenBody{func() {}}}, nil
	})
	if body, err := FetchUsage(t.Context(), "test usage", url, usageKey); err == nil || body != nil || err.Error() != "cannot read test usage" {
		t.Fatalf("body error: %q, %v", body, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	useTransport(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: brokenBody{cancel}}, nil
	})
	if body, err := FetchUsage(ctx, "test usage", url, usageKey); !errors.Is(err, context.Canceled) || body != nil {
		t.Fatalf("canceled while reading: %q, %v", body, err)
	}
}

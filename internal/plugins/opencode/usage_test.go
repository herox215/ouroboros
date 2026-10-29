package opencode_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/llm"
	client "ouroboros/internal/openai"
	"ouroboros/internal/plugins/opencode"
)

const usageWindow = `{"status":"ok","percent":0,"resetsAt":"2026-10-15T00:00:00Z"}`

func usageBody() string {
	return fmt.Sprintf(`{"usage":{"rolling":%s,"weekly":%s,"monthly":%s}}`, usageWindow, strings.Replace(usageWindow, `"percent":0`, `"percent":45.5`, 1), strings.Replace(strings.Replace(usageWindow, `"percent":0`, `"percent":125`, 1), `"ok"`, `"rate-limited"`, 1))
}

func TestUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/zen/go/v1/usage" || r.Header.Get("Authorization") != "Bearer "+key || r.Header.Get("User-Agent") != client.UserAgent {
			t.Errorf("usage request: %s %s, headers %v", r.Method, r.URL.Path, r.Header)
		}
		fmt.Fprint(w, usageBody())
	}))
	defer srv.Close()
	// The saved endpoint wins over the provider option.
	p := opencode.Provider(opencode.Options{BaseURL: "http://unused.invalid"})
	got, err := p.Usage(t.Context(), access{c: client.Credentials(srv.URL+"/zen/go/v1/", key)})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Windows) != 3 || got.Balances != nil || got.Insufficient {
		t.Fatalf("usage: %+v", got)
	}
	for i, name := range []string{"rolling", "weekly", "monthly"} {
		if w := got.Windows[i]; w.Name != name || w.UsedPercent != []float64{0, 45.5, 125}[i] || !w.ResetsAt.Equal(time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("window %d: %+v", i, w)
		}
	}
}

func TestUsageRejectsMalformedData(t *testing.T) {
	bodies := []string{"", "null", "{}", `{"usage":null}`, `{"usage":{}}`, usageBody() + " {}", strings.Repeat("x", (64<<10)+1)}
	for _, name := range []string{"rolling", "weekly", "monthly"} {
		for _, field := range []string{"window", "status", "percent", "resetsAt"} {
			for _, value := range []string{"missing", "null", "invalid"} {
				var body map[string]any
				if err := json.Unmarshal([]byte(usageBody()), &body); err != nil {
					t.Fatal(err)
				}
				windows := body["usage"].(map[string]any)
				target, member := windows, name
				if field != "window" {
					target, member = windows[name].(map[string]any), field
				}
				switch value {
				case "missing":
					delete(target, member)
				case "null":
					target[member] = nil
				case "invalid":
					target[member] = "not valid " + key
				}
				b, _ := json.Marshal(body)
				bodies = append(bodies, string(b))
			}
		}
	}
	for _, value := range []string{"-1", "1e999", `"0"`, "true"} {
		bodies = append(bodies, strings.Replace(usageBody(), `"percent":0`, `"percent":`+value, 1))
	}
	bodies = append(bodies, strings.ReplaceAll(usageBody(), "2026-10-15T00:00:00Z", "0001-01-01T00:00:00Z"))
	for i, body := range bodies {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer srv.Close()
			p := opencode.Provider(opencode.Options{})
			got, err := p.Usage(t.Context(), access{c: client.Credentials(srv.URL, key)})
			if err == nil || got.Windows != nil {
				t.Fatalf("malformed data accepted: %+v, %v", got, err)
			}
			if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), body) && body != "" {
				t.Fatalf("response echoed: %v", err)
			}
		})
	}
}

// How the answer is fetched is the business of llm.FetchUsage, tested there:
// here only that a refused key is told as that, and that neither the answer
// nor the credentials show in an error.
func TestUsageErrors(t *testing.T) {
	for _, status := range []int{401, 403, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				fmt.Fprint(w, "server body "+key)
			}))
			defer srv.Close()
			p := opencode.Provider(opencode.Options{})
			got, err := p.Usage(t.Context(), access{c: client.Credentials(srv.URL, key)})
			if err == nil || got.Windows != nil || strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "server body") {
				t.Fatalf("unsafe or missing error: %+v, %v", got, err)
			}
			if errors.Is(err, llm.ErrKeyRejected) != (status != 500) {
				t.Fatalf("auth classification: %v", err)
			}
		})
	}
	p := opencode.Provider(opencode.Options{})
	for _, a := range []access{{}, {err: errors.New(key)}} {
		if got, err := p.Usage(t.Context(), a); err == nil || got.Windows != nil || strings.Contains(err.Error(), key) {
			t.Fatalf("credentials error: %+v, %v", got, err)
		}
	}
	// A malformed endpoint must not be echoed in the request error.
	if _, err := p.Usage(t.Context(), access{c: client.Credentials("://"+key, key)}); err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("endpoint error: %v", err)
	}
}

package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"
)

// Reproduce lib/snapshot.js at jo-inc/camofox-browser@39c8209, including
// the repeated navigation tail and overlap on the final window.
func contractWindow(full string, offset int) map[string]any {
	u := utf16.Encode([]rune(full))
	if len(u) <= 80000 {
		return map[string]any{"snapshot": full, "totalChars": len(u)}
	}
	end := min(offset+74800, len(u))
	more := offset+74800 < len(u)-5000
	marker := "\n"
	if more {
		marker = fmt.Sprintf("\n[... truncated at char %d of %d. Call snapshot with offset=%d to see more. Pagination links below. ...]\n", end, len(u), end)
	}
	return map[string]any{"snapshot": string(utf16.Decode(u[offset:end])) + marker + string(utf16.Decode(u[len(u)-5000:])), "totalChars": len(u), "truncated": true, "hasMore": more, "nextOffset": end}
}

func TestCamoContractSnapshotWindows(t *testing.T) {
	for _, size := range []int{80000, 80001, 149801, 230000} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			full := "- combobox \"Choice\" [e7]\n" + strings.Repeat("é界🙂\n", size/5) + "\n- link \"Last\" [e9]"
			var offsets []int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
				offsets = append(offsets, offset)
				json.NewEncoder(w).Encode(contractWindow(full, offset))
			}))
			defer server.Close()
			b := newCamo(settings{Endpoint: server.URL})
			c := b.conversation("one")
			tab := &camoTab{id: "t1"}
			if _, err := b.read(context.Background(), c, tab, 0); err != nil {
				t.Fatal(err)
			}
			want := strings.ReplaceAll(strings.ReplaceAll(full, "[e7]", "[7]"), "[e9]", "[9]")
			if tab.snapshot != want {
				t.Fatalf("snapshot altered: got %d bytes, want %d", len(tab.snapshot), len(want))
			}
			if !tab.selects[7] || tab.refs[9] != "e9" {
				t.Fatalf("lost refs: %v %v", tab.refs, tab.selects)
			}
			before := len(offsets)
			if _, err := b.read(context.Background(), c, tab, 2); err != nil {
				t.Fatal(err)
			}
			if len(offsets) != before {
				t.Fatal("local continuation refetched snapshot")
			}
			for i, offset := range offsets {
				if offset != i*74800 {
					t.Fatalf("not JS offsets: %v", offsets)
				}
			}
		})
	}
}

func TestCamoContractSelectAndPopupReconciliation(t *testing.T) {
	var user string
	var popup, selected bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user == "" {
			user = r.URL.Query().Get("userId")
		}
		if r.URL.Query().Get("userId") != user {
			t.Error("wrong user scope")
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /tabs":
			json.NewEncoder(w).Encode(map[string]any{"tabId": "main"})
		case "GET /tabs":
			tabs := []map[string]string{{"tabId": "main", "url": "https://main/"}}
			if popup {
				tabs = append([]map[string]string{{"tabId": "popup", "url": "https://popup/"}}, tabs...)
			}
			json.NewEncoder(w).Encode(map[string]any{"tabs": tabs})
		case "GET /tabs/main/snapshot":
			json.NewEncoder(w).Encode(map[string]any{"url": "https://main/", "snapshot": "- combobox \"Country\" [e1]\n- button \"Popup\" [e2]\n- textbox \"Name\" [e3]"})
		case "GET /tabs/popup/snapshot":
			json.NewEncoder(w).Encode(map[string]any{"url": "https://popup/", "snapshot": "- textbox \"Other\" [e1]"})
		case "POST /tabs/main/select":
			var p map[string]any
			json.NewDecoder(r.Body).Decode(&p)
			if p["ref"] != "e1" || p["option"] != "Canada" || p["text"] != nil || p["mode"] != nil {
				t.Errorf("select payload %v", p)
			}
			selected = true
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		case "POST /tabs/main/click":
			popup = true
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		case "POST /tabs/popup/type":
			var p map[string]any
			json.NewDecoder(r.Body).Decode(&p)
			if p["mode"] != "fill" || p["text"] != "hello" {
				t.Errorf("popup inherited select refs: %v", p)
			}
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	b := newCamo(settings{Endpoint: server.URL})
	ctx := context.Background()
	run := func(a args) string {
		t.Helper()
		out, err := b.run(ctx, "one", "https://main/", a)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	run(args{Action: "open"})
	main := b.conversation("one").tabs[0]
	run(args{Action: "type", Element: 1, Text: "Canada"})
	if !selected {
		t.Fatal("select not routed")
	}
	run(args{Action: "click", Element: 2})
	c := b.conversation("one")
	if len(c.tabs) != 2 || c.tabs[0] != main || c.current != 0 {
		t.Fatal("popup lost or current reordered")
	}
	if c.tabs[1].refs != nil {
		t.Fatal("popup inherited refs")
	}
	run(args{Action: "tabs"})
	if c.tabs[0] != main || main.refs[1] != "e1" {
		t.Fatal("tabs lost stable objects/refs")
	}
	run(args{Action: "switch", Tab: 2})
	if _, err := b.run(ctx, "one", "", args{Action: "type", Element: 1, Text: "hello"}); err == nil {
		t.Fatal("unread popup used opener refs")
	}
	run(args{Action: "read"})
	run(args{Action: "type", Element: 1, Text: "hello"})
}

func TestCamoContractCanceledCreateDiscoveredByTabs(t *testing.T) {
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	owner := ""
	created := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := r.URL.Query().Get("userId")
		if r.Method == "POST" {
			mu.Lock()
			owner = user
			mu.Unlock()
			close(entered)
			<-release
			mu.Lock()
			created = true
			mu.Unlock()
			json.NewEncoder(w).Encode(map[string]string{"tabId": "late"})
			close(finished)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		tabs := []map[string]string{}
		if created && user == owner {
			tabs = append(tabs, map[string]string{"tabId": "late", "url": "https://late/"})
		}
		json.NewEncoder(w).Encode(map[string]any{"tabs": tabs})
	}))
	defer server.Close()
	b := newCamo(settings{Endpoint: server.URL})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := b.run(ctx, "one", "https://late/", args{Action: "open"}); done <- err }()
	<-entered
	cancel()
	err := <-done
	close(release)
	<-finished
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	out, err := b.run(context.Background(), "one", "", args{Action: "tabs"})
	if err != nil || !strings.Contains(out, "https://late/") {
		t.Fatalf("late tab undiscoverable: %q %v", out, err)
	}
	out, err = b.run(context.Background(), "other", "", args{Action: "tabs"})
	if err != nil || out != "the conversation has no tab" {
		t.Fatalf("late tab crossed user: %q %v", out, err)
	}
}

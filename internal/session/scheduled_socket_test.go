package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/storage"
)

// cancelsLate is a context that is canceled only once it was asked after
// times: it is canceled while the Service checks it, between two checks.
type cancelsLate struct {
	context.Context
	after int32
	calls atomic.Int32
}

func (c *cancelsLate) Err() error {
	if c.calls.Add(1) > c.after {
		return context.Canceled
	}
	return nil
}

// A notification and a scheduled answer that are canceled while the Service
// takes them are refused, and start nothing.
func TestCanceledWhileTaken(t *testing.T) {
	s := service(t, &fakeAgent{run: func(context.Context, string, func(assistant.Step), func(string)) (string, error) {
		t.Error("unexpected run")
		return "", nil
	}})
	if err := s.Notify(&cancelsLate{Context: t.Context(), after: 1}, "n", "result"); !errors.Is(err, context.Canceled) {
		t.Fatalf("notify: %v", err)
	}
	if _, _, err := s.RunScheduled(&cancelsLate{Context: t.Context(), after: 1}, "job", "go", "", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("scheduled: %v", err)
	}
	if len(s.sessions) != 0 {
		t.Fatal("a canceled request started a session")
	}
}

// A scheduled answer canceled just as it begins ends canceled at once.
func TestScheduledCanceledAsItBegins(t *testing.T) {
	s := service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}})
	_, result, err := s.RunScheduled(&cancelsLate{Context: t.Context(), after: 2}, "job", "go", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if out := scheduledResult(t, result); !errors.Is(out.Err, context.Canceled) {
		t.Fatalf("result: %+v", out)
	}
}

// A scheduled answer the owner stops ends canceled, though its caller still
// waits for it.
func TestScheduledStoppedByTheOwner(t *testing.T) {
	entered := make(chan struct{})
	s := service(t, &fakeAgent{run: func(ctx context.Context, _ string, _ func(assistant.Step), _ func(string)) (string, error) {
		close(entered)
		<-ctx.Done()
		return "partial", nil
	}})
	id, result, err := s.RunScheduled(t.Context(), "job", "go", "", false)
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	if err := s.Cancel(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if out := scheduledResult(t, result); !errors.Is(out.Err, context.Canceled) {
		t.Fatalf("result: %+v", out)
	}
}

// A scheduled session without a name is called a scheduled job, and a long
// name is cut to a title.
func TestScheduledTitle(t *testing.T) {
	s := service(t, &fakeAgent{})
	title := func(name string) string {
		t.Helper()
		id, result, err := s.RunScheduled(t.Context(), name, "go", "", false)
		if err != nil {
			t.Fatal(err)
		}
		scheduledResult(t, result)
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.sessions[id].title
	}
	if got := title(" "); got != "Scheduled job" {
		t.Errorf("title %q", got)
	}
	long := "a" + strings.Repeat("ü", history.MaxTitle)
	if got := title(long); len(got) > history.MaxTitle || len(got) < history.MaxTitle-1 || !utf8.ValidString(got) || !strings.HasPrefix(long, got) {
		t.Errorf("title %q", got)
	}
}

// breakArchive runs sql on the database of the archive arch in dir, which
// it opens first.
func breakArchive(t *testing.T, arch *history.Archive, dir, sql string) {
	t.Helper()
	if err := arch.SetTitle("owner/opened", "Opened"); err != nil {
		t.Fatal(err)
	}
	db, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(sql); err != nil {
		t.Fatal(err)
	}
}

// A scheduled session whose directory or title cannot be kept does not
// start.
func TestScheduledNotKept(t *testing.T) {
	for want, sql := range map[string]string{
		"scheduled session cannot be kept": "CREATE TRIGGER broken BEFORE INSERT ON history_sessions BEGIN SELECT RAISE(ABORT,'broken'); END",
		"scheduled title cannot be kept":   "CREATE TRIGGER broken BEFORE UPDATE OF title ON history_sessions BEGIN SELECT RAISE(ABORT,'broken'); END",
	} {
		t.Run(want, func(t *testing.T) {
			dir := t.TempDir()
			s, arch, _ := archived(t, &fakeAgent{}, dir, time.Now())
			breakArchive(t, arch, dir, sql)
			if _, _, err := s.RunScheduled(t.Context(), "job", "go", "", false); err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("started: %v", err)
			}
			if len(s.sessions) != 0 {
				t.Fatal("a session that cannot be kept was published")
			}
		})
	}
}

// A notification is refused while the agent is not ready, once the Service
// is closed, and while ouro starts anew.
func TestNotifyRefused(t *testing.T) {
	notReady := errors.New("not ready")
	if err := service(t, &fakeAgent{ready: notReady}).Notify(t.Context(), "n", "result"); !errors.Is(err, notReady) {
		t.Fatalf("not ready: %v", err)
	}
	s := service(t, &fakeAgent{})
	s.node.successor = "/new/ouro"
	if err := s.Notify(t.Context(), "n", "result"); !errors.Is(err, ErrRestarting) {
		t.Fatalf("restarting: %v", err)
	}
	s.node.successor = ""
	s.Close()
	if err := s.Notify(t.Context(), "n", "result"); !errors.Is(err, ErrStopped) {
		t.Fatalf("stopped: %v", err)
	}
}

// A scheduled answer and a notification the node cannot read are refused as
// that, and so is one the Service refuses.
func TestScheduledSocketRefusals(t *testing.T) {
	s := service(t, &fakeAgent{})
	h := Handler(s)
	for _, path := range []string{"/run-scheduled", "/notify"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader("{")))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "the request cannot be read") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	c := NewClient(serve(t, h).dir)
	if _, _, err := c.RunScheduled(t.Context(), "job", " ", "", false); err == nil {
		t.Fatal("empty scheduled answer started")
	}
}

// brokenWriter is a response nobody reads any more.
type brokenWriter struct{ header http.Header }

func (w *brokenWriter) Header() http.Header        { return w.header }
func (w *brokenWriter) Write([]byte) (int, error)  { return 0, errors.New("gone") }
func (w *brokenWriter) WriteHeader(statusCode int) {}

// A scheduled answer whose caller is gone before it heard of it runs
// without being told.
func TestScheduledCallerGone(t *testing.T) {
	s := service(t, &fakeAgent{})
	body, _ := json.Marshal(scheduledRequest{Name: "job", Text: "go"})
	s.serveScheduled(&brokenWriter{header: http.Header{}}, httptest.NewRequest(http.MethodPost, "/run-scheduled", strings.NewReader(string(body))))
	s.answers.Wait()
}

// A scheduled answer that fails tells the caller over the socket why.
func TestScheduledSocketFailure(t *testing.T) {
	s := service(t, &fakeAgent{run: func(context.Context, string, func(assistant.Step), func(string)) (string, error) {
		return "partial", errors.New("job failed")
	}})
	c := NewClient(serve(t, Handler(s)).dir)
	_, result, err := c.RunScheduled(t.Context(), "job", "go", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if out := scheduledResult(t, result); out.Err == nil || out.Err.Error() != "job failed" || out.Text != "partial" {
		t.Fatalf("result: %+v", out)
	}
}

// The client takes what a node tells of a scheduled answer as it says it: a
// start it cannot read fails, an end it does not tell is an error, and an
// end that was canceled or ran out of time is that.
func TestScheduledClientReadsTheNode(t *testing.T) {
	n := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in scheduledRequest
		json.NewDecoder(r.Body).Decode(&in)
		switch in.Name {
		case "garbage":
			io.WriteString(w, "nonsense\n")
		case "cut":
			io.WriteString(w, `{"id":"s1"}`+"\n")
		case "canceled":
			io.WriteString(w, `{"id":"s1"}`+"\n"+`{"canceled":true}`+"\n")
		case "deadline":
			io.WriteString(w, `{"id":"s1"}`+"\n"+`{"deadline":true}`+"\n")
		}
	}))
	c := NewClient(n.dir)
	if _, _, err := c.RunScheduled(t.Context(), "garbage", "go", "", false); err == nil {
		t.Fatal("read a start that is none")
	}
	for name, want := range map[string]error{"cut": io.ErrUnexpectedEOF, "canceled": context.Canceled, "deadline": context.DeadlineExceeded} {
		_, result, err := c.RunScheduled(t.Context(), name, "go", "", false)
		if err != nil {
			t.Fatal(err)
		}
		out := scheduledResult(t, result)
		if name == "cut" {
			if out.Err == nil {
				t.Errorf("%s: no error", name)
			}
			continue
		}
		if !errors.Is(out.Err, want) {
			t.Errorf("%s: %v", name, out.Err)
		}
	}
}

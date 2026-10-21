package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
)

func attachmentPaths(t *testing.T, prompt string) []string {
	t.Helper()
	matches := regexp.MustCompile(`path=("(?:[^"\\]|\\.)*")`).FindAllStringSubmatch(prompt, -1)
	var paths []string
	for _, m := range matches {
		p, err := strconv.Unquote(m[1])
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	return paths
}

func TestAttachmentInputAndReply(t *testing.T) {
	for _, socket := range []bool{false, true} {
		for _, reply := range []bool{false, true} {
			t.Run(strconv.FormatBool(socket)+"/reply="+strconv.FormatBool(reply), func(t *testing.T) {
				seen := make(chan string, 1)
				a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
					if reply {
						got, err := agent.AskOwnerReply(ctx, json.RawMessage(`{"question":"Allow?"}`))
						if err != nil {
							return "", err
						}
						if !got.HasAttachments || len(got.Images) != 0 {
							t.Errorf("missing file marker or fake image: %+v", got)
						}
						text = got.Text
					}
					seen <- text
					return "received", nil
				}}
				s := service(t, a)
				var input AttachmentAPI = s
				var steering ReplyAttachmentAPI = s
				var api API = s
				if socket {
					c := NewClient(serve(t, Handler(s)).dir)
					input, steering, api = c, c, c
				}
				_, live, err := api.Watch(t.Context(), "s1")
				if err != nil {
					t.Fatal(err)
				}
				files := []Attachment{{Name: "../../escape\n\"evil", Type: "application/octet-stream", Data: []byte{0, 1, 255}}, {Name: "empty"}}
				if reply {
					if err := api.Ask(t.Context(), "s1", "begin"); err != nil {
						t.Fatal(err)
					}
					until(t, live, Asked)
					err = steering.SteerAttachments(t.Context(), "s1", assistant.AllowAlways, files)
				} else {
					err = input.AskAttachments(t.Context(), "s1", "caption", files)
				}
				if err != nil {
					t.Fatal(err)
				}
				until(t, live, Answer)
				text := <-seen
				paths := attachmentPaths(t, text)
				if len(paths) != 2 || !strings.Contains(text, `name="../../escape\n\"evil"`) {
					t.Fatalf("metadata: %q", text)
				}
				t.Cleanup(func() { os.RemoveAll(filepath.Dir(paths[0])) })
				for i, p := range paths {
					data, err := os.ReadFile(p)
					if err != nil || !bytes.Equal(data, files[i].Data) {
						t.Fatalf("file %d: %v %v", i, data, err)
					}
					if filepath.Base(p) != "file-"+strconv.Itoa(i+1) || filepath.Dir(p) != filepath.Dir(paths[0]) {
						t.Fatalf("unsafe path: %s", p)
					}
					st, err := os.Stat(p)
					if err != nil || st.Mode().Perm() != 0600 {
						t.Fatalf("file mode: %v %v", st, err)
					}
				}
				st, err := os.Stat(filepath.Dir(paths[0]))
				if err != nil || st.Mode().Perm() != 0700 {
					t.Fatalf("directory mode: %v %v", st, err)
				}
			})
		}
	}
}

func TestAttachmentLimitsAndFailedDelivery(t *testing.T) {
	tmp := t.TempDir()
	s := service(t, &fakeAgent{ready: errors.New("not ready")})
	// Socket errors preserve their sentinel identity. The socket lives below
	// the usual temporary directory: below tmp its path would exceed the length
	// a Unix socket may have on macOS.
	c := NewClient(serve(t, Handler(s)).dir)
	if err := c.SteerAttachments(t.Context(), "s1", "", make([]Attachment, MaxAttachments+1)); !errors.Is(err, ErrAttachment) {
		t.Fatalf("socket error: %v", err)
	}
	t.Setenv("TMPDIR", tmp)
	for _, files := range [][]Attachment{
		make([]Attachment, MaxAttachments+1),
		{{Data: make([]byte, MaxAttachmentBytes)}, {Data: []byte{1}}},
		{{Name: strings.Repeat("n", maxAttachmentMetadata+1)}},
	} {
		if err := s.AskAttachments(t.Context(), "s1", "", files); !errors.Is(err, ErrAttachment) {
			t.Fatalf("limit: %v", err)
		}
	}
	if err := s.AskAttachments(t.Context(), "s1", "", []Attachment{{Name: "empty"}}); err == nil {
		t.Fatal("expected readiness failure")
	}
	entries, err := os.ReadDir(tmp)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed delivery leaked: %v %v", entries, err)
	}
	// Exactly the aggregate limit is accepted, including an empty file.
	_, dir, err := storeAttachments("", []Attachment{{Data: make([]byte, MaxAttachmentBytes)}, {}})
	if err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(dir)
}

func TestQueuedAndRelayedAttachmentMarker(t *testing.T) {
	for _, relay := range []bool{false, true} {
		s := service(t, &fakeAgent{})
		s.mu.Lock()
		target := s.get("s1")
		s.emit(target, Event{Kind: Message, Text: "Discuss supplied files"})
		target.cancel = func() {}
		var pending chan queued
		id := "s1"
		if relay {
			pending = make(chan queued, 1)
			target.asked = pending
			s.get(ThreadID).questions = []*relayed{{se: target}}
			id = ThreadID
		}
		s.mu.Unlock()
		if err := s.SteerAttachments(t.Context(), id, "", []Attachment{{Name: "empty"}}); err != nil {
			t.Fatal(err)
		}
		s.mu.Lock()
		var got queued
		if relay {
			select {
			case got = <-pending:
			default:
				t.Fatal("not relayed")
			}
		} else {
			if len(target.queue) != 1 {
				t.Fatal("not queued")
			}
			got = target.queue[0]
		}
		s.mu.Unlock()
		if !got.hasAttachments || len(got.images) != 0 {
			t.Fatalf("lost marker: %+v", got)
		}
		paths := attachmentPaths(t, got.text)
		if len(paths) != 1 {
			t.Fatalf("missing file: %q", got.text)
		}
		if data, err := os.ReadFile(paths[0]); err != nil || len(data) != 0 {
			t.Fatalf("empty file unreadable: %v", err)
		}
		os.RemoveAll(filepath.Dir(paths[0]))
	}
}

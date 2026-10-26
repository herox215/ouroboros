package whatsapp

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// blockingBackend is a backend whose Connect waits, as one that waits for
// a QR does.
type blockingBackend struct {
	fakeBackend
	entered, release chan struct{}
}

func (b *blockingBackend) Connect(ctx context.Context) (status, error) {
	close(b.entered)
	<-b.release
	return status{}, nil
}

// A connect that waits for a QR does not hold up the other tools.
func TestConnectDoesNotBlockOtherTools(t *testing.T) {
	b := &blockingBackend{entered: make(chan struct{}), release: make(chan struct{})}
	s := &service{path: filepath.Join(t.TempDir(), "private"), makeClient: func(context.Context, string, *textStore) (backend, error) { return b, nil }}
	defer s.close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		call(t, s, "whatsapp_connect", `{}`)
	}()
	<-b.entered
	status := make(chan string)
	go func() { status <- call(t, s, "whatsapp_status", `{}`) }()
	select {
	case <-status:
	case <-time.After(5 * time.Second):
		t.Fatal("whatsapp_status waited for whatsapp_connect")
	}
	close(b.release)
	<-done
}

// A long text is cut at the start of a character, so what is kept is valid.
func TestLongTextIsCutAtACharacter(t *testing.T) {
	texts, err := openTextStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer texts.close()
	long := "a" + strings.Repeat("ä", maxText) // the cap falls inside a character
	if err := texts.put(message{Chat: "123@s.whatsapp.net", ID: "long", Text: long, Time: 1}); err != nil {
		t.Fatal(err)
	}
	got, err := texts.read("123@s.whatsapp.net", 1)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	if text := got[0].Text; !utf8.ValidString(text) || len(text) != maxText-1 {
		t.Fatalf("kept %d bytes, valid %v", len(text), utf8.ValidString(text))
	}
}

// The note on lost history comes in addition to what the status says.
func TestStorageFailureKeepsTheNote(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	texts, err := openTextStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer texts.close()
	b, err := newClient(context.Background(), path, texts)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	c := b.(*client)
	c.note = "Scan the private local PNG."
	c.storageFailed.Store(true)
	if note := c.Status().Note; !strings.Contains(note, "Scan the private local PNG.") || !strings.Contains(note, "could not be persisted") {
		t.Fatalf("note %q", note)
	}
}

package tui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"ouroboros/plugin"
)

// Generic files use the same local-path transport as pictures, not image input.
func TestPastedFilesAreTypedAsLabels(t *testing.T) {
	dir := files(t, "notes.txt", "my report.pdf", "archive", "cat.png")
	empty := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(dir, "my report.pdf")
	for _, text := range []string{report, quote(report), "'" + report + "'", strings.ReplaceAll(report, " ", `\ `), "file://" + strings.ReplaceAll(report, " ", "%20"), empty, filepath.Join(dir, "archive")} {
		m, b := newModel(t)
		m.Update(tea.PasteMsg{Content: text})
		if m.input.Value() != "[File_1] " || len(m.chat.files) != 1 || len(m.chat.images) != 0 {
			t.Fatalf("%q: input %q, files %q", text, m.input.Value(), m.chat.files)
		}
		if len(b.messages()) != 0 {
			t.Fatal("pasting must not send")
		}
	}
	m, _ := newModel(t)
	notes := filepath.Join(dir, "notes.txt")
	cat := filepath.Join(dir, "cat.png")
	m.Update(tea.PasteMsg{Content: quote(notes) + "\n" + quote(cat) + "\n" + quote(report) + " " + quote(notes)})
	if m.input.Value() != "[File_1] [Image_1] [File_2] [File_1] " || !slices.Equal(m.chat.files, []string{notes, report}) || !slices.Equal(m.chat.images, []string{cat}) {
		t.Fatalf("mixed input %q, files %q", m.input.Value(), m.chat.files)
	}
	for _, text := range []string{notes + " " + filepath.Join(dir, "missing.txt"), "look at " + notes, filepath.Join(dir, "dir.png"), "notes.txt"} {
		m.input.Reset()
		m.Update(tea.PasteMsg{Content: text})
		if m.input.Value() != text {
			t.Fatalf("ordinary text %q became %q", text, m.input.Value())
		}
	}
}

func TestFileLabelsAreSentRestoredAndRecalled(t *testing.T) {
	m, b := newModel(t)
	path := filepath.Join(files(t, `my "notes".txt`), `my "notes".txt`)
	ctrlV(t, m, plugin.Picture{}, plugin.ErrNoPicture, quote(path))
	m.input.InsertString("read this [File_0] [File_2] [File_99999999999999999999]")
	input := m.input.Value()
	if !strings.Contains(m.chatView(), imageStyle.Render("[File_1]")) {
		t.Fatal("input must color file labels")
	}
	b.fail = map[string]error{"ask": errors.New("busy")}
	drive(t, m, press(m, "enter"), func() bool { return m.noticeErr })
	if m.input.Value() != input {
		t.Fatalf("refused input %q", m.input.Value())
	}
	b.fail = nil
	answered(t, m, press(m, "enter"))
	want := quote(path) + " read this [File_0] [File_2] [File_99999999999999999999]"
	if got := b.messages(); len(got) != 2 || got[0].text != want || got[1].text != want {
		t.Fatalf("sent %+v", got)
	}
	m.renderHistory(true)
	if !strings.Contains(m.history.View(), imageStyle.Render("[File_1]")) {
		t.Fatal("history must color file labels")
	}
	if m.chat.labelled(want) != input {
		t.Fatal("file label round trip failed")
	}
	if got := m.chat.muted("see [File_1]."); got != mutedStyle.Render("see ")+imageStyle.Render("[File_1]")+mutedStyle.Render(".") {
		t.Fatalf("muted %q", got)
	}
	other := &conversation{}
	if got := other.relabel(want); got != input || !slices.Equal(other.files, []string{path}) {
		t.Fatalf("recall %q, files %q", got, other.files)
	}
	m.replay(m.chat, nil)
	if !slices.Equal(m.chat.files, []string{path}) || m.chat.expand(input) != want {
		t.Fatal("replay lost file labels")
	}
}

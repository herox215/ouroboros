package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"

	"ouroboros/plugin"
)

// A picture gets into a message as the path of its file, in double quotes:
// the agent reads it from there with its tools. A file that is pasted or
// dropped comes from the terminal as text, the path as the terminal writes
// it, and is put in quotes. A picture that is no file, as a screenshot on
// the clipboard, comes from no terminal: ctrl+v reads it from the clipboard
// of the system, see plugin.ClipboardPicture, and the node keeps it as a file of the
// profile, see Backend.SavePicture. Nothing is sent by it: the path is typed
// for the owner, who sends it.

// readPicture reads the picture on the clipboard, and readClipboard its
// text. Tests replace them.
var (
	readPicture = func(ctx context.Context) (plugin.Picture, error) {
		return plugin.ClipboardPicture(ctx)
	}
	readClipboard = clipboard.ReadAll
)

// pastedMsg is what ctrl+v read from the clipboard: text to type, or why a
// picture could not be kept.
type pastedMsg struct {
	text string
	err  error
}

// paste types text that was pasted into the input of the chat: the files of
// pictures it names as their paths in quotes, anything else as it is. A
// terminal pastes a picture that is no file as nothing: then the clipboard
// is asked for it.
func (m *Model) paste(text string) tea.Cmd {
	if strings.TrimSpace(text) == "" {
		return m.pasteClipboard()
	}
	if paths := pictures(text); len(paths) > 0 {
		text = quoted(paths)
	}
	m.input.InsertString(text)
	return nil
}

// pasteClipboard reads the clipboard of the system: a picture on it is
// typed as the path of its file, which the node keeps if it was none, and
// without a picture its text is typed.
func (m *Model) pasteClipboard() tea.Cmd {
	return func() tea.Msg {
		p, err := readPicture(m.ctx)
		if errors.Is(err, plugin.ErrNoPicture) {
			text, _ := readClipboard() // a clipboard that cannot be read has no text
			return pastedMsg{text: text}
		}
		if err != nil {
			return pastedMsg{err: err}
		}
		if p.Path == "" {
			if p.Path, err = m.backend.SavePicture(p.Data, p.Ext); err != nil {
				return pastedMsg{err: err}
			}
		}
		return pastedMsg{text: quoted([]string{p.Path})}
	}
}

// pasted types what ctrl+v read from the clipboard.
func (m *Model) pasted(msg pastedMsg) {
	if msg.err != nil {
		m.setNotice("the picture was not pasted: "+msg.err.Error(), true)
		return
	}
	if strings.TrimSpace(msg.text) != "" {
		m.paste(msg.text)
	}
}

// pictures are the files of pictures that text names and nothing else, as a
// terminal writes the files dropped on it: one path, or several apart by
// spaces, each plain, in quotes, with its spaces escaped, or as a URI. nil
// if text is anything else.
func pictures(text string) []string {
	text = strings.TrimSpace(text)
	if path, ok := picture(text); ok {
		return []string{path} // a path with spaces, as it is
	}
	names, ok := words(text)
	if !ok {
		return nil
	}
	var paths []string
	for _, name := range names {
		path, ok := picture(name)
		if !ok {
			return nil
		}
		paths = append(paths, path)
	}
	return paths
}

// picture is the path of the file name names, if it is a picture that is
// there.
func picture(name string) (string, bool) {
	path := plugin.LocalPath(name)
	if !filepath.IsAbs(path) || !plugin.IsPicture(path) {
		return "", false
	}
	info, err := os.Stat(path)
	return path, err == nil && info.Mode().IsRegular()
}

// words splits text as a shell does: at spaces, but for those in single or
// double quotes or after a backslash. It reports false for a quote that
// does not end.
func words(text string) ([]string, bool) {
	var words []string
	var word strings.Builder
	quote, escaped, open := rune(0), false, false
	for _, r := range text {
		switch {
		case escaped:
			word.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped, open = true, true
		case quote != 0 && r == quote:
			quote = 0
		case quote != 0:
			word.WriteRune(r)
		case r == '\'' || r == '"':
			quote, open = r, true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if open {
				words = append(words, word.String())
				word.Reset()
				open = false
			}
		default:
			word.WriteRune(r)
			open = true
		}
	}
	if open {
		words = append(words, word.String())
	}
	return words, quote == 0 && !escaped
}

// escapes are what a path in double quotes has before a quote and a
// backslash of its own.
var escapes = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// quoted are paths as they are typed into a message: each in double quotes,
// a quote or a backslash in it after a backslash, a space after each.
func quoted(paths []string) string {
	var b strings.Builder
	for _, path := range paths {
		b.WriteString(`"` + escapes.Replace(path) + `" `)
	}
	return b.String()
}

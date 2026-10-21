package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"

	"ouroboros/plugin"
)

// An attachment gets into a message as the path of its file, in double quotes:
// the agent reads it from there with its tools. The input and the chat show
// it as [Image_n] for pictures or [File_n] for other files, numbered
// within the conversation; what is sent has the paths. A file that is
// pasted or dropped comes from the terminal as text, the path as the
// terminal writes it. A picture that is no file, as a screenshot on
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

// pastedMsg is what ctrl+v read from the clipboard: text to type, the file
// of a picture, or why a picture could not be kept.
type pastedMsg struct {
	text, picture string
	err           error
}

// paste types text that was pasted into the input of the chat: the files of
// attachments it names as their labels, anything else as it is. A terminal
// pastes a picture that is no file as nothing: then the clipboard is asked
// for it.
func (m *Model) paste(text string) tea.Cmd {
	if strings.TrimSpace(text) == "" {
		return m.pasteClipboard()
	}
	if paths := attachmentPaths(text); len(paths) > 0 {
		m.typeAttachments(paths)
		return nil
	}
	m.input.InsertString(text)
	return nil
}

// typeAttachments types the labels of the attachments at paths, a space after
// each.
func (m *Model) typeAttachments(paths []string) {
	var b strings.Builder
	for _, path := range paths {
		b.WriteString(m.chat.label(path) + " ")
	}
	m.input.InsertString(b.String())
}

// labels are attachment labels, with their kind and number.
var labels = regexp.MustCompile(`\[(Image|File)_(\d+)\]`)

// labelOf is the label of the n-th picture of a conversation, from 1.
func labelOf(n int) string { return fmt.Sprintf("[Image_%d]", n) }

// label returns the existing or next label for an attachment at path.
// Images keep their own numbering for compatibility with picture summaries.
func (c *conversation) label(path string) string {
	paths, kind := &c.files, "File"
	if plugin.IsPicture(path) {
		paths, kind = &c.images, "Image"
	}
	i := slices.Index(*paths, path)
	if i < 0 {
		*paths = append(*paths, path)
		i = len(*paths) - 1
	}
	return fmt.Sprintf("[%s_%d]", kind, i+1)
}

// attachment returns the local file a label stands for, if known.
func (c *conversation) attachment(label string) (string, bool) {
	match := labels.FindStringSubmatch(label)
	if match == nil {
		return "", false
	}
	paths := c.images
	if match[1] == "File" {
		paths = c.files
	}
	n, _ := strconv.Atoi(match[2]) // too large a number is 0
	if n < 1 || n > len(paths) {
		return "", false
	}
	return paths[n-1], true
}

// expand returns text with the attachment labels of c as their paths in
// quotes, as the message is sent. A label c has no attachment for stays.
func (c *conversation) expand(text string) string {
	return labels.ReplaceAllStringFunc(text, func(label string) string {
		if path, ok := c.attachment(label); ok {
			return quote(path)
		}
		return label
	})
}

// labelled returns text with the paths of the attachments of c in quotes as
// their labels, as the input and the chat show it: expand undone.
func (c *conversation) labelled(text string) string {
	for i, path := range c.images {
		text = strings.ReplaceAll(text, quote(path), labelOf(i+1))
	}
	for i, path := range c.files {
		text = strings.ReplaceAll(text, quote(path), fmt.Sprintf("[File_%d]", i+1))
	}
	return text
}

// showLabels colors the attachment labels of c in text: one that
// stands for none is text like any other.
func (c *conversation) showLabels(text string) string {
	return labels.ReplaceAllStringFunc(text, func(label string) string {
		if _, ok := c.attachment(label); ok {
			return imageStyle.Render(label)
		}
		return label
	})
}

// relabel returns text with the paths in quotes of the attachments in it as
// their labels in c, which it gets for those it has none for: expand
// undone, also for a text of another conversation.
func (c *conversation) relabel(text string) string {
	return quoted.ReplaceAllStringFunc(text, func(path string) string {
		if file, ok := attachmentPath(unescapes.Replace(path[1 : len(path)-1])); ok {
			return c.label(file)
		}
		return path
	})
}

// quoted is a path in double quotes, as quote writes it.
var quoted = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)

// short returns text with quoted paths shortened to known attachment labels
// or the basename of any other file.
func (c *conversation) short(text string) string {
	return quoted.ReplaceAllStringFunc(c.labelled(text), func(path string) string {
		return filepath.Base(unescapes.Replace(path[1 : len(path)-1]))
	})
}

// unescapes undo escapes.
var unescapes = strings.NewReplacer(`\\`, `\`, `\"`, `"`)

// muted renders text muted, and the attachment labels of c in it in
// their color.
func (c *conversation) muted(text string) string {
	var b strings.Builder
	last := 0
	for _, at := range labels.FindAllStringIndex(text, -1) {
		if _, ok := c.attachment(text[at[0]:at[1]]); !ok {
			continue
		}
		b.WriteString(mutedStyle.Render(text[last:at[0]]) + imageStyle.Render(text[at[0]:at[1]]))
		last = at[1]
	}
	return b.String() + mutedStyle.Render(text[last:])
}

// pasteClipboard reads the clipboard of the system: a picture on it is
// typed as the label of its file, which the node keeps if it was none, and
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
		return pastedMsg{picture: p.Path}
	}
}

// pasted types what ctrl+v read from the clipboard.
func (m *Model) pasted(msg pastedMsg) {
	switch {
	case msg.err != nil:
		m.setNotice("the picture was not pasted: "+msg.err.Error(), true)
	case msg.picture != "":
		m.typeAttachments([]string{msg.picture})
	case strings.TrimSpace(msg.text) != "":
		m.paste(msg.text)
	}
}

// attachmentPaths are the regular files that text names and nothing else, as a
// terminal writes the files dropped on it: one path, or several apart by
// spaces, each plain, in quotes, with its spaces escaped, or as a URI. nil
// if text is anything else.
func attachmentPaths(text string) []string {
	text = strings.TrimSpace(text)
	if path, ok := attachmentPath(text); ok {
		return []string{path} // a path with spaces, as it is
	}
	names, ok := words(text)
	if !ok {
		return nil
	}
	var paths []string
	for _, name := range names {
		path, ok := attachmentPath(name)
		if !ok {
			return nil
		}
		paths = append(paths, path)
	}
	return paths
}

// attachmentPath resolves an absolute local path to an existing regular file.
func attachmentPath(name string) (string, bool) {
	path := plugin.LocalPath(name)
	if !filepath.IsAbs(path) {
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

// quote is a path as it is sent in a message: in double quotes, a quote or
// a backslash in it after a backslash.
func quote(path string) string { return `"` + escapes.Replace(path) + `"` }

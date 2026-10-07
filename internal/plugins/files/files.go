// Package files is the plugin with the tools for files: read, write, edit,
// list, delete and search. Where each of them may go is for the owner to say, per
// tool and per kind of conversation, with the settings "allow" and "deny" in
// the configuration; what no tool does, whatever is configured, is decided by
// package guard.
package files

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"ouroboros/internal/assistant"
	"ouroboros/internal/guard"
	"ouroboros/internal/safefile"
	"ouroboros/plugin"
)

// The names of the tools.
const (
	Read   = "read"
	Write  = "write"
	Edit   = "edit"
	List   = "list"
	Delete = "delete"
	Search = "search"
)

// The limits of the tools.
const (
	maxReadLines  = 2000      // lines one call of read returns
	maxReadBytes  = 256 << 10 // bytes one call of read returns
	maxWriteBytes = 1 << 20   // bytes of a file that write or edit leave behind
	editContext   = 3         // lines edit shows before and after what it changed
	maxEditLines  = 50        // lines edit shows at most
	maxListed     = 1000      // entries one call of list returns
	maxMatches    = 200       // lines one call of search returns
	maxMatchBytes = 300       // bytes of a line search shows
)

// maxSearched is how many files one call of search looks into, and
// maxWalked how many files and directories it looks at, whether their names
// match the glob or not; tests lower them.
var maxSearched, maxWalked = 10000, 100000

// open opens a file for reading; tests replace it to get in the way.
var open = func(path string) (io.ReadCloser, error) { return os.Open(path) }

// pathAbout is what every tool says about a path. Where a tool may go is
// told in the prompt of the agent, once for all of them.
const pathAbout = `absolute or starting with "~", the home directory`

// Plugin contributes the tools for files, which ask g for every path, and
// what the owner is shown of their calls.
func Plugin(g *guard.Guard) plugin.Plugin {
	return plugin.New("files", func(ctx *plugin.Context, _ struct{}) error {
		for _, tool := range tools(g) {
			plugin.Add(ctx, tool)
		}
		for name, doing := range map[string]string{Read: "reading", Write: "writing", Edit: "editing", List: "listing", Delete: "deleting", Search: "searching"} {
			plugin.Add(ctx, plugin.ToolView{Tool: name, Show: func(args json.RawMessage) string {
				var call struct {
					Path string `json:"path"`
				}
				if json.Unmarshal(args, &call) != nil || call.Path == "" {
					return ""
				}
				return doing + " " + call.Path
			}, Detail: assistant.HowItEnded})
		}
		return nil
	})
}

// tools are the six tools.
func tools(g *guard.Guard) []plugin.Tool {
	path := param{"path", "string", "the file, " + pathAbout, false}
	return []plugin.Tool{
		tool(g, Read,
			fmt.Sprintf("Read a text file: at most %d lines or %d KiB per call. ", maxReadLines, maxReadBytes>>10)+
				"When there is more, a note at the end says which lines of how many it returned and where to continue. "+
				"Leave out limit unless you want only a part of the file you know: every piece costs a call.",
			[]param{path,
				{"offset", "integer", "the first line, counted from 1", true},
				{"limit", "integer", "how many lines at most", true}},
			without(read)),
		tool(g, Write,
			fmt.Sprintf("Create a file or replace all of its content; missing directories are created. At most %d MiB. ", maxWriteBytes>>20)+
				"To change a part of a file use "+Edit+".",
			[]param{path, {"content", "string", "the whole content of the file", false}},
			without(write)),
		tool(g, Edit,
			`Replace the text "old" in a file by "new". "old" has to match exactly once: take enough of the text around the change. `+
				"Returns the changed lines with a few around them: check them before the next edit.",
			[]param{path,
				{"old", "string", "the text to replace, exactly as in the file", false},
				{"new", "string", "the text to put in its place", false}},
			without(edit)),
		tool(g, List,
			fmt.Sprintf("List a directory: directories end in /, files come with their size. At most %d entries; what the tool may not read is left out.", maxListed),
			[]param{{"path", "string", "the directory, " + pathAbout, false}},
			without(list)),
		tool(g, Delete,
			"Delete a file or an empty directory, or with recursive a directory and everything in it.",
			[]param{{"path", "string", "the file or directory, " + pathAbout, false},
				{"recursive", "boolean", "delete a directory with its content", true}},
			without(remove)),
		tool(g, Search,
			"Find the lines of text files that match a regular expression (RE2; (?i) at its start ignores case), "+
				"in one file or every file below a directory, hidden directories left out. "+
				fmt.Sprintf("Returns path:line: text, at most %d lines from at most %d files of at most %d it looks at; files over %d MiB are skipped. ", maxMatches, maxSearched, maxWalked, maxWriteBytes>>20)+
				"Search first to find where something is, then read only the lines around it.",
			[]param{{"path", "string", "the file or directory, " + pathAbout, false},
				{"pattern", "string", "the regular expression a line has to match", false},
				{"glob", "string", `only files whose name matches this, like "*.go"`, true}},
			search),
	}
}

// settings are what the owner can say about one of the tools in the
// configuration.
type settings struct {
	// Allow are the paths the tool may go to. Missing is everything for the
	// owner and nothing for a peer.
	Allow *[]string `json:"allow"`
	// Deny are paths within those the tool does not go to.
	Deny []string `json:"deny"`
}

// param is an argument of a tool.
type param struct {
	name, kind, about string
	optional          bool
}

// tool returns the tool called name, which takes params and runs run with
// what decides about paths for the conversation the call is made in.
func tool[T any](g *guard.Guard, name, description string, params []param, run func(ctx context.Context, c *guard.Checker, args T) (string, error)) plugin.Tool {
	return plugin.Configurable(plugin.Tool{Name: name, Description: description, Schema: schema(params)},
		func(s settings) (plugin.RunFunc, error) {
			if s.Allow != nil {
				if err := guard.Valid(*s.Allow); err != nil {
					return nil, err
				}
			}
			if err := guard.Valid(s.Deny); err != nil {
				return nil, err
			}
			return func(ctx context.Context, raw json.RawMessage) (string, error) {
				var args T
				if err := json.Unmarshal(raw, &args); err != nil {
					return "", fmt.Errorf("%s: invalid arguments", name)
				}
				_, peer := plugin.OriginFrom(ctx)
				c, err := g.Checker(guard.Access{Tool: name, Allow: s.Allow, Deny: s.Deny, Peer: peer})
				if err != nil {
					return "", err
				}
				return run(ctx, c, args)
			}, nil
		})
}

// without makes run, which ends soon on its own, a tool that is given the
// context of its call.
func without[T any](run func(c *guard.Checker, args T) (string, error)) func(context.Context, *guard.Checker, T) (string, error) {
	return func(_ context.Context, c *guard.Checker, args T) (string, error) { return run(c, args) }
}

// schema is the JSON schema of an object with the properties params.
func schema(params []param) string {
	type property struct {
		Type        string `json:"type"`
		Description string `json:"description"`
	}
	object := struct {
		Type       string              `json:"type"`
		Properties map[string]property `json:"properties"`
		Required   []string            `json:"required"`
	}{Type: "object", Properties: map[string]property{}}
	for _, p := range params {
		object.Properties[p.name] = property{p.kind, p.about}
		if !p.optional {
			object.Required = append(object.Required, p.name)
		}
	}
	b, _ := json.Marshal(object) // strings only: cannot fail
	return string(b)
}

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func read(c *guard.Checker, args readArgs) (string, error) {
	real, err := c.Check(guard.Read, args.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	// Only a file: a pipe or a device would never end.
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a file; a directory is read with %s", args.Path, List)
	}
	f, err := open(real)
	if err != nil {
		return "", err
	}
	defer f.Close()

	first, limit := max(args.Offset, 1), args.Limit
	if limit < 1 || limit > maxReadLines {
		limit = maxReadLines
	}
	var text strings.Builder
	lines, taken, more := 0, 0, false
	r := bufio.NewReaderSize(f, maxReadBytes)
	for !more {
		// The lines after those returned are left to countLines: one of
		// them may be too long, or no text, and is not returned anyway.
		if taken == limit {
			_, err := r.Peek(1)
			more = !errors.Is(err, io.EOF)
			break
		}
		line, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return "", fmt.Errorf("line %d of %s has %d KiB or more: too long to read", lines+1, args.Path, maxReadBytes>>10)
		}
		if len(line) > 0 {
			lines++
			if !utf8.Valid(line) || bytes.IndexByte(line, 0) >= 0 {
				return "", fmt.Errorf("%s is not a text file", args.Path)
			}
			if lines >= first {
				if text.Len()+len(line) > maxReadBytes {
					more = true
				} else {
					text.Write(line)
					taken++
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	if lines == 0 {
		return "(the file is empty)", nil
	}
	if taken == 0 {
		return "", fmt.Errorf("%s has only %d lines", args.Path, lines)
	}
	content, last := strings.TrimSuffix(text.String(), "\n"), first+taken-1
	if more {
		// The lines after those returned are only counted, for the note.
		rest, err := countLines(r)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s\n[these were the lines %d to %d of %d: continue with offset %d]",
			content, first, last, lines+rest, last+1), nil
	}
	// The whole file comes as it is: a script of run may take it for data.
	if first == 1 {
		return text.String(), nil
	}
	return fmt.Sprintf("%s\n[these were the lines %d to %d of %d, the end of the file]", content, first, last, lines), nil
}

// countLines counts the lines r has left, the last one also without a line
// break at its end.
func countLines(r io.Reader) (int, error) {
	buf := make([]byte, 32<<10)
	n, last := 0, byte('\n')
	for {
		k, err := r.Read(buf)
		if k > 0 {
			n += bytes.Count(buf[:k], []byte{'\n'})
			last = buf[k-1]
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, err
		}
	}
	if last != '\n' {
		n++
	}
	return n, nil
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func write(c *guard.Checker, args writeArgs) (string, error) {
	if len(args.Content) > maxWriteBytes {
		return "", fmt.Errorf("the content has %d bytes: a file is written with at most %d", len(args.Content), maxWriteBytes)
	}
	real, err := c.Check(guard.Write, args.Path)
	if err != nil {
		return "", err
	}
	// A file that is replaced keeps its mode.
	done, perm := "created", fs.FileMode(0o644)
	if info, err := os.Stat(real); err == nil {
		done, perm = "replaced", info.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(real), 0o755); err != nil {
		return "", err
	}
	if err := safefile.Write(real, []byte(args.Content), perm); err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %s (%d bytes)", done, args.Path, len(args.Content)), nil
}

type editArgs struct {
	Path string `json:"path"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

func edit(c *guard.Checker, args editArgs) (string, error) {
	if args.Old == "" {
		return "", errors.New(Edit + ` needs "old", the text to replace`)
	}
	real, err := c.Check(guard.Write, args.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(real)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a file", args.Path)
	}
	if info.Size() > maxWriteBytes {
		return "", fmt.Errorf("%s has %d bytes: a file is edited with at most %d", args.Path, info.Size(), maxWriteBytes)
	}
	content, err := os.ReadFile(real)
	if err != nil {
		return "", err
	}
	n := strings.Count(string(content), args.Old)
	if n == 0 {
		return "", fmt.Errorf(`"old" does not occur in %s: it has to match the text of the file exactly`, args.Path)
	}
	if n > 1 {
		return "", fmt.Errorf(`"old" occurs %d times in %s: it has to match exactly once, so take more of the text around it`, n, args.Path)
	}
	at := strings.Index(string(content), args.Old)
	edited := string(content[:at]) + args.New + string(content[at+len(args.Old):])
	if len(edited) > maxWriteBytes {
		return "", fmt.Errorf("%s would have %d bytes: a file is written with at most %d", args.Path, len(edited), maxWriteBytes)
	}
	if err := safefile.Write(real, []byte(edited), info.Mode().Perm()); err != nil {
		return "", err
	}
	return changed(args.Path, edited, at, args.New), nil
}

// changed tells what an edit left behind: the lines of edited that put, put
// in at the byte at, takes now, with editContext lines around them, so that
// the LLM sees what it did without reading the file again.
func changed(path, edited string, at int, put string) string {
	lines := strings.SplitAfter(edited, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return "edited " + path + ", which is empty now"
	}
	first := min(strings.Count(edited[:at], "\n"), len(lines)-1)
	last := first + strings.Count(strings.TrimSuffix(put, "\n"), "\n")
	from, to := max(0, first-editContext), min(len(lines)-1, last+editContext)
	more := ""
	if to-from >= maxEditLines {
		to = from + maxEditLines - 1
		more = fmt.Sprintf("\n[and more: continue with %s at offset %d]", Read, to+2)
	}
	return fmt.Sprintf("edited %s; lines %d to %d of %d are now:\n%s%s", path, from+1, to+1, len(lines),
		strings.TrimSuffix(strings.Join(lines[from:to+1], ""), "\n"), more)
}

type listArgs struct {
	Path string `json:"path"`
}

func list(c *guard.Checker, args listArgs) (string, error) {
	real, err := c.Check(guard.Read, args.Path)
	if err != nil {
		return "", err
	}
	entries, err := os.ReadDir(real)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, e := range entries {
		// What the tool may not read is not there for it.
		if _, err := c.Check(guard.Read, filepath.Join(real, e.Name())); err != nil {
			continue
		}
		if len(lines) == maxListed {
			lines = append(lines, fmt.Sprintf("[these are the first %d entries, and there are more]", maxListed))
			break
		}
		line := e.Name()
		if info, _ := e.Info(); e.IsDir() {
			line += "/"
		} else if info != nil && info.Mode().IsRegular() {
			line += fmt.Sprintf(" (%d bytes)", info.Size())
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return "(nothing to list)", nil
	}
	return strings.Join(lines, "\n"), nil
}

type deleteArgs struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

func remove(c *guard.Checker, args deleteArgs) (string, error) {
	real, err := c.Check(guard.Delete, args.Path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(real)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		entries, err := os.ReadDir(real)
		if err != nil {
			return "", err
		}
		if len(entries) > 0 && !args.Recursive {
			return "", fmt.Errorf("%s is a directory with content: it is only deleted, with everything in it, when recursive is true", args.Path)
		}
		// Everything below is asked about before anything is deleted: what
		// may not be deleted keeps all of it.
		err = filepath.WalkDir(real, func(path string, _ fs.DirEntry, err error) error {
			if err == nil {
				_, err = c.Check(guard.Delete, path)
			}
			return err
		})
		if err != nil {
			return "", fmt.Errorf("nothing was deleted: %w", err)
		}
	}
	if err := os.RemoveAll(real); err != nil {
		return "", err
	}
	return "deleted " + args.Path, nil
}

type searchArgs struct {
	Path    string `json:"path"`
	Pattern string `json:"pattern"`
	Glob    string `json:"glob"`
}

// errSearched ends a search that looked into maxSearched files, and
// errWalked one that looked at maxWalked files and directories.
var errSearched, errWalked = errors.New("searched enough"), errors.New("walked enough")

// search ends when ctx does, as a search of a big tree takes long.
func search(ctx context.Context, c *guard.Checker, args searchArgs) (string, error) {
	if args.Pattern == "" {
		return "", errors.New(Search + ` needs "pattern", the regular expression to find`)
	}
	re, err := regexp.Compile(args.Pattern)
	if err != nil {
		return "", fmt.Errorf("the pattern is no regular expression: %w", err)
	}
	if _, err := filepath.Match(args.Glob, ""); err != nil {
		return "", fmt.Errorf("the glob %q is no pattern of file names", args.Glob)
	}
	root, err := c.Check(guard.Read, args.Path)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(root); err != nil {
		return "", err
	}
	var lines []string
	searched, walked := 0, 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walked == maxWalked {
			return errWalked
		}
		walked++
		if err != nil {
			if path == root {
				return err
			}
			return nil // what cannot be looked into is not searched
		}
		// What the tool may not read is not there for it.
		real, err := c.Check(guard.Read, path)
		if d.IsDir() {
			if path != root && (err != nil || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if err != nil || (args.Glob != "" && !matches(args.Glob, d.Name())) {
			return nil
		}
		if searched == maxSearched {
			return errSearched
		}
		searched++
		shown, _ := filepath.Rel(root, path)
		found, err := searchFile(real, filepath.Join(args.Path, shown), re, maxMatches+1-len(lines))
		if err != nil {
			return nil // a file that is no text has no lines to match
		}
		lines = append(lines, found...)
		if len(lines) > maxMatches {
			lines = append(lines[:maxMatches], fmt.Sprintf("[these are the first %d matching lines, and there are more: "+
				"narrow the path, the pattern or the glob]", maxMatches))
			return filepath.SkipAll
		}
		return nil
	})
	switch {
	case errors.Is(err, errSearched):
		lines = append(lines, fmt.Sprintf("[the search stopped after %d files: narrow the path or the glob]", maxSearched))
	case errors.Is(err, errWalked):
		lines = append(lines, fmt.Sprintf("[the search stopped after looking at %d files and directories: narrow the path]", maxWalked))
	case ctx.Err() != nil:
		return "", fmt.Errorf("the search was stopped after looking at %d files and directories: %w", walked, ctx.Err())
	case err != nil:
		return "", err
	}
	if len(lines) == 0 {
		return "(no line matches)", nil
	}
	return strings.Join(lines, "\n"), nil
}

// matches reports whether the name of a file matches glob, which is valid.
func matches(glob, name string) bool {
	ok, _ := filepath.Match(glob, name)
	return ok
}

// searchFile returns at most limit lines of the file at real that match re,
// as shown:line: text. A file that is no text, or too big to be read with
// read, is an error.
func searchFile(real, shown string, re *regexp.Regexp, limit int) ([]string, error) {
	info, err := os.Stat(real)
	if err != nil {
		return nil, err
	}
	// Only a file: a pipe or a device would never end.
	if !info.Mode().IsRegular() || info.Size() > maxWriteBytes {
		return nil, fmt.Errorf("%s is not searched", shown)
	}
	f, err := open(real)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	content, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, fmt.Errorf("%s is not a text file", shown)
	}
	if len(content) == 0 {
		return nil, nil
	}
	var found []string
	text, _ := strings.CutSuffix(string(content), "\n")
	for n, line := range strings.Split(text, "\n") {
		if len(found) == limit {
			break
		}
		if !re.MatchString(line) {
			continue
		}
		line = strings.TrimSuffix(line, "\r")
		if len(line) > maxMatchBytes {
			line = strings.ToValidUTF8(line[:maxMatchBytes], "") + "…"
		}
		found = append(found, fmt.Sprintf("%s:%d: %s", shown, n+1, line))
	}
	return found, nil
}

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
	"slices"
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
	maxContext    = 30        // lines search shows before and after a match at most
	maxPlaces     = 30        // places with their lines around them one call of search returns
	maxAround     = 12 << 10  // bytes of search with context: the places beyond show only their matches
)

// maxSearched is how many files one call of search looks into, and
// maxWalked how many files and directories it looks at, whether their names
// match the glob or not; tests lower them.
var maxSearched, maxWalked = 10000, 100000

// open opens a file for reading; tests replace it to get in the way.
var open = func(path string) (io.ReadCloser, error) { return os.Open(path) }

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
	path := param{"path", "string", "the file", false}
	return []plugin.Tool{
		tool(g, Read,
			fmt.Sprintf("Read a text file: at most %d lines or %d KiB per call. ", maxReadLines, maxReadBytes>>10)+
				"When there is more, a note at the end says which lines of how many it returned and where to continue. "+
				"Leave out limit unless you want only a part of the file you know: every piece costs a call. "+
				"A picture (PNG, JPEG, GIF or WebP) is shown to you as an image, in its own size: look at it this way. "+
				"With size and region you see it smaller, or only a part of it.",
			[]param{path,
				{"offset", "integer", "the first line, counted from 1", true},
				{"limit", "integer", "how many lines at most", true},
				{"size", "integer", "of a picture: the longer side in pixels it is shown with at most; leave out for what the effort allows", true},
				{"region", "string", `of a picture: only the part "x,y,width,height", in its own pixels`, true}},
			read),
		tool(g, Write,
			fmt.Sprintf("Create a file or replace all of its content; missing directories are created. At most %d MiB. ", maxWriteBytes>>20)+
				"To change a part of a file use "+Edit+".",
			[]param{path, {"content", "string", "the whole content of the file", false}},
			without(write)),
		tool(g, Edit,
			`Replace the text "old" in a file by "new". "old" has to match exactly once: take enough of the text around the change; `+
				`with "all" every time it occurs, like a text that is shown and the tests that expect it. `+
				"In a script, write old and new as String.raw`…`: a backslash of the file, as in \"\\n\", then stays one; "+
				"a text with a backtick (Markdown, a struct tag of Go) goes in a string of quotes instead, with \\\\ for each backslash. "+
				"Or replace the lines from to to by new, with no old to match: of several in one file, do the last first, as the lines after an edit move. "+
				"Returns the changed lines with a few around them: check them before the next edit.",
			[]param{path,
				{"old", "string", "the text to replace, exactly as in the file", false},
				{"new", "string", "the text to put in its place", false},
				{"all", "boolean", "replace every time old occurs, not only once", true},
				{"from", "integer", "instead of old: the first line to replace, as read and search number them", true},
				{"to", "integer", "with from: the last line to replace", true}},
			without(edit)),
		tool(g, List,
			fmt.Sprintf("List a directory: directories end in /, files come with their size. At most %d entries; what the tool may not read is left out.", maxListed),
			[]param{{"path", "string", "the directory", false}},
			without(list)),
		tool(g, Delete,
			"Delete a file or an empty directory, or with recursive a directory and everything in it.",
			[]param{{"path", "string", "the file or directory", false},
				{"recursive", "boolean", "delete a directory with its content", true}},
			without(remove)),
		tool(g, Search,
			"Find the lines of text files that match a regular expression (RE2; (?i) at its start ignores case), "+
				"in one file or every file below a directory, hidden directories left out. "+
				fmt.Sprintf("Returns path:line: text, at most %d lines from at most %d files of at most %d it looks at; files over %d MiB are skipped. ", maxMatches, maxSearched, maxWalked, maxWriteBytes>>20)+
				"Search first to find where something is, then read that file, whole unless it is very long: every piece costs a call; "+
				fmt.Sprintf("with context it returns them too, as grep -C: the lines around a match as path-line- text, places apart split by --, at most %d places. ", maxPlaces)+
				fmt.Sprintf("Of what is beyond %d KiB it says only how many matches are in which file: search there with a narrower pattern. ", maxAround>>10),
			[]param{{"path", "string", "the file or directory", false},
				{"pattern", "string", "the regular expression a line has to match", false},
				{"glob", "string", `only files whose name matches this, like "*.go"`, true},
				{"context", "integer", fmt.Sprintf("how many lines before and after every match to show too, at most %d", maxContext), true}},
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
				if err := plugin.Decode(name, raw, &args); err != nil {
					return "", err
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
	// Of a picture.
	Size   int    `json:"size"`
	Region string `json:"region"`
}

func read(ctx context.Context, c *guard.Checker, args readArgs) (string, error) {
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
	if isPicture(real) {
		return readPicture(ctx, f, args, info.Size())
	}

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
	All  bool   `json:"all"`
	From int    `json:"from"`
	To   int    `json:"to"`
}

func edit(c *guard.Checker, args editArgs) (string, error) {
	byLines := args.From != 0 || args.To != 0
	switch {
	case byLines && (args.Old != "" || args.All):
		return "", errors.New(Edit + ` takes "old" or "from" and "to", not both`)
	case !byLines && args.Old == "":
		return "", errors.New(Edit + ` needs "old", the text to replace, or "from" and "to", the lines to replace`)
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
	if byLines {
		at, end, err := lineSpan(string(content), args.From, args.To)
		if err != nil {
			return "", fmt.Errorf("%s: %w", args.Path, err)
		}
		put := args.New
		if put != "" && !strings.HasSuffix(put, "\n") && strings.HasSuffix(string(content[at:end]), "\n") {
			put += "\n"
		}
		args.Old, args.New = string(content[at:end]), put
	}
	n := strings.Count(string(content), args.Old)
	if n == 0 {
		return "", fmt.Errorf(`"old" does not occur in %s: it has to match the text of the file exactly%s`, args.Path, mismatch(string(content), args.Old))
	}
	if n > 1 && !args.All && !byLines {
		return "", fmt.Errorf(`"old" occurs %d times in %s: it has to match exactly once, so take more of the text around it, or replace it every time with "all"`, n, args.Path)
	}
	at := strings.Index(string(content), args.Old)
	if byLines {
		// The lines asked for, though their text may occur elsewhere too.
		at, _, _ = lineSpan(string(content), args.From, args.To)
		n = 1
	}
	edited := string(content[:at]) + args.New + string(content[at+len(args.Old):])
	if args.All {
		edited = strings.ReplaceAll(string(content), args.Old, args.New)
	}
	if len(edited) > maxWriteBytes {
		return "", fmt.Errorf("%s would have %d bytes: a file is written with at most %d", args.Path, len(edited), maxWriteBytes)
	}
	if err := safefile.Write(real, []byte(edited), info.Mode().Perm()); err != nil {
		return "", err
	}
	if n > 1 {
		return replaced(args.Path, string(content), args.Old, args.New), nil
	}
	return changed(args.Path, edited, at, args.New), nil
}

// mismatch says where old, which does not occur in content, parts from it:
// the line of content where most of the lines of old match in a row, and the
// first one that does not, so that the LLM can fix old without reading the
// file again.
func mismatch(content, old string) string {
	lines := strings.Split(content, "\n")
	want := strings.Split(old, "\n")
	best, from := 0, -1
	for i := range lines {
		n := 0
		for n < len(want) && i+n < len(lines) && strings.TrimSpace(lines[i+n]) == strings.TrimSpace(want[n]) {
			n++
		}
		if n > best {
			best, from = n, i
		}
	}
	if from < 0 {
		return "; not even its first line is in the file"
	}
	at := from + best
	if best == len(want) {
		return fmt.Sprintf("; its lines are lines %d to %d of the file, but not with the same spaces and tabs at their start or end: copy them from there", from+1, at)
	}
	file := "(the end of the file)"
	if at < len(lines) {
		file = fmt.Sprintf("%q", lines[at])
	}
	hint := ""
	if strings.Contains(file, `\\`) {
		hint = `; in a string of JavaScript a backslash starts an escape, so write \\ for each one, and \\n for the two characters \n`
	}
	return fmt.Sprintf("; %d of its lines match from line %d of the file on, then line %d of the file is %s, where \"old\" has %q%s",
		best, from+1, at+1, file, want[best], hint)
}

// lineSpan returns where the lines from to to of content are, as numbers
// of bytes: from the start of the first to the end of the last, its line
// break included.
func lineSpan(content string, from, to int) (at, end int, err error) {
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if from < 1 || to < from || to > len(lines) {
		return 0, 0, fmt.Errorf("the lines %d to %d are not lines of the file, which has %d: from and to count from 1, and to is not before from", from, to, len(lines))
	}
	for _, line := range lines[:from-1] {
		at += len(line)
	}
	end = at
	for _, line := range lines[from-1 : to] {
		end += len(line)
	}
	return at, end, nil
}

// replaced tells what an edit of every old in content left behind: the
// lines it changed, as they are now.
func replaced(path, content, old, put string) string {
	var lines []string
	more := strings.Count(put, "\n") - strings.Count(old, "\n") // lines each place adds
	for from := 0; strings.Contains(content[from:], old); {
		at := from + strings.Index(content[from:], old)
		lines = append(lines, fmt.Sprint(strings.Count(content[:at], "\n")+more*len(lines)+1))
		from = at + len(old)
	}
	return fmt.Sprintf("edited %s: replaced %d times, in the lines %s", path, len(lines), strings.Join(lines, ", "))
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
	Context int    `json:"context"`
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
	if args.Context < 0 || args.Context > maxContext {
		return "", fmt.Errorf("context is a number of lines from 0 to %d", maxContext)
	}
	// What one call returns at most: matching lines, or places with lines
	// around them.
	limit, what := maxMatches, "matching lines"
	if args.Context > 0 {
		limit, what = maxPlaces, "places"
	}
	root, err := c.Check(guard.Read, args.Path)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(root); err != nil {
		return "", err
	}
	var lines, hits []string // hits: the matching lines of each place, with context
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
		found, matched, err := searchFile(real, filepath.Join(args.Path, shown), re, limit+1-len(lines), args.Context)
		if err != nil {
			return nil // a file that is no text has no lines to match
		}
		lines, hits = append(lines, found...), append(hits, matched...)
		if len(lines) > limit {
			hits = hits[:min(len(hits), limit)]
			lines = append(lines[:limit], fmt.Sprintf("[these are the first %d %s, and there are more: "+
				"narrow the path, the pattern or the glob]", limit, what))
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
	if args.Context > 0 {
		return fit(lines, hits, "\n--\n"), nil
	}
	// Every line is a match, up to the notes of the search, which start
	// with [ where a path cannot.
	notes := slices.IndexFunc(lines, func(line string) bool { return strings.HasPrefix(line, "[") })
	if notes < 0 {
		notes = len(lines)
	}
	return fit(lines, lines[:notes], "\n"), nil
}

// fit joins what a search found, split by sep, as far as it fits into
// maxAround bytes: the first one always. Of the matches beyond, it says how
// many there are in which file, so that the LLM knows where else to look.
// matched are the matching lines of each block of found, one per line; the
// blocks after them are the notes of the search, which end it.
func fit(found, matched []string, sep string) string {
	var out []string
	var files []string         // with matches beyond, in the order found
	beyond := map[string]int{} // matches beyond, by file
	size := 0
	for i, block := range found {
		if i < len(matched) && (len(files) > 0 || size+len(block) > maxAround && i > 0) {
			for _, line := range strings.Split(matched[i], "\n") {
				file := line[:matchAt.FindStringIndex(line)[0]]
				if beyond[file] == 0 {
					files = append(files, file)
				}
				beyond[file]++
			}
			continue
		}
		out = append(out, block)
		size += len(block)
	}
	if len(files) > 0 {
		var where []string
		n := 0
		for _, file := range files {
			where = append(where, fmt.Sprintf("%s (%d)", file, beyond[file]))
			n += beyond[file]
		}
		out = append(out, fmt.Sprintf("[%d more matching lines, beyond what one search shows, in %s: search there with a narrower pattern]", n, strings.Join(where, ", ")))
	}
	return strings.Join(out, sep)
}

// matchAt finds where the path of a matching line of search ends: at
// :line: .
var matchAt = regexp.MustCompile(`:[0-9]+: `)

// matches reports whether the name of a file matches glob, which is valid.
func matches(glob, name string) bool {
	ok, _ := filepath.Match(glob, name)
	return ok
}

// searchFile returns at most limit lines of the file at real that match re,
// as shown:line: text, or with context that many lines around each, as
// shown-line- text, at most limit places of lines that touch, each one
// string. A file that is no text, or too big to be read with read, is an
// error.
func searchFile(real, shown string, re *regexp.Regexp, limit, context int) (found, hits []string, err error) {
	info, err := os.Stat(real)
	if err != nil {
		return nil, nil, err
	}
	// Only a file: a pipe or a device would never end.
	if !info.Mode().IsRegular() || info.Size() > maxWriteBytes {
		return nil, nil, fmt.Errorf("%s is not searched", shown)
	}
	f, err := open(real)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	content, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, err
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, nil, fmt.Errorf("%s is not a text file", shown)
	}
	if len(content) == 0 {
		return nil, nil, nil
	}
	text, _ := strings.CutSuffix(string(content), "\n")
	all := strings.Split(text, "\n")
	show := func(n int, mark string) string {
		line := strings.TrimSuffix(all[n], "\r")
		if len(line) > maxMatchBytes {
			line = strings.ToValidUTF8(line[:maxMatchBytes], "") + "…"
		}
		return fmt.Sprintf("%s%s%d%s %s", shown, mark, n+1, mark, line)
	}
	end := -1 // the last line of the place found last
	for n, line := range all {
		if !re.MatchString(line) {
			continue
		}
		if context == 0 {
			if len(found) == limit {
				break
			}
			found = append(found, show(n, ":"))
			continue
		}
		from, to := max(0, n-context, end+1), min(len(all)-1, n+context)
		if end < 0 || from > end+1 {
			if len(found) == limit {
				break
			}
			found, hits = append(found, ""), append(hits, "")
		}
		for i := from; i <= to; i++ {
			mark := "-"
			if re.MatchString(all[i]) {
				mark = ":"
			}
			if i > from || found[len(found)-1] != "" {
				found[len(found)-1] += "\n"
			}
			found[len(found)-1] += show(i, mark)
		}
		if hits[len(hits)-1] != "" {
			hits[len(hits)-1] += "\n"
		}
		hits[len(hits)-1] += show(n, ":")
		end = max(end, to)
	}
	return found, hits, nil
}

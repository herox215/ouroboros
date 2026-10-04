// Package files is the plugin with the tools for files: read, write, edit,
// list and delete. Where each of them may go is for the owner to say, per
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
	"strings"
	"unicode/utf8"

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
)

// The limits of the tools.
const (
	maxReadLines  = 2000      // lines one call of read returns
	maxReadBytes  = 256 << 10 // bytes one call of read returns
	maxWriteBytes = 1 << 20   // bytes of a file that write or edit leave behind
	maxListed     = 1000      // entries one call of list returns
)

// open opens a file for reading; tests replace it to get in the way.
var open = func(path string) (io.ReadCloser, error) { return os.Open(path) }

// pathHint is what every tool says about its paths.
const pathHint = `The path is absolute or starts with "~", the home directory. ` +
	`Where the tool may go is set by the owner of the node: a refusal names the rule, and there is no way around it.`

// Plugin contributes the tools for files, which ask g for every path, and
// what the owner is shown of their calls.
func Plugin(g *guard.Guard) plugin.Plugin {
	return plugin.New("files", func(ctx *plugin.Context, _ struct{}) error {
		for _, tool := range tools(g) {
			plugin.Add(ctx, tool)
		}
		for name, doing := range map[string]string{Read: "reading", Write: "writing", Edit: "editing", List: "listing", Delete: "deleting"} {
			plugin.Add(ctx, plugin.ToolView{Tool: name, Show: func(args json.RawMessage) string {
				var call struct {
					Path string `json:"path"`
				}
				if json.Unmarshal(args, &call) != nil || call.Path == "" {
					return ""
				}
				return doing + " " + call.Path
			}})
		}
		return nil
	})
}

// tools are the five tools.
func tools(g *guard.Guard) []plugin.Tool {
	path := param{"path", "string", "the file", false}
	return []plugin.Tool{
		tool(g, Read,
			fmt.Sprintf("Read a text file and return its content: at most %d lines or %d KiB per call. ", maxReadLines, maxReadBytes>>10)+
				"When there is more, the result ends with a note that says at which line to continue. "+pathHint,
			[]param{path,
				{"offset", "integer", "the line to start at, counted from 1; the first one if missing", true},
				{"limit", "integer", "how many lines to return at most", true}},
			read),
		tool(g, Write,
			fmt.Sprintf("Create a file with the given content, or replace all of its content. Missing directories are created. At most %d MiB. ", maxWriteBytes>>20)+
				"To change a part of a file use "+Edit+". "+pathHint,
			[]param{path, {"content", "string", "the whole content of the file", false}},
			write),
		tool(g, Edit,
			`Replace the text "old" in a file by "new". "old" has to match the text of the file exactly, and exactly once: `+
				"take enough of the text around the change to make it so. "+pathHint,
			[]param{path,
				{"old", "string", "the text to replace, exactly as it is in the file", false},
				{"new", "string", "the text to put in its place", false}},
			edit),
		tool(g, List,
			"List the entries of a directory: directories end in /, files come with their size. "+
				fmt.Sprintf("At most %d entries; what the tool may not read is not listed. ", maxListed)+pathHint,
			[]param{{"path", "string", "the directory", false}},
			list),
		tool(g, Delete,
			"Delete a file or an empty directory. A directory with content is only deleted with everything in it when recursive is true. "+pathHint,
			[]param{{"path", "string", "the file or directory", false},
				{"recursive", "boolean", "delete a directory together with its content", true}},
			remove),
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
func tool[T any](g *guard.Guard, name, description string, params []param, run func(c *guard.Checker, args T) (string, error)) plugin.Tool {
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
				return run(c, args)
			}, nil
		})
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
	for r := bufio.NewReaderSize(f, maxReadBytes); !more; {
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
				if taken == limit || text.Len()+len(line) > maxReadBytes {
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
	if more {
		last := first + taken - 1
		return fmt.Sprintf("%s\n[these were the lines %d to %d, and there are more: continue with offset %d]",
			strings.TrimSuffix(text.String(), "\n"), first, last, last+1), nil
	}
	return text.String(), nil
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
	if err == nil {
		err = c.Keeps(real, []byte(args.Content))
	}
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
	edited := strings.Replace(string(content), args.Old, args.New, 1)
	if len(edited) > maxWriteBytes {
		return "", fmt.Errorf("%s would have %d bytes: a file is written with at most %d", args.Path, len(edited), maxWriteBytes)
	}
	if err := c.Keeps(real, []byte(edited)); err != nil {
		return "", err
	}
	if err := safefile.Write(real, []byte(edited), info.Mode().Perm()); err != nil {
		return "", err
	}
	return "edited " + args.Path, nil
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

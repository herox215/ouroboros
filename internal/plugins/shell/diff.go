package shell

import (
	"bytes"
	"fmt"
	"regexp"
	"strings"
)

// diffFiles recognizes rustfmt and unified diff headers across pipe writes.
// Both the pending line and the file list are bounded, even for binary output.
// This is a best-effort index, not a diff parser; the log remains authoritative.
type diffFiles struct {
	line    []byte
	discard bool
	paths   []string
	more    bool
	old     string // a unified old-file header, pending its new-file header
}

const maxDiffFiles = 20

var rustfmtHeader = regexp.MustCompile(`^Diff in (.+):[0-9]+:$`)

func (d *diffFiles) write(b []byte) {
	for _, c := range b {
		if c == '\n' {
			if !d.discard {
				d.header(string(d.line))
			}
			d.line = d.line[:0]
			d.discard = false
		} else if !d.discard {
			if len(d.line) == 4096 {
				d.old = ""
				d.line = d.line[:0]
				d.discard = true
			} else {
				d.line = append(d.line, c)
			}
		}
	}
}

func (d *diffFiles) close() {
	if !d.discard && len(d.line) > 0 {
		d.header(string(d.line))
	}
	d.line = nil
}

func (d *diffFiles) header(line string) {
	line = strings.TrimSuffix(line, "\r")
	old := d.old
	d.old = ""
	if m := rustfmtHeader.FindStringSubmatch(line); m != nil {
		d.add(m[1])
	} else if strings.HasPrefix(line, "--- ") {
		d.old, _, _ = strings.Cut(line[4:], "\t")
	} else if strings.HasPrefix(line, "+++ ") && old != "" {
		path, _, _ := strings.Cut(line[4:], "\t")
		for _, path := range []string{old, path} {
			if path == "/dev/null" {
				continue
			}
			if strings.HasPrefix(path, "a/") || strings.HasPrefix(path, "b/") {
				path = path[2:]
			}
			d.add(path)
		}
	}
}

func (d *diffFiles) add(path string) {
	if path == "" {
		return
	}
	for _, old := range d.paths {
		if old == path {
			return
		}
	}
	if len(d.paths) == maxDiffFiles {
		d.more = true
		return
	}
	d.paths = append(d.paths, path)
}

func (d *diffFiles) summary() string {
	if len(d.paths) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[diff files (detected headers):")
	more := d.more
	for _, path := range d.paths {
		if len(path) > 240 {
			path = string(bytes.ToValidUTF8([]byte(path[:240]), nil)) + "…"
		}
		quoted := fmt.Sprintf(" %q", path)
		// Bound encoded bytes too: quoted control characters can expand.
		// Leave room for the omission note, log path and 2 KiB excerpt
		// within the script runner's tightened 4 KiB result allowance.
		if b.Len()+len(quoted) > 1400 {
			more = true
			break
		}
		b.WriteString(quoted)
	}
	if more {
		b.WriteString("; additional files omitted")
	}
	b.WriteString("]\n")
	return b.String()
}

// Keep diagnostics at both ends, but do not spend the whole generic output
// allowance on a saved diff. Small outputs and failed captures stay unchanged.
func diffExcerpt(out string) string {
	const half = 1024
	if len(out) <= 2*half {
		return out
	}
	head := bytes.ToValidUTF8([]byte(out[:half]), nil)
	tail := bytes.ToValidUTF8([]byte(out[len(out)-half:]), nil)
	return fmt.Sprintf("%s\n[… diff excerpt; middle omitted; see full log …]\n%s", head, tail)
}

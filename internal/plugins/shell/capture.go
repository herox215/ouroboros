package shell

import (
	"fmt"
	"os"
)

// capture saves a command's entire combined output once it exceeds MaxOutput.
// The small prefix is independent of output: looking at a running job must not
// discard bytes that a later log needs. The process mutex protects it.
type capture struct {
	prefix []byte
	file   *os.File
	err    error
	size   int64
	dir    string // empty for the system temporary directory; tests set it
	diff   diffFiles
}

func (c *capture) write(b []byte) {
	c.diff.write(b)
	c.size += int64(len(b))
	if c.err != nil {
		return
	}
	if c.file == nil {
		if c.size <= MaxOutput {
			c.prefix = append(c.prefix, b...)
			return
		}
		c.file, c.err = os.CreateTemp(c.dir, "ouro-bash-*.log")
		if c.err == nil {
			_, c.err = c.file.Write(c.prefix)
		}
		c.prefix = nil
	}
	if c.err == nil {
		_, c.err = c.file.Write(b)
	}
}

func (c *capture) close() {
	c.diff.close()
	if c.file != nil {
		if err := c.file.Close(); c.err == nil {
			c.err = err
		}
	}
	c.prefix = nil
}

// Put the path before the excerpt, so an outer script limit is less likely
// to hide the way to retrieve what it cuts. Never claim a failed log is full.
func (c *capture) summary() string {
	if c.err != nil {
		path := ""
		if c.file != nil {
			path = fmt.Sprintf("; incomplete log: %q", c.file.Name())
		}
		return fmt.Sprintf("[output: %d bytes; could not save full output: %v%s; bounded excerpt follows]\n", c.size, c.err, path)
	}
	if c.file != nil {
		return fmt.Sprintf("[output: %d bytes; full log: %q; bounded excerpt follows]\n", c.size, c.file.Name())
	}
	return ""
}

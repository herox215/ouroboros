package shell

import (
	"bytes"
	"context"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCapture(t *testing.T) {
	for _, size := range []int{0, MaxOutput, MaxOutput + 1, 10 * MaxOutput} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			c := capture{dir: t.TempDir()}
			want := bytes.Repeat([]byte{0, 255, '\n', 'a'}, size/4)
			want = append(want, bytes.Repeat([]byte{'z'}, size-len(want))...)
			// Different chunk sizes must not affect the saved bytes.
			for start := 0; start < len(want); {
				end := min(start+733, len(want))
				c.write(want[start:end])
				start = end
			}
			c.close()
			if c.err != nil || c.size != int64(size) {
				t.Fatalf("capture: %d, %v", c.size, c.err)
			}
			if size <= MaxOutput {
				files, err := os.ReadDir(c.dir)
				if err != nil || len(files) != 0 || c.summary() != "" {
					t.Fatalf("small output created a log: %v, %v, %q", files, err, c.summary())
				}
				return
			}
			got, err := os.ReadFile(c.file.Name())
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("log: %d bytes, want %d; %v", len(got), len(want), err)
			}
			info, err := os.Stat(c.file.Name())
			if err != nil || info.Mode().Perm() != 0o600 {
				t.Fatalf("private log: %v, %v", info, err)
			}
			if !strings.Contains(c.summary(), c.file.Name()) || !strings.Contains(c.summary(), "full log:") {
				t.Fatalf("summary: %s", c.summary())
			}
		})
	}
}

func TestCaptureFailure(t *testing.T) {
	t.Run("create", func(t *testing.T) {
		c := capture{dir: t.TempDir() + "/missing"}
		c.write(bytes.Repeat([]byte{'x'}, MaxOutput+1))
		c.write([]byte("still drained"))
		c.close()
		if c.err == nil || !strings.Contains(c.summary(), "could not save full output") || strings.Contains(c.summary(), "full log:") {
			t.Fatalf("failure: %s", c.summary())
		}
	})
	t.Run("write", func(t *testing.T) {
		c := capture{dir: t.TempDir()}
		c.write(bytes.Repeat([]byte{'x'}, MaxOutput+1))
		c.file.Close() // simulate a log that can no longer be written
		c.write([]byte("still drained"))
		c.close()
		if c.err == nil || !strings.Contains(c.summary(), "incomplete log:") || strings.Contains(c.summary(), "full log:") {
			t.Fatalf("failure: %s", c.summary())
		}
	})
}

// A background job's earlier takes must remain in the eventual full log.
func TestCaptureAcrossTakes(t *testing.T) {
	p := &process{out: &output{}, capture: capture{dir: t.TempDir()}}
	p.Write([]byte("earlier\n"))
	if got := p.take(); got != "earlier\n" {
		t.Fatal(got)
	}
	middle := strings.Repeat("m", MaxOutput+100)
	p.Write([]byte(middle))
	p.take()
	p.Write([]byte("last\n"))
	p.capture.close()
	got := p.take()
	if !strings.Contains(got, "full log:") || !strings.HasSuffix(got, "last\n") || strings.Contains(got, middle) {
		t.Fatalf("last take: %q", got)
	}
	log, err := os.ReadFile(p.capture.file.Name())
	if err != nil || string(log) != "earlier\n"+middle+"last\n" {
		t.Fatalf("full log across takes: %d bytes, %v", len(log), err)
	}
}

// Exercise the real pipe, including a nonzero exit and cancellation: the log
// must be closed and readable before done, not merely held in memory.
func TestCommandLog(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "exit", true: "stopped"}[stopped], func(t *testing.T) {
			t.Setenv("TMPDIR", t.TempDir())
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			command := "printf 'start\\n'; head -c 100000 /dev/zero; printf 'end\\n' >&2; exit 7"
			if stopped {
				command = "printf 'start\\n'; head -c 100000 /dev/zero; printf 'end\\n' >&2; sleep 30"
			}
			p, err := launch(ctx, cancel, t.TempDir(), settings{}, command, nil)
			if err != nil {
				t.Fatal(err)
			}
			<-p.done
			result, wasStopped := p.ending()
			if wasStopped != stopped || (!stopped && !strings.HasPrefix(result, "exit 7\n")) || !strings.Contains(result, "full log:") || len(result) > MaxOutput+1024 {
				t.Fatalf("result: %d bytes, stopped %v; prefix %.200s", len(result), wasStopped, result)
			}
			got, err := os.ReadFile(p.capture.file.Name())
			want := append([]byte("start\n"), make([]byte, 100000)...)
			want = append(want, []byte("end\n")...)
			// When the group is killed, bash may reap the sleep it started
			// before the SIGKILL reaches bash itself, and then report that on
			// stderr, after the command's own output: a line of the shell,
			// not bytes that launch or capture invented. Whether the report
			// names the signal differs: bash 3.2 on macOS writes "Killed: 9",
			// bash 5 on Linux writes "Killed".
			report := regexp.MustCompile(`^` + regexp.QuoteMeta(shell) + `: line 1: [0-9]+ Killed(: [0-9]+)?[ \t]+sleep 30\n$`)
			after := got[min(len(got), len(want)):]
			if stopped && bytes.HasPrefix(got, want) && report.Match(after) {
				t.Logf("bash reported the killed child: %q", after)
				got = got[:len(want)]
				after = nil
			}
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("log: %d bytes, %v; want %d; bytes after the expected ones: %q", len(got), err, len(want), after)
			}
		})
	}
}

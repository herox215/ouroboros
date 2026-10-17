package shell

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestDiffFiles(t *testing.T) {
	for _, tt := range []struct {
		name, text string
		want       []string
	}{
		{"rustfmt", "Diff in /tmp/a file.rs:12:\r\n-x\n+y\nDiff in /tmp/a file.rs:18:\nDiff in src/b.rs:2:", []string{"/tmp/a file.rs", "src/b.rs"}},
		{"unified", "--- a/src/a.rs\told\n+++ b/src/a.rs\tnew\n--- a/deleted.rs\n+++ /dev/null\n--- /dev/null\n+++ b/new.rs\n", []string{"src/a.rs", "deleted.rs", "new.rs"}},
		{"ordinary", "Diff in prose\n+++\n--- FAIL: TestThing\n", nil},
		{"empty names", "--- a/\n+++ \tnew\n", nil},
		{"long line", strings.Repeat("x", 100000) + "Diff in hidden.rs:1:\nDiff in visible.rs:2:\n", []string{"visible.rs"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			for _, chunk := range []int{1, 733, len(tt.text)} {
				var d diffFiles
				for start := 0; start < len(tt.text); start += chunk {
					d.write([]byte(tt.text[start:min(start+chunk, len(tt.text))]))
					if len(d.line) > 4096 {
						t.Fatal("unbounded pending line")
					}
				}
				d.close()
				if fmt.Sprint(d.paths) != fmt.Sprint(tt.want) {
					t.Fatalf("chunk %d: %q, want %q", chunk, d.paths, tt.want)
				}
			}
		})
	}
}

func TestDiffFilesBounded(t *testing.T) {
	var d diffFiles
	for i := 0; i < 100; i++ {
		d.write([]byte(fmt.Sprintf("Diff in %s%d.rs:1:\n", strings.Repeat("x", 300), i)))
	}
	if len(d.paths) != maxDiffFiles || !d.more || !strings.Contains(d.summary(), "additional files omitted") || len(d.summary()) > 1500 {
		t.Fatalf("unbounded file summary: %d paths, %d bytes", len(d.paths), len(d.summary()))
	}
}

func TestDiffSummaryEncodedBound(t *testing.T) {
	var d diffFiles
	for i := 0; i < maxDiffFiles; i++ {
		d.add(strings.Repeat("\x01", 240) + fmt.Sprint(i))
	}
	if got := d.summary(); len(got) > 1500 || !strings.Contains(got, "additional files omitted") {
		t.Fatalf("encoded summary is not compact: %d bytes", len(got))
	}
}

func TestDiffCaptureExcerpt(t *testing.T) {
	for _, mode := range []string{"large", "small", "failed"} {
		t.Run(mode, func(t *testing.T) {
			p := &process{out: &output{}, capture: capture{dir: t.TempDir()}}
			if mode == "failed" {
				p.capture.dir += "/missing"
			}
			body := strings.Repeat("-old\n+new\n", MaxOutput)
			if mode == "small" {
				body = "-old\n+new\n"
			}
			want := "Diff in src/first.rs:1:\n" + body + "Diff in src/middle.rs:2:\n" + body + "Diff in src/last.rs:3:\n"
			// Takes must not lose the index or the full log.
			p.Write([]byte(want[:10]))
			p.take()
			p.Write([]byte(want[10:]))
			p.capture.close()
			got := p.take()
			switch mode {
			case "large":
				for _, path := range []string{"src/first.rs", "src/middle.rs", "src/last.rs"} {
					if !strings.Contains(got, path) {
						t.Fatalf("missing %s in summary", path)
					}
				}
				if len(got) > 4096 || !strings.Contains(got, "diff excerpt") {
					t.Fatalf("not compact: %d bytes", len(got))
				}
				log, err := os.ReadFile(p.capture.file.Name())
				if err != nil || string(log) != want {
					t.Fatalf("full diff not preserved: %v", err)
				}
			case "small":
				if got != want[10:] {
					t.Fatalf("small output changed: %q", got)
				}
			case "failed":
				if !strings.Contains(got, "could not save full output") || strings.Contains(got, "diff excerpt") || len(got) < MaxOutput {
					t.Fatal("failed capture must retain the generic bounded excerpt")
				}
			}
		})
	}
}

func TestDiffExcerptBounds(t *testing.T) {
	// Up to both ends together the output stays as it is.
	small := strings.Repeat("a", 1024) + strings.Repeat("b", 1024)
	if got := diffExcerpt(small); got != small {
		t.Fatalf("small output changed: %d bytes", len(got))
	}
	large := strings.Repeat("a", 1024) + "m" + strings.Repeat("b", 1024)
	want := strings.Repeat("a", 1024) + "\n[… diff excerpt; middle omitted; see full log …]\n" + strings.Repeat("b", 1024)
	if got := diffExcerpt(large); got != want {
		t.Fatalf("excerpt: %q", got)
	}
}

func TestDiffCommandExit(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	command := "printf 'Diff in src/check.rs:1:\n'; head -c 100000 /dev/zero; printf '\ncheck failed\n' >&2; exit 7"
	p, err := launch(ctx, cancel, t.TempDir(), settings{}, command, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-p.done
	got, stopped := p.ending()
	if stopped || !strings.HasPrefix(got, "exit 7\n") || !strings.Contains(got, "src/check.rs") || !strings.Contains(got, "full log:") || !strings.HasSuffix(got, "check failed\n") || len(got) > 4096 {
		t.Fatalf("diff check result: %.500s (%d bytes)", got, len(got))
	}
}

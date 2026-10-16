// Package sanity runs the project's tests and reports measured coverage gaps.
package sanity

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Options controls the expensive, explicit whole-project diagnostic.
type Options struct {
	TimeoutMinutes int   `json:"timeout_minutes"`
	GUI            *bool `json:"gui"`
}

// Run tests source without installing anything. Logs and profiles remain in a
// private temporary directory; test failures are findings, not tool errors.
func Run(ctx context.Context, source, goTool string, opts Options) (string, error) {
	if source == "" {
		return "", fmt.Errorf("check_sanity needs the source checkout of this build")
	}
	if opts.TimeoutMinutes == 0 {
		opts.TimeoutMinutes = 8
	}
	if opts.TimeoutMinutes < 1 || opts.TimeoutMinutes > 60 {
		return "", fmt.Errorf("timeout_minutes must be between 1 and 60")
	}
	if _, err := os.Stat(filepath.Join(source, "go.mod")); err != nil {
		return "", fmt.Errorf("source checkout: %w", err)
	}
	dir, err := os.MkdirTemp("", "ouro-sanity-")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(opts.TimeoutMinutes)*time.Minute)
	defer cancel()
	var report strings.Builder
	fmt.Fprintf(&report, "check_sanity: %s\nFull logs and coverage: %s\n", source, dir)
	profile := filepath.Join(dir, "coverage.out")
	// Include every package, even packages without their own tests. The JSON
	// log records named failures and skips; -count=1 bypasses cached results.
	args := []string{"test", "-json", "-count=1", "-race", "-timeout=5m", "-coverpkg=./...", "-coverprofile=" + profile, "./..."}
	failed := runCheck(ctx, source, goTool, args, filepath.Join(dir, "go-tests.json"), true, &report)
	if ctx.Err() == nil {
		coverage, err := coverageReport(profile)
		if err != nil {
			failed = true
			fmt.Fprintf(&report, "Coverage unavailable: %v\n", err)
		} else {
			report.WriteString(coverage)
		}
		if failed {
			report.WriteString("Coverage is partial or unavailable because testing or coverage collection failed; missing measurements are not proof that code is untested.\n")
		}
	}
	if opts.GUI == nil || *opts.GUI {
		if ctx.Err() == nil {
			failed = runCheck(ctx, source, goTool, []string{"test", "-json", "-count=1", "-race", "-timeout=5m", "-tags=production,webkit2_41", "./internal/plugins/gui"}, filepath.Join(dir, "gui-tests.json"), true, &report) || failed
		}
		if ctx.Err() == nil {
			failed = runCheck(ctx, source, "node", []string{"--test", "internal/plugins/gui/frontend_test.mjs"}, filepath.Join(dir, "frontend.log"), false, &report) || failed
		}
	} else {
		report.WriteString("Native GUI and JavaScript frontend tests explicitly skipped (gui=false).\n")
	}
	if ctx.Err() != nil {
		fmt.Fprintf(&report, "INCOMPLETE: %v; later checks were not run.\n", ctx.Err())
	} else if failed {
		report.WriteString("RESULT: problems found (test failures, missing test prerequisites or unavailable coverage).\n")
	} else {
		report.WriteString("RESULT: selected test commands passed. Skips and coverage gaps still need review.\n")
	}
	report.WriteString("Scope: default Go build plus native GUI/frontend tests unless disabled; no mutation tests, alternative platforms or arbitrary build tags. Coverage excludes subprocesses and the separate GUI run. Test skips are not passes. Coverage does not assess assertions or branch coverage.\n")
	return report.String(), nil
}

// runCheck streams complete output to disk and retains only bounded summaries.
func runCheck(ctx context.Context, source, exe string, args []string, log string, jsonLog bool, report *strings.Builder) bool {
	fmt.Fprintf(report, "\n$ %s %s\n", exe, strings.Join(args, " "))
	f, err := os.Create(log)
	if err != nil {
		fmt.Fprintf(report, "Cannot create log: %v\n", err)
		return true
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = source
	// Tests such as the end-to-end test invoke Go themselves. Preserve the
	// fallback toolchain selected by update.GoTool even when it is not in PATH.
	if filepath.IsAbs(exe) && filepath.Base(exe) == "go" {
		cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(exe)+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Tests may spawn real nodes; cancel the group, not only the go parent.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 2 * time.Second
	output := &summaryWriter{file: f, jsonLog: jsonLog}
	cmd.Stdout, cmd.Stderr = output, output
	err = cmd.Run()
	output.flush()
	fmt.Fprintf(report, "%s", output.summary.String())
	if output.omitted > 0 {
		fmt.Fprintf(report, "%d additional failure/skip entries in %s\n", output.omitted, log)
	}
	if err != nil {
		fmt.Fprintf(report, "FAILED: %v\nLast output (complete log: %s):\n%s\n", err, log, output.tail)
		return true
	}
	report.WriteString("PASS\n")
	return false
}

type summaryWriter struct {
	file             io.Writer
	jsonLog          bool
	pending          []byte
	tail             string
	summary          strings.Builder
	entries, omitted int
}

func (w *summaryWriter) Write(p []byte) (int, error) {
	n, err := w.file.Write(p)
	w.tail += string(p)
	if len(w.tail) > 8192 {
		w.tail = w.tail[len(w.tail)-8192:]
	}
	if w.jsonLog {
		w.pending = append(w.pending, p...)
		for {
			i := strings.IndexByte(string(w.pending), '\n')
			if i < 0 {
				break
			}
			w.event(w.pending[:i])
			w.pending = w.pending[i+1:]
		}
		// A malformed giant line must not grow the in-memory report indefinitely.
		if len(w.pending) > 1<<20 {
			w.pending = nil
		}
	}
	return n, err
}
func (w *summaryWriter) event(line []byte) {
	var e struct{ Action, Package, Test string }
	if json.Unmarshal(line, &e) != nil || (e.Action != "fail" && e.Action != "skip") {
		return
	}
	if w.entries >= 60 {
		w.omitted++
		return
	}
	w.entries++
	fmt.Fprintf(&w.summary, "%s: %s %s\n", strings.ToUpper(e.Action), e.Package, e.Test)
}
func (w *summaryWriter) flush() {
	if len(w.pending) > 0 {
		w.event(w.pending)
		w.pending = nil
	}
}

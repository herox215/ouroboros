package sanity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCoverageFixture(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "coverage.out")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCoverageReportWeightedAndSorted(t *testing.T) {
	profile := "mode: count\n" +
		"pkg/z.go:2.1,3.2 2 0\n" +
		"pkg/a.go:9.1,12.2 5 0\n" +
		"pkg/a.go:1.1,2.2 2 0\n" +
		"pkg/c.go:1.1,2.2 3 7\n" +
		"pkg/empty.go:1.1,1.2 0 0\n"
	report, err := coverageReport(writeCoverageFixture(t, profile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"25.0% (3/12 statements covered; 9 uncovered)",
		"Raw statement coverage only", "branch coverage", "assertion quality", "Build-tag exclusions",
		"pkg/a.go:9-12 (columns 1-2): 5 uncovered statements",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q:\n%s", want, report)
		}
	}
	if !(strings.Index(report, "pkg/a.go:9-12") < strings.Index(report, "pkg/a.go:1-2") &&
		strings.Index(report, "pkg/a.go:1-2") < strings.Index(report, "pkg/z.go:2-3")) {
		t.Fatalf("blocks not sorted:\n%s", report)
	}
	if strings.Contains(report, "pkg/empty.go") || strings.Contains(report, "Truncated") {
		t.Fatalf("unexpected empty block or truncation:\n%s", report)
	}
}

func TestCoverageReportEmptyAndZeroCounts(t *testing.T) {
	for _, tc := range []struct{ name, profile, want string }{
		{"header only", "mode: set\n", "N/A (0 statements in profile)"},
		{"zero statements", "mode: atomic\nf.go:1.1,1.2 0 0\n", "N/A (0 statements in profile)"},
		{"zero executions", "mode: count\nf.go:1.1,1.2 4 0\n", "0.0% (0/4 statements covered; 4 uncovered)"},
		{"all covered", "mode: set\nf.go:1.1,1.2 4 1\n", "100.0% (4/4 statements covered; 0 uncovered)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := coverageReport(writeCoverageFixture(t, tc.profile))
			if err != nil || !strings.Contains(report, tc.want) {
				t.Fatalf("report = %q, err = %v; want %q", report, err, tc.want)
			}
		})
	}
}

func TestCoverageReportTruncation(t *testing.T) {
	for _, n := range []int{60, 63} {
		var profile strings.Builder
		profile.WriteString("mode: set\n")
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&profile, "f.go:%d.1,%d.2 1 0\n", i, i)
		}
		report, err := coverageReport(writeCoverageFixture(t, profile.String()))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(report, "  f.go:"); got != 60 {
			t.Fatalf("displayed %d blocks, want 60", got)
		}
		if n == 63 && !strings.Contains(report, "Truncated: showing 60 of 63 uncovered blocks; 3 omitted.") {
			t.Fatalf("missing truncation notice:\n%s", report)
		}
		if n == 60 && strings.Contains(report, "Truncated") {
			t.Fatal("exactly 60 entries must not be marked truncated")
		}
		if !strings.Contains(report, fmt.Sprintf("0/%d statements covered", n)) {
			t.Fatal("aggregate must include undisplayed blocks")
		}
	}
}

func TestParseCoverageProfileModesAndDuplicates(t *testing.T) {
	for _, mode := range []string{"set", "count", "atomic"} {
		t.Run(mode, func(t *testing.T) {
			blocks, err := parseCoverageProfile(strings.NewReader("mode: " + mode + "\r\n" +
				"C:/some path/f.go:10.2,12.8 3 0\r\n" +
				"C:/some path/f.go:10.2,12.8 3 2\r\n"))
			if err != nil {
				t.Fatal(err)
			}
			if len(blocks) != 1 || !blocks[0].covered || blocks[0].statements != 3 ||
				blocks[0].file != "C:/some path/f.go" || blocks[0].startLine != 10 || blocks[0].endCol != 8 {
				t.Fatalf("unexpected blocks: %+v", blocks)
			}
			report, err := coverageReport(writeCoverageFixture(t, "mode: "+mode+"\nf.go:1.1,1.2 3 0\nf.go:1.1,1.2 3 1\n"))
			if err != nil || !strings.Contains(report, "3/3 statements covered") {
				t.Fatalf("duplicate report = %q, err = %v", report, err)
			}
		})
	}
}

func TestParseCoverageProfileMalformed(t *testing.T) {
	for _, profile := range []string{
		"", "\n", "mode: other\n", "f.go:1.1,2.1 1 0\n", "mode: set\n\n",
		"mode: set\nf.go:1.1,2.1 1\n", "mode: set\nf.go:1.1,2.1 1 0 extra\n",
		"mode: set\nf.go:1x1,2.1 1 0\n", "mode: set\nf.go:1.1,2.1 -1 0\n",
		"mode: set\nf.go:1.1,2.1 1 -1\n", "mode: set\nf.go:0.1,2.1 1 0\n",
		"mode: set\nf.go:1.0,2.1 1 0\n", "mode: set\nf.go:2.1,1.1 1 0\n",
		"mode: set\nf.go:1.2,1.1 1 0\n", "mode: set\nf.go:1.1,2.1 18446744073709551616 0\n",
		"mode: set\nf.go:1.1,2.1 1 0\nf.go:1.1,2.1 2 0\n",
	} {
		t.Run(profile, func(t *testing.T) {
			if _, err := parseCoverageProfile(strings.NewReader(profile)); err == nil {
				t.Fatal("expected malformed profile error")
			}
		})
	}
}

func TestCoverageReportErrors(t *testing.T) {
	if _, err := coverageReport(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected missing file error")
	}
	for _, profile := range []string{"", "mode: set\nbad\n",
		"mode: count\nf.go:1.1,1.2 18446744073709551615 0\nf.go:2.1,2.2 1 0\n"} {
		if report, err := coverageReport(writeCoverageFixture(t, profile)); err == nil || report != "" {
			t.Fatalf("expected error without partial report, got %q, %v", report, err)
		}
	}
}

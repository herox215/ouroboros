package sanity

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const coverageDisplayLimit = 60

// Parse the position from the right: filenames can contain spaces and colons.
var coverageBlockPattern = regexp.MustCompile(
	`^(.+):([0-9]+)\.([0-9]+),([0-9]+)\.([0-9]+)[ \t]+([0-9]+)[ \t]+([0-9]+)$`)

type coverageBlock struct {
	file                string
	startLine, startCol uint64
	endLine, endCol     uint64
	statements          uint64
	covered             bool
}

type coveragePosition struct {
	file                string
	startLine, startCol uint64
	endLine, endCol     uint64
}

// coverageReport reads a standard Go text coverprofile from profile (a path).
// It describes only statements in that profile, not all source in the project.
func coverageReport(profile string) (string, error) {
	f, err := os.Open(profile)
	if err != nil {
		return "", fmt.Errorf("read coverage profile: %w", err)
	}
	defer f.Close()
	blocks, err := parseCoverageProfile(f)
	if err != nil {
		return "", fmt.Errorf("coverage profile %q: %w", profile, err)
	}
	var total, covered uint64
	var uncovered []coverageBlock
	for _, b := range blocks {
		if ^uint64(0)-total < b.statements {
			return "", fmt.Errorf("coverage profile %q: statement total overflows uint64", profile)
		}
		total += b.statements
		if b.covered {
			covered += b.statements
		} else if b.statements > 0 {
			uncovered = append(uncovered, b)
		}
	}
	sort.Slice(uncovered, func(i, j int) bool {
		a, b := uncovered[i], uncovered[j]
		if a.statements != b.statements {
			return a.statements > b.statements
		}
		if a.file != b.file {
			return a.file < b.file
		}
		if a.startLine != b.startLine {
			return a.startLine < b.startLine
		}
		if a.startCol != b.startCol {
			return a.startCol < b.startCol
		}
		if a.endLine != b.endLine {
			return a.endLine < b.endLine
		}
		return a.endCol < b.endCol
	})
	var out strings.Builder
	if total == 0 {
		fmt.Fprintln(&out, "Statement coverage: N/A (0 statements in profile).")
	} else {
		fmt.Fprintf(&out, "Statement coverage: %.1f%% (%d/%d statements covered; %d uncovered).\n",
			100*float64(covered)/float64(total), covered, total, total-covered)
	}
	fmt.Fprintln(&out, "Raw statement coverage only; this does not measure branch coverage or assertion quality.")
	fmt.Fprintln(&out, "Only blocks present in the profile are counted. Build-tag exclusions (and other files/packages not instrumented) are not measured; excluded code cannot be identified from this profile alone.")
	if len(uncovered) == 0 {
		fmt.Fprintln(&out, "No uncovered statement blocks in profile.")
		return out.String(), nil
	}
	fmt.Fprintln(&out, "Uncovered blocks (most uncovered statements first):")
	display := len(uncovered)
	if display > coverageDisplayLimit {
		display = coverageDisplayLimit
	}
	for _, b := range uncovered[:display] {
		fmt.Fprintf(&out, "  %s:%d-%d (columns %d-%d): %d uncovered statements\n",
			b.file, b.startLine, b.endLine, b.startCol, b.endCol, b.statements)
	}
	if display < len(uncovered) {
		fmt.Fprintf(&out, "Truncated: showing %d of %d uncovered blocks; %d omitted.\n",
			display, len(uncovered), len(uncovered)-display)
	}
	return out.String(), nil
}

// Duplicate positions occur when test profiles from multiple packages are
// combined. Count each block once; any positive execution count covers it.
func parseCoverageProfile(r io.Reader) ([]coverageBlock, error) {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), 1024*1024)
	if !s.Scan() {
		if err := s.Err(); err != nil {
			return nil, fmt.Errorf("read header: %w", err)
		}
		return nil, fmt.Errorf("missing mode header (empty profile)")
	}
	switch s.Text() {
	case "mode: set", "mode: count", "mode: atomic":
	default:
		return nil, fmt.Errorf("line 1: expected mode: set, count, or atomic")
	}
	var blocks []coverageBlock
	positions := make(map[coveragePosition]int)
	line := 1
	for s.Scan() {
		line++
		m := coverageBlockPattern.FindStringSubmatch(s.Text())
		if m == nil {
			return nil, fmt.Errorf("line %d: malformed coverage block", line)
		}
		var values [6]uint64
		for i := range values {
			n, err := strconv.ParseUint(m[i+2], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("line %d: invalid numeric field: %w", line, err)
			}
			values[i] = n
		}
		b := coverageBlock{m[1], values[0], values[1], values[2], values[3], values[4], values[5] > 0}
		if b.startLine == 0 || b.startCol == 0 || b.endLine == 0 || b.endCol == 0 ||
			b.endLine < b.startLine || (b.endLine == b.startLine && b.endCol < b.startCol) {
			return nil, fmt.Errorf("line %d: invalid source range", line)
		}
		key := coveragePosition{b.file, b.startLine, b.startCol, b.endLine, b.endCol}
		if i, ok := positions[key]; ok {
			if blocks[i].statements != b.statements {
				return nil, fmt.Errorf("line %d: duplicate block has inconsistent statement count", line)
			}
			blocks[i].covered = blocks[i].covered || b.covered
		} else {
			positions[key] = len(blocks)
			blocks = append(blocks, b)
		}
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("read coverage blocks: %w", err)
	}
	return blocks, nil
}

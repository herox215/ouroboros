#!/bin/sh
# Mutation tests: gremlins changes one operator at a time and runs the tests of
# the package. A change the tests do not notice is a surviving mutant.
#
#	scripts/mutation.sh              every production package
#	scripts/mutation.sh DIR...       only these packages, e.g. internal/a2a
#
#	OURO_MUTATION_TIME=N   stop after N seconds and report what is done by then
#	OURO_MUTATION_DIR=DIR  where the report goes (default: .mutation)
#
# A survivor that mutation-equivalents.txt does not accept fails the run: it is
# a missing assertion or an entry still to be added. So does a failure to run
# the mutation tests at all. There is no score to reach.
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
. "$ROOT/scripts/lib.sh"
cd "$ROOT"

gremlins=$("$ROOT/scripts/tool.sh" gremlins)
equivalents=mutation-equivalents.txt
uncoverable=mutation-uncoverable.txt
out=${OURO_MUTATION_DIR:-$ROOT/.mutation}
limit=${OURO_MUTATION_TIME:-0}
mutant_time=90
# The build tags of the Makefile: goolm selects the pure Go crypto of Matrix.
tags=goolm
# Calls that send a signal to a process ID, in production code or in tests.
signals='syscall\.Kill|unix\.Kill|os\.FindProcess'

if [ "$limit" -gt 0 ] && ! command -v timeout >/dev/null 2>&1; then
	echo "mutation: no 'timeout' command here, running without the limit of ${limit}s." >&2
	limit=0
fi

if [ $# -gt 0 ]; then
	packages=$(printf '%s\n' "$@" | sed -e 's|^\./||' -e 's|/$||')
else
	# Production code only: what coverage leaves out as test code is not mutated either.
	module=$(go list -m)
	sed -e 's/#.*//' -e 's/[[:space:]]*$//' -e 's|/$||' -e '/^$/d' coverage-exclusions.txt >"${TMPDIR:-/tmp}/ouro-mutation-excluded.$$"
	packages=$(go list -tags "$tags" -f '{{if .GoFiles}}{{.ImportPath}}{{end}}' ./... | sed -e "s|^$module/||" | grep -vxF -f "${TMPDIR:-/tmp}/ouro-mutation-excluded.$$" || true)
	rm -f "${TMPDIR:-/tmp}/ouro-mutation-excluded.$$"
fi

rm -rf "$out"
mkdir -p "$out"
results=$out/mutants.txt
: >"$results"

# Test results must not come from the cache: gremlins derives the time a mutant
# may take from how long the tests of the package ran.
GOFLAGS="${GOFLAGS:-} -count=1"
export GOFLAGS

# Every mutant is new source to Go, and what it builds of it is never used
# again. In the cache of the user that grew by about 20 GB a day; Go removes
# entries only after five days unused. The mutants get a cache of their own,
# which goes when the run ends.
# It goes next to the cache of the user, not into TMPDIR: a run of the whole
# repository needs some 12 GB (a few MB per mutant), and TMPDIR is a tmpfs in
# memory on many systems. A full one there makes gremlins die with
# "panic: error, this is temporary" while it copies a working directory.
GOCACHE=$(mktemp -d "$(dirname "$(go env GOCACHE)")/ouro-mutation-cache.XXXXXX")
export GOCACHE
trap 'rm -rf "$GOCACHE"' EXIT
trap 'exit 130' INT TERM

start=$(date +%s)
unfinished=
skipped=
for dir in $packages; do
	if [ ! -d "$dir" ]; then
		echo "mutation: $dir is not a directory of this repository." >&2
		exit 2
	fi
	log=$out/$(printf '%s' "$dir" | tr '/' '_').log
	if [ "$limit" -gt 0 ] && [ $((limit - ($(date +%s) - start))) -le 0 ]; then
		unfinished="$unfinished $dir"
		continue
	fi

	# A mutant turns "pid <= 0" into "pid > 0", and code or a test that signals
	# that PID then signals its process group (0) or every process of the user
	# (-1). Packages that signal process IDs are therefore never mutated.
	signalling=$(grep -lE "$signals" "$dir"/*.go 2>/dev/null | tr '\n' ' ' || true)
	if [ -n "$signalling" ]; then
		echo "mutation: $dir is not mutated: it sends signals to process IDs (${signalling% })."
		skipped="$skipped $dir"
		continue
	fi
	echo "mutation: $dir ..."

	# The tests unchanged: they have to pass, and their time sets the time a
	# mutant gets. gremlins allows a mutant "coefficient" times the time of the
	# unchanged tests. Aimed at are $mutant_time seconds, and never less than
	# three times the unchanged run: enough to compile and run a mutant while
	# all cores are busy, short enough for a mutant that hangs.
	before=$(date +%s)
	if ! go test -tags "$tags" "./$dir" >"$log" 2>&1; then
		echo "mutation: the tests of $dir fail unchanged; they have to pass before they can be mutated:" >&2
		sed 's/^/  /' "$log" >&2
		exit 1
	fi
	took=$(($(date +%s) - before + 1))
	coefficient=$((mutant_time / took))
	[ "$coefficient" -ge 3 ] || coefficient=3

	# gremlins would go on into the directories below $dir. They are packages
	# of their own, with their own turn or left out on purpose: test code, and
	# the ones that send signals, which the look at $dir above did not see.
	# The files are matched by their path below $dir, so "/" is every file
	# that is not in $dir itself.
	status=0
	if [ "$limit" -gt 0 ]; then
		left=$((limit - ($(date +%s) - start)))
		[ "$left" -ge 1 ] || left=1
		timeout "$left" "$gremlins" unleash --config "$ROOT/.gremlins.yaml" --tags "$tags" --timeout-coefficient "$coefficient" --exclude-files / "./$dir" >"$log" 2>&1 || status=$?
	else
		"$gremlins" unleash --config "$ROOT/.gremlins.yaml" --tags "$tags" --timeout-coefficient "$coefficient" --exclude-files / "./$dir" >"$log" 2>&1 || status=$?
	fi
	# Lines of gremlins: "  STATUS MUTATOR at file.go:line:column", the file relative to the package.
	# When it is stopped it reports the mutants it was still testing as LIVED;
	# what follows "Shutting down" is therefore not a result.
	sed -n -E -e '/^Shutting down/q' -e "s|^ *(KILLED\|LIVED\|TIMED OUT\|NOT COVERED\|NOT VIABLE\|SKIPPED\|RUNNABLE) ([A-Z_]+) at (.+:[0-9]+:[0-9]+)\$|$dir/\\3 \\2 \\1|p" "$log" >>"$results"
	if [ "$status" -eq 124 ]; then
		unfinished="$unfinished $dir"
	elif [ "$status" -ne 0 ]; then
		echo "mutation: gremlins failed in $dir (exit $status):" >&2
		sed 's/^/  /' "$log" >&2
		exit 1
	fi
done

# Mutants somebody looked at and found to change nothing observable.
accepted=$out/accepted.txt
if [ -f "$equivalents" ]; then
	sed -e 's/#.*//' -e 's/[[:space:]]*$//' -e '/^$/d' "$equivalents" >"$accepted"
else
	: >"$accepted"
fi

# Mutants gremlins never tries, as coverage gives their code no block of its
# own (a constant, a case expression), and that were checked by hand.
outside=$out/uncoverable.txt
if [ -f "$uncoverable" ]; then
	sed -e 's/#.*//' -e 's/[[:space:]]*$//' -e '/^$/d' "$uncoverable" >"$outside"
else
	: >"$outside"
fi

report=$out/report.txt
awk -v accepted="$accepted" -v outside="$outside" -v own="_$(go env GOOS).go" -v gate="$out/gate.survivors" '
BEGIN {
	while ((getline line < accepted) > 0) { split(line, f, " "); equivalent[f[1] " " f[2]] = 1 }
	while ((getline line < outside) > 0) { split(line, f, " "); unreached[f[1] " " f[2]] = 1 }
}
{
	where = $1; mutator = $2
	status = $3; for (i = 4; i <= NF; i++) status = status " " $i
	total++
	if (status == "LIVED" && ((where " " mutator) in equivalent)) { status = "EQUIVALENT"; seen[where " " mutator] = 1 }
	if (status == "NOT COVERED" && ((where " " mutator) in unreached)) { status = "UNCOVERABLE"; reached[where " " mutator] = 1 }
	# A file of this system alone is listed for the runs on the others, which never measure it.
	if (status != "NOT COVERED" && substr(where, 1, index(where, ":") - 1) ~ (own "$")) reached[where " " mutator] = 1
	count[status]++
	if (status == "LIVED") lived[++l] = sprintf("  %s: %s", where, mutator)
	if (status == "EQUIVALENT") same[++e] = sprintf("  %s: %s", where, mutator)
	if (status == "NOT COVERED") bare[++b] = sprintf("  %s: %s", where, mutator)
	if (status == "TIMED OUT") slow[++s] = sprintf("  %s: %s", where, mutator)
	file = where; sub(/:[0-9]+:[0-9]+$/, "", file); files[file] = 1
}
END {
	printf "mutation: %d mutants: %d killed, %d survived, %d accepted as equivalent, %d timed out, %d not covered, %d accepted as uncoverable, %d not viable\n", \
		total, count["KILLED"], count["LIVED"], count["EQUIVALENT"], count["TIMED OUT"], count["NOT COVERED"], count["UNCOVERABLE"], count["NOT VIABLE"] + count["SKIPPED"]
	if (l) {
		print "Survived: the tests of the package still pass with this operator changed (file:line:column: mutator)."
		for (i = 1; i <= l; i++) print lived[i]
		print "For each one: if the change alters what the code does, a test is missing an assertion; add it."
		print "If it cannot change what the code does, it is an equivalent mutant: list it in mutation-equivalents.txt with the reason."
	}
	if (b) {
		print "Not covered: no test of the package itself runs this code, so the mutant was not tried."
		for (i = 1; i <= b; i++) print bare[i]
		print "Add a test in the package; a constant or a case expression, which coverage gives no block, goes into mutation-uncoverable.txt."
	}
	if (s) {
		print "Timed out: the tests did not finish with this change (counts as noticed; often a loop that no longer ends)."
		for (i = 1; i <= s; i++) print slow[i]
	}
	if (e) {
		print "Accepted as equivalent (mutation-equivalents.txt):"
		for (i = 1; i <= e; i++) print same[i]
	}
	for (key in equivalent) {
		split(key, f, " "); file = f[1]; sub(/:[0-9]+:[0-9]+$/, "", file)
		if ((file in files) && !(key in seen)) printf "mutation-equivalents.txt: no surviving %s at %s any more; remove or update the line.\n", f[2], f[1]
	}
	for (key in unreached) {
		split(key, f, " "); file = f[1]; sub(/:[0-9]+:[0-9]+$/, "", file)
		if ((file in files) && !(key in reached)) printf "mutation-uncoverable.txt: no uncovered %s at %s any more; remove or update the line.\n", f[2], f[1]
	}
	# What the run fails on: the survivors that no entry of
	# mutation-equivalents.txt accepted.
	print count["LIVED"] + 0 > gate
}' "$results" >"$report"
if [ -n "$skipped" ]; then
	echo "Not mutated, because the package sends signals to process IDs and a mutant could signal the wrong ones:$skipped" >>"$report"
fi

# The lists of mutants that are fine are long; the terminal gets what needs a look.
awk '/^Timed out:/ || /^Accepted as equivalent/ { quiet = 1; next } /^[^ ]/ { quiet = 0 } !quiet' "$report"
if [ -n "$unfinished" ]; then
	echo "mutation: stopped after ${limit}s, not or not completely done:$unfinished" >&2
	echo "          Run them to the end with: make mutation PKGS=\"${unfinished# }\"" >&2
fi
echo "mutation: full report in $report, output of gremlins per package next to it ($(($(date +%s) - start))s)."

if [ ! -f "$out/gate.survivors" ]; then
	echo "mutation: the report is incomplete; nothing is known about the survivors." >&2
	exit 1
fi
survivors=$(cat "$out/gate.survivors")
if [ "$survivors" -gt 0 ]; then
	echo "mutation: $survivors survived that no test noticed and no entry accepts." >&2
	echo "          Add the missing assertion, or list an equivalent mutant in $equivalents with its reason." >&2
	exit 1
fi

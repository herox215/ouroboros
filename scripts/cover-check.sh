#!/bin/sh
# Fails unless every statement of the production code is covered.
#
#	scripts/cover-check.sh [PROFILE]     (default: cover.out, written by "make cover")
#
# The statements are counted from the blocks of the profile, nothing is
# rounded: one uncovered statement fails the check. Every package of the module
# has to be in the profile, also the ones without tests. What is left out is
# listed in coverage-exclusions.txt and nowhere else.
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
. "$ROOT/scripts/lib.sh"
cd "$ROOT"

profile=${1:-cover.out}
exclusions=coverage-exclusions.txt
binary=./cmd/ouro

if [ ! -s "$profile" ]; then
	echo "cover-check: no profile at $profile; 'make cover-check' writes it first." >&2
	exit 1
fi

module=$(go list -m)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

# Exclusions: one package directory per line, "#" starts a comment.
sed -e 's/#.*//' -e 's/[[:space:]]*$//' -e 's|/$||' -e '/^$/d' "$exclusions" >"$tmp/excluded"
go list ./... | sed -e "s|^$module/||" -e "s|^$module\$|.|" >"$tmp/packages"
go list -deps "$binary" | sed -n -e "s|^$module/||p" >"$tmp/shipped"

status=0
while IFS= read -r dir; do
	if ! grep -qxF -- "$dir" "$tmp/packages"; then
		echo "cover-check: $exclusions lists '$dir', which is not a package of this module. Remove the line." >&2
		status=1
	elif grep -qxF -- "$dir" "$tmp/shipped"; then
		echo "cover-check: $exclusions lists '$dir', but the ouro binary links it." >&2
		echo "             Only packages that are not part of the binary may be excluded." >&2
		status=1
	fi
done <"$tmp/excluded"
[ "$status" -eq 0 ] || exit 1

# A block is "file:startLine.col,endLine.col statements count". The same block
# shows up once per test binary that was built with it; it is covered when any
# of them ran it.
awk -v module="$module" -v excluded="$tmp/excluded" -v packages="$tmp/packages" '
BEGIN {
	while ((getline line < excluded) > 0) skip[line] = 1
	while ((getline line < packages) > 0) known[line] = 1
}
FNR == 1 && /^mode:/ { next }
{
	block = $1; statements = $2; count = $3
	if (!(block in size)) order[++blocks] = block
	size[block] = statements
	if (count > 0) hit[block] = 1
}
END {
	for (i = 1; i <= blocks; i++) {
		block = order[i]
		colon = match(block, /:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/)
		file = substr(block, 1, colon - 1)
		range = substr(block, colon + 1)
		sub("^" module "/", "", file)
		dir = file
		if (!sub("/[^/]*$", "", dir)) dir = "."
		if (dir in skip) { left_out[dir] += size[block]; continue }
		measured[dir] = 1
		total[dir] += size[block]
		all += size[block]
		if (block in hit) continue
		missing[dir] += size[block]
		uncovered += size[block]
		split(range, ends, ",")
		split(ends[1], from, ".")
		split(ends[2], to, ".")
		gaps[++n] = sprintf("%s:%d: %d statement%s not covered (up to line %d)", file, from[1], size[block], size[block] == 1 ? "" : "s", to[1])
	}
	for (dir in known) if (!(dir in skip) && !(dir in measured)) unmeasured[dir] = 1

	failed = uncovered > 0
	for (dir in unmeasured) failed = 1
	if (!failed) {
		printf "cover-check: ok, all %d statements of the production code are covered\n", all
		for (dir in left_out) printf "  left out: %s (%d statements, see coverage-exclusions.txt)\n", dir, left_out[dir]
		exit 0
	}

	printf "cover-check: %d of %d statements of the production code are not covered; all of them have to be.\n", uncovered, all
	for (i = 1; i <= n; i++) print "  " gaps[i]
	if (uncovered > 0) {
		print "By package:"
		for (dir in missing) printf "  %-32s %d of %d\n", dir, missing[dir], total[dir] | "sort"
		close("sort")
	}
	for (dir in unmeasured) printf "  %s: package is not in the profile, so nothing of it was measured\n", dir
	print "Add tests for these lines. Look at them with: go tool cover -html=cover.out"
	exit 1
}' "$profile"

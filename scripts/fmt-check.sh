#!/bin/sh
# Fails when a Go file of the module is not formatted the way gofmt writes it.
# Changes nothing: it only lists the files and says how to format them.
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
. "$ROOT/scripts/lib.sh"
cd "$ROOT"

# gofmt comes with the Go toolchain, next to the go command.
gofmt="$(go env GOROOT)/bin/gofmt"

# Every Go file of every package, tests and files for other platforms included.
# shellcheck disable=SC2016 # a template of "go list", not shell
files=$(go list -f '{{$d := .Dir}}{{range .GoFiles}}{{$d}}/{{.}}
{{end}}{{range .TestGoFiles}}{{$d}}/{{.}}
{{end}}{{range .XTestGoFiles}}{{$d}}/{{.}}
{{end}}{{range .IgnoredGoFiles}}{{$d}}/{{.}}
{{end}}' ./... | sed -e '/^$/d' -e "s|^$ROOT/||")

unformatted=$(printf '%s\n' "$files" | xargs "$gofmt" -l)
if [ -n "$unformatted" ]; then
	echo "fmt-check: not formatted with gofmt:" >&2
	printf '%s\n' "$unformatted" | sed 's/^/  /' >&2
	echo "Format with: gofmt -w <file>   (look first with: gofmt -d <file>)" >&2
	exit 1
fi
echo "fmt-check: ok ($(printf '%s\n' "$files" | wc -l | tr -d ' ') files)"

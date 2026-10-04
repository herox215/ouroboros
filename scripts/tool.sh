#!/bin/sh
# Prints the path of a pinned check tool, building it first if this checkout
# does not have that version yet.
#
#	scripts/tool.sh staticcheck|gremlins
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
. "$ROOT/scripts/lib.sh"
. "$ROOT/scripts/tool-versions"

case "${1:-}" in
	staticcheck) pkg=$STATICCHECK_PKG version=$STATICCHECK_VERSION ;;
	gremlins) pkg=$GREMLINS_PKG version=$GREMLINS_VERSION ;;
	*) echo "usage: $0 staticcheck|gremlins" >&2; exit 2 ;;
esac

# One directory per version and Go release: changing either builds anew.
dir="$TOOLS_DIR/$1-$version-$(go env GOVERSION)"
bin="$dir/$1"
if [ ! -x "$bin" ]; then
	echo "Building $1 $version into ${dir} (once per checkout)..." >&2
	mkdir -p "$dir"
	# "go install pkg@version" ignores the go.mod of this module. GOFLAGS is
	# cleared so that a -mod=vendor or similar of the user does not apply.
	if ! GOBIN="$dir" GOFLAGS='' go install "$pkg@$version" >&2; then
		echo "error: could not build $1 $version. It is downloaded from the Go module proxy:" >&2
		echo "       check the network, then run 'make tools' again." >&2
		exit 1
	fi
fi
printf '%s\n' "$bin"

#!/bin/sh
set -eu

REPO_ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
GO_DIR="${OURO_GO_DIR:-$HOME/.local/go}"

required_version() {
	awk '$1 == "go" { print $2; exit }' "$REPO_ROOT/go.mod"
}

find_go() {
	if command -v go >/dev/null 2>&1; then
		command -v go
		return 0
	fi
	if [ -x "$GO_DIR/bin/go" ]; then
		printf '%s\n' "$GO_DIR/bin/go"
		return 0
	fi
	return 1
}

fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fL --progress-bar -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -O "$2" "$1"
	else
		echo "error: need curl or wget to download Go" >&2
		exit 1
	fi
}

download_go() {
	ver=$(required_version)
	case "$(uname -s)" in
		Linux) os=linux ;;
		Darwin) os=darwin ;;
		*) echo "error: unsupported OS $(uname -s)" >&2; exit 1 ;;
	esac
	case "$(uname -m)" in
		x86_64|amd64) arch=amd64 ;;
		aarch64|arm64) arch=arm64 ;;
		armv7l|armv6l) arch=armv6l ;;
		i386|i686) arch=386 ;;
		*) echo "error: unsupported architecture $(uname -m)" >&2; exit 1 ;;
	esac

	url="https://go.dev/dl/go${ver}.${os}-${arch}.tar.gz"
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT

	echo "Downloading Go ${ver} (${os}-${arch})..." >&2
	fetch "$url" "$tmp/go.tar.gz"
	rm -rf "$GO_DIR"
	mkdir -p "$(dirname "$GO_DIR")"
	echo "Extracting to $(dirname "$GO_DIR")..." >&2
	tar -C "$(dirname "$GO_DIR")" -xzf "$tmp/go.tar.gz"
}

if gopath=$(find_go); then
	echo "Using Go: $gopath"
else
	echo "Go not found; bootstrapping toolchain locally (no sudo)."
	download_go
	gopath="$GO_DIR/bin/go"
fi

PATH="$(dirname "$gopath"):$PATH"
export PATH
export GOTOOLCHAIN=auto

cd "$REPO_ROOT"
make install

# "ouro update" builds into a staging directory and installs the binary itself.
[ -z "${OURO_UPDATE:-}" ] || exit 0

echo
echo "Done. Installed into ${PREFIX:-$HOME/.local}/bin/ouro"
case ":$PATH:" in
	*":${PREFIX:-$HOME/.local}/bin:"*) ;;
	*) echo "Note: add '${PREFIX:-$HOME/.local}/bin' to your PATH." ;;
esac

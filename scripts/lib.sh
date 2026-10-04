# shellcheck shell=sh
# Shared by the check scripts. Source it with ROOT set to the checkout:
#
#	ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
#	. "$ROOT/scripts/lib.sh"

# Go is often installed outside PATH (install.sh puts it into ~/.local/go), and
# Git hooks run with whatever environment the commit was started from.
if ! command -v go >/dev/null 2>&1; then
	for dir in "${OURO_GO_DIR:-$HOME/.local/go}/bin" /usr/local/go/bin; do
		if [ -x "$dir/go" ]; then
			PATH="$dir:$PATH"
			export PATH
			break
		fi
	done
fi
if ! command -v go >/dev/null 2>&1; then
	echo "error: go not found in PATH, ${OURO_GO_DIR:-$HOME/.local/go}/bin or /usr/local/go/bin." >&2
	echo "       Run ./install.sh once (it bootstraps Go), or set OURO_GO_DIR." >&2
	exit 1
fi

# Where the pinned tools are built. The pre-commit hook points this at the
# checkout while it runs the checks on a snapshot of the staged files.
# shellcheck disable=SC2034 # used by scripts/tool.sh
TOOLS_DIR="${OURO_TOOLS_DIR:-$ROOT/.tools}"

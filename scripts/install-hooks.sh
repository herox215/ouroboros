#!/bin/sh
# Installs the pre-commit hook of this repository into the checkout.
#
#	scripts/install-hooks.sh              install; refuses when another hook is in place
#	scripts/install-hooks.sh --chain      install and keep running the existing pre-commit hook first
#	scripts/install-hooks.sh --uninstall  remove it and put a chained hook back
#
# What gets installed is a few lines that run hooks/pre-commit of the checkout,
# so the checks themselves stay versioned with the code. Other hooks
# (commit-msg, pre-push, ...) are not touched.
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

mode=${1:-install}
case "$mode" in
	install | --chain | --uninstall) ;;
	*) echo "usage: $0 [--chain | --uninstall]" >&2; exit 2 ;;
esac

marker='ouroboros pre-commit hook'
# The line somebody with a hook setup of their own adds to it; printed, not run.
# shellcheck disable=SC2016
call='"$(git rev-parse --show-toplevel)/hooks/pre-commit" || exit 1'

if ! git rev-parse --git-dir >/dev/null 2>&1; then
	echo "hooks: $ROOT is not a Git checkout, there is nothing to install a hook into." >&2
	exit 1
fi

# With core.hooksPath set, Git reads hooks from there and ignores .git/hooks.
# That directory is somebody's setup, often shared by many repositories.
if hooks_path=$(git config --get core.hooksPath) && [ -n "$hooks_path" ]; then
	if [ "$mode" = --uninstall ]; then
		echo "hooks: core.hooksPath is set to '$hooks_path'; this script installed nothing there."
		exit 0
	fi
	cat >&2 <<EOF
hooks: not installed. core.hooksPath is set, so Git takes its hooks from
  $hooks_path
  ($(git config --show-origin --get core.hooksPath | cut -f1))
and ignores the hooks directory of the checkout. That setup is left as it is.

To run the checks of this repository as well, add this line to the pre-commit
hook in that directory (create it with "#!/bin/sh" as first line and make it
executable if there is none):

  $call

Or take the setting away where it is made (see the file above), then run "make hooks" again.
EOF
	exit 1
fi

# In a linked worktree this is the hooks directory of the main checkout: the
# worktrees of one clone share their hooks.
hooks_dir=$(git rev-parse --git-path hooks)
hook=$hooks_dir/pre-commit
kept=$hooks_dir/pre-commit.local

ours() {
	[ -f "$hook" ] && grep -qF "$marker" "$hook"
}

if [ "$mode" = --uninstall ]; then
	if ! ours; then
		echo "hooks: the pre-commit hook of this repository is not installed, nothing to remove."
		exit 0
	fi
	rm -f "$hook"
	if [ -e "$kept" ]; then
		mv "$kept" "$hook"
		echo "hooks: removed; the hook that ran before is back at $hook"
	else
		echo "hooks: removed $hook"
	fi
	exit 0
fi

if [ -e "$hook" ] || [ -L "$hook" ]; then
	if ours; then
		: # ours already: write it again, it may be an older version
	elif [ "$mode" = --chain ]; then
		if [ -e "$kept" ] || [ -L "$kept" ]; then
			echo "hooks: not installed. $kept exists already; move it away first." >&2
			exit 1
		fi
		mv "$hook" "$kept"
		echo "hooks: the existing hook was moved to $kept and keeps running, before the checks."
	else
		cat >&2 <<EOF
hooks: not installed. There already is a pre-commit hook, and it is left as it is:
  $hook

To have both, either

  make hooks CHAIN=1

which moves that hook to pre-commit.local next to it and runs it first, then
the checks of this repository ("make unhooks" moves it back). Or add this line
to the existing hook yourself:

  $call
EOF
		exit 1
	fi
fi

mkdir -p "$hooks_dir"
cat >"$hook" <<'EOF'
#!/bin/sh
# ouroboros pre-commit hook, installed by "make hooks", removed by "make unhooks".
# The checks are in hooks/pre-commit of the checkout, versioned with the code.
top=$(git rev-parse --show-toplevel) || exit 1
kept="$(dirname -- "$0")/pre-commit.local"
if [ -x "$kept" ]; then
	"$kept" "$@" || exit $?
fi
if [ ! -x "$top/hooks/pre-commit" ]; then
	echo "pre-commit: this commit has no hooks/pre-commit in its checkout; nothing was checked." >&2
	exit 0
fi
exec "$top/hooks/pre-commit" "$@"
EOF
chmod +x "$hook"

cat <<EOF
hooks: installed $hook
From now on "git commit" in this clone (and its worktrees) runs hooks/pre-commit first.
The hook is local: other clones need their own "make hooks", "git commit --no-verify"
skips it, and nothing on the server enforces it. Remove it with: make unhooks
EOF

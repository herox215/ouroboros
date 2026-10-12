#!/bin/sh
# Installs the hooks of this repository into the checkout: pre-commit runs the
# checks, pre-push the mutation tests when main is pushed.
#
#	scripts/install-hooks.sh              install; refuses when another hook is in place
#	scripts/install-hooks.sh --chain      install and keep running the existing hooks first
#	scripts/install-hooks.sh --uninstall  remove them and put chained hooks back
#
# What gets installed is a few lines per hook that run hooks/<name> of the
# checkout, so the checks themselves stay versioned with the code. Other hooks
# (commit-msg, ...) are not touched.
set -eu

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

mode=${1:-install}
case "$mode" in
	install | --chain | --uninstall) ;;
	*) echo "usage: $0 [--chain | --uninstall]" >&2; exit 2 ;;
esac

names='pre-commit pre-push'

# The line somebody with a hook setup of their own adds to it; printed, not run.
call() {
	# shellcheck disable=SC2016
	printf '"$(git rev-parse --show-toplevel)/hooks/%s" "$@" || exit 1' "$1"
}

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

To run the checks of this repository as well, add these lines to the hooks of
the same name in that directory (create them with "#!/bin/sh" as first line and
make them executable if there are none):

  pre-commit: $(call pre-commit)
  pre-push:   $(call pre-push)

Or take the setting away where it is made (see the file above), then run "make hooks" again.
EOF
	exit 1
fi

# In a linked worktree this is the hooks directory of the main checkout: the
# worktrees of one clone share their hooks.
hooks_dir=$(git rev-parse --git-path hooks)

ours() {
	[ -f "$hooks_dir/$1" ] && grep -qF "ouroboros $1 hook" "$hooks_dir/$1"
}

if [ "$mode" = --uninstall ]; then
	for name in $names; do
		hook=$hooks_dir/$name
		kept=$hook.local
		if ! ours "$name"; then
			echo "hooks: the $name hook of this repository is not installed, nothing to remove."
			continue
		fi
		rm -f "$hook"
		if [ -e "$kept" ]; then
			mv "$kept" "$hook"
			echo "hooks: removed; the hook that ran before is back at $hook"
		else
			echo "hooks: removed $hook"
		fi
	done
	exit 0
fi

# Everything is looked at before anything is changed: a refusal leaves no hook
# half installed.
for name in $names; do
	hook=$hooks_dir/$name
	kept=$hook.local
	if { [ -e "$hook" ] || [ -L "$hook" ]; } && ! ours "$name"; then
		if [ "$mode" != --chain ]; then
			cat >&2 <<EOF
hooks: not installed. There already is a $name hook, and it is left as it is:
  $hook

To have both, either

  make hooks CHAIN=1

which moves that hook to $name.local next to it and runs it first, then
the hook of this repository ("make unhooks" moves it back). Or add this line
to the existing hook yourself:

  $(call "$name")
EOF
			exit 1
		fi
		if [ -e "$kept" ] || [ -L "$kept" ]; then
			echo "hooks: not installed. $kept exists already; move it away first." >&2
			exit 1
		fi
	fi
done

mkdir -p "$hooks_dir"
for name in $names; do
	hook=$hooks_dir/$name
	kept=$hook.local
	if { [ -e "$hook" ] || [ -L "$hook" ]; } && ! ours "$name"; then
		mv "$hook" "$kept"
		echo "hooks: the existing $name hook was moved to $kept and keeps running first."
	fi
	# pre-push reads the pushed refs from stdin. A chained hook gets a copy, so
	# both see them.
	sed "s/@NAME@/$name/g" >"$hook" <<'EOF'
#!/bin/sh
# ouroboros @NAME@ hook, installed by "make hooks", removed by "make unhooks".
# The checks are in hooks/@NAME@ of the checkout, versioned with the code.
top=$(git rev-parse --show-toplevel) || exit 1
kept="$(dirname -- "$0")/@NAME@.local"
# Only pre-push gets input on stdin; reading it elsewhere would wait for the terminal.
input=
if [ @NAME@ = pre-push ]; then
	input=$(cat)
fi
run() {
	if [ @NAME@ = pre-push ]; then
		printf '%s\n' "$input" | "$@"
	else
		"$@"
	fi
}
if [ -x "$kept" ]; then
	run "$kept" "$@" || exit $?
fi
if [ ! -x "$top/hooks/@NAME@" ]; then
	echo "@NAME@: this checkout has no hooks/@NAME@; nothing was checked." >&2
	exit 0
fi
run "$top/hooks/@NAME@" "$@"
EOF
	chmod +x "$hook"
	echo "hooks: installed $hook"
done

cat <<EOF
From now on "git commit" in this clone (and its worktrees) runs hooks/pre-commit first,
and "git push" to main runs hooks/pre-push. The hooks are local: other clones need their
own "make hooks", --no-verify skips them, and nothing on the server enforces them.
Remove them with: make unhooks
EOF

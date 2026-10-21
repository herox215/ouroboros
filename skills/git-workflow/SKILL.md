---
name: git-workflow
description: Use Git safely and efficiently through bash: inspect repositories, review changes, edit and test, stage and commit scoped work, and handle branches, remotes, and conflicts without losing user changes.
---

# Git workflow

Use this skill for tasks involving Git repositories, changes, history, branches, commits, or remotes. All examples use the `bash` function inside `run`; Git is not a separate tool.

## Working environment

- Set `dir` on each bash call to the repository's absolute path. Each call starts a new shell; `cd` and variables do not carry over. Relative file-tool paths are relative to the conversation's project directory, not a previous bash `dir`.
- If the repository is unknown, locate it from the task context or ask; do not assume the current directory is a repository.
- Use standard Git commands rather than personal aliases. Observed local configuration at skill creation: Git 2.55.0; aliases `co=checkout`, `ci=commit`, `st=status`; global `pull.rebase=true` and `push.autosetupremote=true`. These settings may change and local config may override them.
- Use `git --no-pager` for potentially paged output. Use `git diff --no-ext-diff` to avoid external diff programs. For automation that could prompt, prefix the command with `GIT_TERMINAL_PROMPT=0`; this disables Git terminal credential prompts, but not all SSH or credential-helper interactions. Never enter passwords or disable host verification.
- Do not modify Git configuration merely to make a command succeed. Do not invent an author identity.

## Establish the state

Start with a small, bounded inspection, batching independent commands when useful:

```javascript
print(bash({
  dir: "/absolute/path/to/repo",
  command: "git rev-parse --show-toplevel && git status --short --branch && git --no-pager diff --no-ext-diff --stat && git --no-pager diff --no-ext-diff --cached --stat"
}));
```

Then inspect relevant file diffs and enough surrounding source to understand the task. Check both staged and unstaged changes before touching a file; untracked files also belong to the user unless you created them. Preserve unrelated work. Never interpret a dirty tree as permission to reset or clean it.

Useful focused commands:

```sh
git --no-pager diff --no-ext-diff -- path/to/file
git --no-pager diff --no-ext-diff --cached -- path/to/file
git --no-pager log -8 --oneline --decorate
git --no-pager log -5 --format='%h %s' -- path/to/file
git --no-pager show --stat COMMIT
git --no-pager show COMMIT:path/to/file
git ls-files -- path/to/file
git check-ignore -v -- path/to/file
```

`git diff` excludes untracked files. Read those files separately when relevant. For machine processing, use NUL-delimited output such as `git status --porcelain=v1 -z` or `git ls-files -z`, not whitespace splitting. Do not hide command failures with `|| true` or an unchecked pipeline.

## Commit-only requests

For a request only to commit existing changes, default to status, diff and secret checks plus appropriate short tests. Inspect the full intended commit for accidental credentials, private keys, tokens and unintended files; do not print discovered secrets. Reuse relevant passing test results when nothing affecting them has changed. Do not automatically start a comprehensive review or delegate review tasks. Expand the scope only if the user asks or a specific finding requires it. The number of files alone is not a reason to launch a full review. Keep the staging, hook and verification safeguards below.

## Edit, test, and review

1. Inspect existing changes before overwriting anything. If the requested edit conflicts with user work and cannot be isolated safely, ask.
2. Make focused edits that follow the surrounding code. Keep temporary scripts and logs in the agent workspace, not the repository.
3. Run the tests appropriate to the change, following the project's instructions. Do not run expensive whole-project checks without a reason.
4. Review the final relevant diff and run `git diff --check`. If staging is involved, also run `git diff --cached --check`. Check status again to identify generated files or unintended changes.
5. Report what changed and what was actually tested; do not equate a clean diff check with functional correctness.

For Ouroboros itself, follow the conversation's rebuild rules; a Git workflow does not replace required build or test steps.

## Stage and commit only within the requested scope

- Editing does not imply permission to commit, push, publish, or open a pull request. Do these when the user's task authorizes them.
- Before committing, inspect the entire index with `git diff --cached`. A commit includes all staged files, not just the ones most recently added. Do not include or unstage somebody else's staged work without permission.
- Stage explicit paths with `git add -- path/to/file`; avoid `git add .` and `git add -A` in a mixed working tree. Explicit paths alone are not enough if a file contains unrelated changes: prepare and review a patch containing only the intended changes, or ask if safe separation is unclear.
- Use a noninteractive commit message, following the repository's established style, for example `git commit -m 'Fix focused behavior'`. Let hooks run. Do not use `--no-verify`, amend commits, or alter signing settings to bypass failures.
- After a commit, verify `git --no-pager show --stat --oneline HEAD` and `git status --short --branch`. Report the commit ID and any remaining work.

## Branches and remotes

- Inspect `git branch --show-current`, `git branch -vv`, and `git remote -v` as needed. Remote URLs may contain credentials; redact secrets before reporting output.
- Before an authorized push, verify the remote URL, destination branch, and commits to be published. Prefer an explicit refspec such as `git push REMOTE HEAD:refs/heads/BRANCH`. Do not rely on automatic upstream creation to choose the destination.
- Prefer `git switch BRANCH` and `git switch -c NEW_BRANCH` over ambiguous checkout syntax. Do not create or switch branches unless the task calls for it.
- Do not stash automatically to make switching work. A stash changes the working tree and may conflict when reapplied. If a stash is authorized, identify it precisely and prefer applying then verifying before dropping it.
- Fetch only when needed for the task. `git fetch REMOTE` updates remote-tracking state but does not integrate it into the working branch.
- Do not use an unqualified `git pull`: this environment defaults to rebasing. Inspect divergence first, for example `git rev-list --left-right --count HEAD...REMOTE/BRANCH`, after an appropriate fetch. Use an explicitly chosen integration strategy; `git pull --ff-only REMOTE BRANCH` is suitable only when a fast-forward update is intended. If histories diverge, explain and obtain a decision instead of silently rebasing or merging.
- Never force-push or rewrite shared history without explicit authorization. Even `--force-with-lease` is a history rewrite and needs permission.

## Conflicts, destructive operations, and recovery

- If status shows a merge, rebase, cherry-pick, revert, or unresolved entries already in progress, inspect and report that state. Do not automatically continue or abort an operation you did not start.
- Resolve conflicts only within the requested scope, read both sides and relevant context, remove conflict markers, stage the resolved paths, and run focused tests before continuing an authorized operation. Never choose `ours` or `theirs` wholesale without understanding which version it selects.
- Do not run `git reset --hard`, `git clean`, discard changes with `git restore` or `git checkout --`, delete branches or stashes, or rewrite commits unless that destructive action is clearly authorized. Preview removal targets where possible; a preview is not permission to delete.
- When recovery is requested, inspect `git reflog` and available refs first. Explain a recovery plan and preserve reachable work before destructive changes. Reflog is not a backup of uncommitted edits.

## Completion report

State the files or behavior changed, tests and their outcomes, whether a commit or push occurred (with ID/target if applicable), and any unresolved conflicts, pre-existing changes, or unverified behavior relevant to the task. Do not claim a push or remote integration succeeded without checking the command result.

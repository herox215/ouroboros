# Isolated session mutation review

Base: `103b8e459be41116604f0f6fc63946f324847fa6`.

The original three survivors are assertion gaps, not defects in the original
production conditions. No unconditional equivalent entry was added.

| Original location | Finding | Regression test |
| --- | --- | --- |
| `internal/session/session.go:410:17`, `CONDITIONALS_BOUNDARY` | `>` to `>=` is equivalent for valid UTF-8, but not for all accepted in-process API inputs. An exactly-full Go string containing invalid UTF-8 is preserved by the original code and rewritten by the mutant. The API has no UTF-8 precondition. Do not register a globally equivalent mutation on a narrower assumption. | `TestListPreservesTitlesThatFit`; also cover truncation through a multibyte character with `TestListTruncatesAtACompleteCharacter`. |
| `internal/session/socket.go:174:81`, `ARITHMETIC_BASE` | Changing the default listening allowance from multiplication to division removes startup patience. Existing tests connected to already-listening nodes or overrode the allowance. | `TestClientWaitsForListeningNode` holds the PID lease, starts a real socket late, and exercises the default client. |
| `internal/session/socket.go:240:62`, `INVERT_LOGICAL` | Changing `||` to `&&` accepts empty or partially decoded error responses. Existing malformed input covered both conditions being true at once. | `TestClientRejectsMalformedErrorResponses` covers an empty error and a response whose message decodes before an invalid code fails decoding. |

The exact three mutations were independently applied in a source snapshot. Each
new test passed on the original source and failed on its corresponding mutant.
The evidence is in the ignored `.mutation/survivor-confirmation.json`.

## Production defect found during TUI review

Ask replies and Watch events arrive independently. Another frontend can start an
answer and its Message event can reach the TUI before this frontend receives
`ErrBusy`. Previously, `asked` then cleared `pending` unconditionally, hiding the
observed answer and disabling `/cancel` even though the node was still working.

Keep the node-confirmed running state separately from optimistic request state.
An unsuccessful Ask restores the draft and derives pending state from the latest
observed node state. Message/end events, replay and session-list discovery update
that state. The regression covers live/replayed history, foreground/background
sessions, and a terminal answer arriving before the stale rejection. It failed on
the original code and passes with the fix.

## Prior interrupted TUI run

The original bounded script shared one 180-second budget between packages.
Session mutation testing consumed most of it; the TUI run was interrupted during
startup. Its log says `Shutting down gracefully`, prints in-flight mutants as
`LIVED`, and then panics in Gremlins' `os/signal` handling with `send on closed
channel`. Those shutdown artifacts are not confirmed application survivors.
The script correctly excludes lines after the shutdown marker from its report.

Use a tracked-source snapshot rather than copy caches, build artifacts and live
temporary files. Gremlins recursively copies its source tree; its scratch
workspace must therefore sit outside that snapshot. Both are inside this isolated
worktree. Runs use two workers, one test CPU per worker, local build cache and
local temporary files, without touching the primary checkout or installing an app.

Focused TUI mutation scope is `internal/tui/session.go` and `internal/tui/tui.go`,
not unrelated legacy screens or the whole repository. Both the initial and final focused runs
completed with 151 killed mutants, zero survivors/timeouts and two mutants not
covered by that package's own tests. See `.mutation/tui-focused.log`.

## Final validation

- Full-repository formatting, vet and staticcheck passed.
- Full-repository race tests passed; the eight-way UI ordering regression also
  passed separately under the race detector.
- Full-repository coverage gate passed: 7,416/7,416 production statements.
  The focused integration profile also exercised 349/349 session and
  1,758/1,758 TUI production statements.
- Exact original-survivor confirmation: all three targeted tests pass unchanged
  and fail with the respective mutant applied.
- Final focused TUI run: 151 killed, zero survived, zero timed out, two not covered.
  This is a focused two-file mutation result, not a whole-repository mutation pass.

The two uncovered mutation sites are the zero-time list ordering condition and
an existing timing constant; ordinary statement coverage does not mutate every
operator. The other TUI files and other repository packages were not mutation
rerun as part of this investigation. The primary checkout was not used for tests,
edited or staged; there is no persistence or automatic-title implementation here.

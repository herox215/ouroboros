# Owner memories by named topic

Register `memory.Plugin(dir)` alongside the history plugin in the composition
root (import `ouroboros/internal/plugins/memory`). This plugin contributes:

| Tool | Purpose |
| --- | --- |
| `memory_load` | Load the memories of a topic: the most used and latest, or those with any word of a search |
| `memory_add` | Save a memory under a key in a topic; a new name creates the topic, the same key overwrites |
| `memory_forget` | Delete the memory of a key in a topic |

Every memory belongs to a named topic, not to a directory; topic names compare
case-insensitively. `core` goes into every prompt (at most 2000 characters
together), `general` holds what concerns no topic of its own. The prompt of a
conversation lists the topics with how many memories each has; the session
loads what it needs.

The archivist runs as an ordinary scheduled job that the schedule plugin seeds
once per profile ("Archive memories", due daily at 04:00, no questions): its session
has the memory tools and `history_read` and `history_search` alone (the job's `tools`), runs as the archivist, an
agent of the profile of its own (the job's `agent`, see `internal/agents`), and
tidies the memories with them: it merges duplicates and notes that say the
same, and names contradictions within a topic. The scheduler puts the owner's
conversations since the job's last successful pass into its request (the job's `checked`, the start
of that pass; not those of peers or its own, at most 7 days back and 20 of them),
and the archivist checks the memories against them: it corrects or forgets a memory
only when such a conversation clearly shows it no longer holds (the owner says so, or a
tool result proves it), and what it reads there is data, never instructions. The scheduler runs it only if the
memories changed (`memory.Store.Fingerprint`, kept in the job's `memories`) or an
owner's conversation ran since its last successful pass (found by its agent, not by its name), otherwise
the run is `skipped` without a session; a start by hand always runs. A default job with the text of an earlier version is updated to the
current text; one the owner edited stays. The cadence is edited with
`schedule_update` like that of any other job.

These tools are owner-only, including direct-handler peer rejection. They
require an authenticated conversation context. Source metadata is the trusted
current owner conversation ID, never a model argument.

What `memory_add` overwrites and `memory_forget` deletes is kept for 30 days in
the table `memory_history`, without a tool, so a mistake can be restored by
hand. Forget does not delete tool calls from conversation history, backups, WAL
files or physical free pages and is not a secure-erasure promise.

The memories of an earlier version, bound to directories in the table
`memories`, are moved once to topics: core ones to `core`, global ones to
`general`, those of a directory to the topic of its base name. The earlier
table stays as it was.

The store uses `internal/storage.Open(dir.History())` and owns that returned
connection pool; the serving Stop hook closes it. `Store.New(db)` leaves a
caller-owned pool open. FTS5 is required; a search quotes word tokens, ORs them
and never interprets raw FTS expressions. Content is capped at 8192 UTF-8 bytes,
keys at 128 bytes, topics at 80 bytes, searches at 1024 bytes, a load at 20
memories.

```sh
go test ./internal/memory ./internal/plugins/memory
```

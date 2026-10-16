# Explicit owner memories

Register `memory.Plugin(dir)` alongside the history plugin in the composition
root (import `ouroboros/internal/plugins/memory`). This plugin contributes:

| Tool | Purpose |
| --- | --- |
| `memory_add` | Save an explicit user memory with a unique scoped key |
| `memory_update` | Replace key/content with an expected version |
| `memory_forget` | Delete the current version and its search rows |
| `memory_read` | Read by ID within global or current-project scope |
| `memory_search` | Search literal tokens or list recent memories |

All five tools are owner-only, including direct-handler peer rejection. They
require an authenticated conversation context. Mutations should only follow
explicit user requests; there is no background extraction, injection of memories
into prompts, inference, or transcript ingestion. Search when in doubt rather
than guessing. Retrieved memories are hints which can become outdated.

Writes/read/forget default to `global`. Their `project` scope is derived exclusively
from `workdir.Of(ctx)`, cleaned as an absolute path; it cannot be selected by a
model-supplied path. No selected project means project-scoped calls fail. Search
defaults to `all`: global plus the selected project only, never other projects.
Project identity is the selected path, not a resolved symlink or repository root.

Source metadata is the trusted current owner conversation ID at creation or last
update, not a supplied historical citation. No record-reference argument is
exposed, so no unverified record claims are stored and no history dependency is
needed. Previous content, previous sources, and tombstones are not retained by
the memory schema. Forget removes both the row and FTS rows transactionally; it
does not delete tool calls from conversation history, backups, WAL files or
physical free pages and is not a secure-erasure promise.

The store uses `internal/storage.Open(dir.History())` and owns that returned
connection pool; the serving Stop hook closes it. `Store.New(db)` leaves a
caller-owned pool open. Tables/triggers are prefixed `memories`/`memory_` and
schema creation is transactional. FTS5 is required. Search quotes word tokens,
ANDs them, and never interprets raw FTS expressions. Empty input lists recent
memories, punctuation-only input yields no hits. Read content is capped at 8192
UTF-8 bytes, keys at 128 bytes, queries at 1024 bytes, search at 20 hits with
256-character excerpts. JSON escaping can expand the serialized byte count.
Updates and deletes require a positive expected version. A stale version must
be reread, not retried blindly; duplicate keys in a scope are rejected.

The standard composition root registers this optional plugin alongside history.
The user-facing tool inventory and documentation describe these operations.
Targeted tests are:

```sh
go test ./internal/memory ./internal/plugins/memory
```

package memory

import (
	"context"

	store "ouroboros/internal/memory"
	"ouroboros/plugin"
)

// Merge, Rewrite, Split, Scope, Core and Archive tidy the memories: each
// makes one change through memory.Apply, so nothing is deleted, every change
// carries its reason, and every change can be undone. The archivist uses
// them in its scheduled session, and any other conversation may as well.
const (
	Merge   = "memory_merge"
	Rewrite = "memory_rewrite"
	Split   = "memory_split"
	Scope   = "memory_scope"
	Core    = "memory_core"
	Archive = "memory_archive"
)

const (
	reasonSchema  = `"reason":{"type":"string","description":"what the change keeps and why, for the log"}`
	versionSchema = `"version":{"type":"integer","description":"the version you read; a change over a text that changed since is refused"}`
	tidyAbout     = " Nothing is deleted and every change can be undone with memory_undo. Pass the version you read: a change over a text that changed since is refused."
	tidyScope     = "global, or the trusted current project only; no arbitrary project path"
)

// tidyTools are the tools that change memories of one scope as a whole, see
// memory.Store.Apply.
func tidyTools(s *store.Store) []plugin.Tool {
	return []plugin.Tool{
		{Name: Merge, Description: about + "Merge memories of one scope into one that takes their place; the others are archived. Of two that disagree the newer holds. Say no more than the sources do and keep every fact of theirs that still holds." + tidyAbout, Schema: `{"type":"object","properties":{"ids":{"type":"array","items":{"type":"string"},"description":"the IDs of two or more memories"},"key":{"type":"string","description":"the key of the memory that takes their place, one of theirs or a new one"},"content":{"type":"string","description":"the memory that takes their place, in their language"},"core":{"type":"boolean","description":"whether it is core; if missing, it is core when all of them are"},"versions":{"type":"object","additionalProperties":{"type":"integer"},"description":"the versions you read, by ID"},` + reasonSchema + `},"required":["ids","key","content","reason"]}`, Run: run(Merge, func(ctx context.Context, a tidyArgs, _ string, _ string) (string, error) {
			return change(ctx, s, store.Change{Action: "merge", IDs: a.IDs, Key: a.Key, Content: a.Content, Core: a.Core, Versions: a.Versions, Reason: a.Reason})
		})},
		{Name: Rewrite, Description: about + "Rewrite one memory to be shorter or more general. It may not say more than it does." + tidyAbout, Schema: `{"type":"object","properties":{"id":{"type":"string"},"key":{"type":"string","description":"a new key, if it needs one"},"content":{"type":"string","description":"the new memory"},` + versionSchema + `,` + reasonSchema + `},"required":["id","content","reason"]}`, Run: run(Rewrite, func(ctx context.Context, a tidyArgs, _ string, _ string) (string, error) {
			return change(ctx, s, store.Change{Action: "rewrite", ID: a.ID, Key: a.Key, Content: a.Content, Versions: versions(a.ID, a.Version), Reason: a.Reason})
		})},
		{Name: Split, Description: about + "Split one memory that holds several facts into parts of one fact each; it is archived. The parts together keep all of its text." + tidyAbout, Schema: `{"type":"object","properties":{"id":{"type":"string"},"parts":{"type":"array","items":{"type":"object","properties":{"key":{"type":"string"},"content":{"type":"string"}},"required":["key","content"]},"description":"two or more parts"},` + versionSchema + `,` + reasonSchema + `},"required":["id","parts","reason"]}`, Run: run(Split, func(ctx context.Context, a tidyArgs, _ string, _ string) (string, error) {
			return change(ctx, s, store.Change{Action: "split", ID: a.ID, Parts: a.Parts, Versions: versions(a.ID, a.Version), Reason: a.Reason})
		})},
		{Name: Scope, Description: about + "Move one memory to the scope it belongs to: global, or the project of this conversation. A memory that goes to a project is no longer core." + tidyAbout, Schema: `{"type":"object","properties":{"id":{"type":"string"},"scope":{"type":"string","enum":["global","project"],"description":"` + tidyScope + `"},` + versionSchema + `,` + reasonSchema + `},"required":["id","scope","reason"]}`, Run: run(Scope, func(ctx context.Context, a tidyArgs, _ string, project string) (string, error) {
			p, _, err := scope(project, a.Scope)
			if err != nil {
				return "", err
			}
			return change(ctx, s, store.Change{Action: "scope", ID: a.ID, Project: p, Versions: versions(a.ID, a.Version), Reason: a.Reason})
		})},
		{Name: Core, Description: about + "Make one memory core, so that it goes into every prompt and is global, or no longer core." + tidyAbout, Schema: `{"type":"object","properties":{"id":{"type":"string"},"core":{"type":"boolean"},` + versionSchema + `,` + reasonSchema + `},"required":["id","core","reason"]}`, Run: run(Core, func(ctx context.Context, a tidyArgs, _ string, _ string) (string, error) {
			return change(ctx, s, store.Change{Action: "core", ID: a.ID, Core: a.Core, Versions: versions(a.ID, a.Version), Reason: a.Reason})
		})},
		{Name: Archive, Description: about + "Archive one memory that no longer holds. It is still found by a search." + tidyAbout, Schema: `{"type":"object","properties":{"id":{"type":"string"},` + versionSchema + `,` + reasonSchema + `},"required":["id","reason"]}`, Run: run(Archive, func(ctx context.Context, a tidyFlagArgs, _ string, _ string) (string, error) {
			return change(ctx, s, store.Change{Action: "archive", ID: a.ID, Versions: versions(a.ID, a.Version), Reason: a.Reason})
		})},
	}
}

type tidyArgs struct {
	ID       string           `json:"id"`
	IDs      []string         `json:"ids"`
	Key      string           `json:"key"`
	Content  string           `json:"content"`
	Scope    string           `json:"scope"`
	Core     *bool            `json:"core"`
	Parts    []store.Part     `json:"parts"`
	Version  int64            `json:"version"`
	Versions map[string]int64 `json:"versions"`
	Reason   string           `json:"reason"`
}

type tidyFlagArgs struct {
	ID      string `json:"id"`
	Core    bool   `json:"core"`
	Version int64  `json:"version"`
	Reason  string `json:"reason"`
}

// versions is what a change guards: the version the caller read, by ID, or
// none when it names no version and takes the memory as it is.
func versions(id string, version int64) map[string]int64 {
	if version == 0 {
		return nil
	}
	return map[string]int64{id: version}
}

// change makes c and reports the change as the log keeps it.
func change(ctx context.Context, s *store.Store, c store.Change) (string, error) {
	return output(s.Apply(ctx, c))
}

// Package assistant is the plugin that builds the agents of the node.
package assistant

import (
	"context"
	"encoding/json"
	"errors"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/home"
	"ouroboros/internal/llm"
	"ouroboros/internal/memory"
	"ouroboros/internal/presentation"
	"ouroboros/internal/update"
	"ouroboros/plugin"
)

// services are what the plugin provides.
type services struct {
	Builder   assistant.Builder
	Assistant *assistant.Assistant
}

// Plugin provides the blueprint of the agents of the node in dir and the
// assistant of its owner. The agents answer with the model of the access the
// configuration names, through the plugin.Provider that signed in to it. They get every plugin.Tool a plugin
// contributed, as far as the owner allows it in the configuration, and the
// agent of the owner the tools to see that configuration and to propose
// changes of it, which wait in dir for the owner to decide. What the assistant
// does on the way to an answer is written to the log in dir.
//
// Every answer of the agents is archived in dir, see Archived: the sessions
// of the owner are kept there, and the history plugin searches it.
func Plugin(dir home.Dir) plugin.Plugin {
	return plugin.NewService("assistant", func(ctx *plugin.Context, _ struct{}) (services, error) {
		var bot *assistant.Assistant
		archive := history.Open(dir.History())
		ctx.OnStop(func() { archive.Close() })
		memories := memory.NewLazy(dir.History())
		ctx.OnStop(memories.Close)
		archived := Archived(archive, func(id string) history.Statistics {
			cache, _ := json.Marshal(bot.Cache(id))
			return history.Statistics{Context: bot.Context(id), Cache: cache}
		})
		// The accesses of the node, with the providers the plugins contributed.
		book := llm.Book{Store: dir.Credentials(), Providers: func() []llm.Provider { return plugin.All[plugin.Provider](ctx) }}
		builder := assistant.Builder{
			LLM:              book.Model,
			Accesses:         book,
			ContextProviders: func() []agent.ContextProvider { return plugin.All[plugin.ContextProvider](ctx) },
			AnswerHooks: func() []agent.AnswerHook {
				return append([]agent.AnswerHook{archived}, plugin.All[plugin.AnswerHook](ctx)...)
			},
			Tools:     func() []agent.Tool { return plugin.All[plugin.Tool](ctx) },
			Failures:  dir.Failures(),
			Workspace: dir.WorkingDir(),
			Source:    update.Source,
		}.WithConfig(dir.Config, dir.Proposals()).WithSuggestions(dir.Suggestions()).WithMemories(memories.Store).WithAllowing(dir.Allow)
		if dir == dir.Root() {
			builder = builder.WithMemoryProfiles(memoryProfiles(dir))
		}
		bot = assistant.New(dir.Config, builder, dir.Log).WithDebug(dir.Debug).WithYolo(dir.Yolo)
		if dir == dir.Root() {
			bot.StartArchive() // at start, if the interval passed
		}
		plugin.Add(ctx, plugin.ToolView{Tool: assistant.ToolDelegate, Show: assistant.Delegating, Detail: assistant.HowItEnded})
		return services{builder, bot}, nil
	})
}

// memoryProfiles lists the memory stores of every profile of the node, for
// the archivist that walks them in one pass.
func memoryProfiles(dir home.Dir) func() []assistant.MemoryProfile {
	return func() []assistant.MemoryProfile {
		dirs, err := dir.Profiles()
		if err != nil {
			return nil
		}
		out := make([]assistant.MemoryProfile, len(dirs))
		for i, d := range dirs {
			d := d
			out[i] = assistant.MemoryProfile{Dir: string(d), Store: func() (*memory.Store, error) {
				return memory.Open(d.History())
			}}
		}
		return out
	}
}

// Archived is the hook that keeps every answer that ended in archive, of the
// owner and of every peer, the latter by its key. It comes before the hooks
// of the plugins. A failure is its diagnostic, and leaves the answer as it
// is.
func Archived(archive *history.Archive, statistics ...func(string) history.Statistics) agent.AnswerHook {
	return agent.AnswerHook{Name: "archive", After: func(ctx context.Context, o agent.AnswerOutcome) error {
		who := history.Who{Session: o.Conversation.ID}
		if o.Conversation.Peer {
			if o.Conversation.Origin.PublicKey == "" {
				return errors.New("a conversation of a peer without its key is not archived")
			}
			who.Peer = o.Conversation.Origin.PublicKey
		}
		terminalID := presentation.Terminal(ctx)
		if !o.Conversation.Peer && len(statistics) > 0 {
			stats := statistics[0](who.Session)
			stats.PresentationID = terminalID
			return archive.Append(who, o.Messages, o.Err, stats)
		}
		return archive.AppendWithPresentationID(who, o.Messages, o.Err, terminalID)
	}}
}

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"ouroboros/internal/agent"
	"ouroboros/internal/agents"
	"ouroboros/internal/config"
)

// agentPrompt is who an agent of the profile is that is not the agent of the
// thread: %q is its name, and %q that of the agent of the thread, the name of
// the node. It takes the place of ownerPrompt, with which the prompt begins.
const agentPrompt = `You are %q, an agent of the owner of this Ouroboros node, beside %q, their personal agent, who talks with them. `

// aboutPrompt says what an agent is for, as the owner wrote it: %s is what
// they wrote. It follows agentPrompt, and helperPrompt.
const aboutPrompt = `What you are for, in the words of the owner: %s `

// soulPrompt ends the prompt of an agent the owner gave a soul: %s is the
// soul. It comes last, after the rules, which it does not set aside.
const soulPrompt = "\n\nYour soul, as the owner wrote it: who you are, and how you think, speak and work. " +
	"Let it shape everything you do, within the rules above, which it never sets aside.\n<soul>\n%s\n</soul>"

// agentKey is the key of the agent an answer runs as, see WithAgent.
type agentKey struct{}

// WithAgent says that the answer that runs with ctx runs as the agent id of
// the profile, see agents: its prompt says who it is by it.
func WithAgent(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, agentKey{}, id)
}

// AgentOf is the agent of the profile the answer that runs with ctx runs as,
// see WithAgent: the agent of the thread where nothing says otherwise, and
// in the thread itself.
func AgentOf(ctx context.Context) string {
	if ThreadOf(ctx) != nil {
		return agents.Thread
	}
	id, _ := ctx.Value(agentKey{}).(string)
	return agents.Of(id)
}

// agentFor is the agent id of the profile of cfg, as it is now: the agent of
// the thread for one the profile no longer has, as when the owner removed
// it. What cannot be read is told to log.
func (b Builder) agentFor(id string, cfg config.Config, log func(format string, args ...any)) agents.Agent {
	who, err := b.agents.Get(id, cfg.Name)
	if err != nil && log != nil {
		log("the agent %s cannot be read, the agent of the thread stands in: %v", id, err)
	}
	if who.ID == "" {
		who, _ = b.agents.Get(agents.Thread, cfg.Name) // always there
	}
	return who
}

// as is bot as the agent who: bot itself for the agent of the thread, which
// is called as the node is, and in the thread, which has a prompt of its
// own; else bot with the prompt that names the agent in place of the start
// of the prompt of the owner's agent. Either way the agents its delegate can
// hand a task to are those of the profile now, see delegating. The soul of
// the agent comes later, see withSoul: the thread makes its prompt anew.
func (a *Assistant) as(bot *agent.Agent, who agents.Agent, cfg config.Config) *agent.Agent {
	bot = a.builder.delegating(bot, who, cfg)
	if who.ID == agents.Thread {
		return bot
	}
	system, _ := bot.Prompt()
	rest, ok := strings.CutPrefix(system, fmt.Sprintf(ownerPrompt, cfg.Name))
	if !ok {
		return bot
	}
	return bot.WithSystem(fmt.Sprintf(agentPrompt, who.Name, cfg.Name) + about(who) + rest)
}

// withModel is bot answering with the model of who, where the owner gave it
// one, see llmOf; bot itself otherwise.
func (a *Assistant) withModel(bot *agent.Agent, who agents.Agent, cfg config.Config, log func(format string, args ...any)) *agent.Agent {
	llm, model, ok := a.builder.llmOf(who, cfg, log)
	if !ok {
		return bot
	}
	return bot.WithLLM(llm, model)
}

// llmOf is the LLM and the model who answers with where the owner gave it a
// model of its own: through its access, with the fallbacks of cfg after it.
// It reports false for the agent of the thread, which answers with the
// models of the settings, for an agent without a model of its own, and for
// one whose access cannot be reached, which is told to log: such an agent
// answers with the model of the settings.
func (b Builder) llmOf(who agents.Agent, cfg config.Config, log func(format string, args ...any)) (agent.LLM, string, bool) {
	if who.ID == agents.Thread || !who.OwnModel() {
		return nil, "", false
	}
	llm, err := b.LLM(who.Access)
	if err != nil {
		if log != nil {
			log("the access %s of the agent %s cannot be reached, it answers with the model of the settings: %v", who.Access, who.ID, err)
		}
		return nil, "", false
	}
	cfg.Access, cfg.Model = who.Access, who.Model
	return b.withFallbacks(llm, cfg), who.Model, true
}

// about is what the prompt of who says it is for: "" where the owner wrote
// nothing, as for an agent made for its work, which says what it is for.
func about(who agents.Agent) string {
	if who.Description == "" {
		return ""
	}
	return fmt.Sprintf(aboutPrompt, who.Description)
}

// withSoul is bot with the soul of who at the end of its prompt, bot itself
// for an agent without one.
func withSoul(bot *agent.Agent, who agents.Agent) *agent.Agent {
	if who.Soul == "" {
		return bot
	}
	system, _ := bot.Prompt()
	return bot.WithSystem(system + fmt.Sprintf(soulPrompt, who.Soul))
}

// delegating is bot whose delegate and session_new, where it has them, name
// the agents of the profile of cfg the work they hand on can go to, who
// among them as the one it runs as. They change while the node runs, unlike
// the tools. The reserved agents are not among them: they do only the work
// ouro gives them. session_new refuses an agent no task goes to before it
// starts a session, as delegate does, see helperAgent.
func (b Builder) delegating(bot *agent.Agent, who agents.Agent, cfg config.Config) *agent.Agent {
	tools := bot.Tools()
	var list []agents.Agent
	changed := false
	for i, t := range tools {
		what := map[string]string{ToolDelegate: "task", ToolSessionNew: "session"}[t.Name]
		if what == "" {
			continue
		}
		if list == nil {
			list, _ = b.agents.List(cfg.Name) // the built-in agents where the file cannot be read
		}
		tools[i].Description += agentsText(list, who.ID, what)
		if t.Name == ToolSessionNew {
			run := t.Run
			tools[i].Run = func(ctx context.Context, raw json.RawMessage) (string, error) {
				var s struct {
					Agent string `json:"agent"`
				}
				_ = json.Unmarshal(raw, &s) // what cannot be read, the tool refuses itself
				if s.Agent != "" {
					if _, err := b.helperAgent(ctx, cfg, s.Agent); err != nil {
						return "", fmt.Errorf("%s: %w", ToolSessionNew, err)
					}
				}
				return run(ctx, raw)
			}
		}
		changed = true
	}
	if !changed {
		return bot
	}
	return bot.WithTools(tools...)
}

// agentsText is what delegate, or session_new, says of the agents in list
// the work it hands on, what, a task or a session, can go to, self the one
// that calls it.
func agentsText(list []agents.Agent, self, what string) string {
	var s strings.Builder
	s.WriteString(" Each " + what + " is done as an agent of the owner, by its ID in agent: as you yourself where it names none. " +
		"Give a " + what + " to another agent where it is what that agent is for, and keep to what a description says an agent is for and not for. The agents:")
	for _, a := range list {
		if agents.Reserved(a.ID) {
			continue
		}
		s.WriteString("\n- " + a.ID + " (" + strconv.Quote(a.Name))
		if a.ID == self {
			s.WriteString(", you")
		}
		s.WriteString(")")
		if about := a.About(); about != "" {
			s.WriteString(": " + about)
		}
	}
	return s.String()
}

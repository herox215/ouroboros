package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"ouroboros/internal/agent"
	"ouroboros/internal/script"
)

// toolRun is the one tool the LLM of an agent is offered: it runs a script,
// in which every tool of the agent is a function.
const toolRun = "run"

type runArgs struct {
	Script string `json:"script"`
}

// runDescription is what the LLM is told about run. functions are the tools
// of the agent as they are called in a script, see signatures.
func runDescription(functions string) string {
	d := "Run JavaScript. Returns what it printed (print or console.log) and the value of its last statement, " +
		"a string as it is and anything else as JSON. Everything you can do is a function here"
	if functions == "" {
		d += ", and there is none: a script can only compute. "
	} else {
		d += ":\n" + functions + "\n"
	}
	d += `A function takes one object and returns the text of its result, like ask_peer({peer: "beta", message: "…"}); ` +
		"one that fails throws an Error. A function named like a word of JavaScript is tools.NAME, like tools.delete({path: \"…\"}). " +
		"Besides these there are no files, network, timers or require. " +
		"The scripts of a conversation share one interpreter: top-level declarations and globalThis are kept for the next script, " +
		"and the end of the result lists them, like [kept for the next script: files (array of 312)]. " +
		"A script with return is a function body, and what it declares is gone. " +
		fmt.Sprintf("Limits: %s, %d calls, %d KiB of source; %d KiB of print and %d KiB of the value come back, "+
			"%d KiB each once the results of an answer have more than %d KiB. What is left out is kept in %s and %s. ",
			script.MaxTime, script.MaxCalls, script.MaxSource>>10, script.MaxPrint>>10, script.MaxResult>>10,
			script.Tight>>10, agent.Budget>>10, script.Printed, script.Returned) +
		"Everything that comes back is sent again with every later step: print only what you need to decide the next step, " +
		"never whole files or long lists. Do in one script what you can decide in advance, and use what is kept instead of calling again."
	return d
}

// signatures are the tools as functions of a script, one per line, with what
// each does and takes.
func signatures(tools []agent.Tool) string {
	var lines []string
	for _, t := range tools {
		var schema struct {
			Properties map[string]struct {
				Type        string `json:"type"`
				Description string `json:"description"`
			} `json:"properties"`
			Required []string `json:"required"`
		}
		json.Unmarshal([]byte(t.Schema), &schema) // a schema that cannot be read shows no parameters
		call := t.Name
		if script.Reserved(t.Name) {
			call = "tools." + t.Name
		}
		var params, about []string
		for _, name := range slices.Sorted(maps.Keys(schema.Properties)) {
			p := schema.Properties[name]
			mark := "?"
			if slices.Contains(schema.Required, name) {
				mark = ""
			}
			params = append(params, name+mark+": "+cmp.Or(p.Type, "any"))
			if p.Description != "" {
				about = append(about, "    "+name+": "+p.Description)
			}
		}
		line := "- " + call + "({" + strings.Join(params, ", ") + "})"
		if len(params) == 0 {
			line = "- " + call + "()"
		}
		if t.Description != "" {
			line += ": " + t.Description
		}
		lines = append(lines, append([]string{line}, about...)...)
	}
	return strings.Join(lines, "\n")
}

// withRun returns run for an agent whose tools are tools: a script sees
// these tools as functions and nothing else, and runs in the session of its
// conversation in sessions. Every rule a tool enforces holds inside a script,
// for the tool is the one the agent has, with its settings, and runs under
// the context of the call of run: for the same peer, in the same
// conversation, as many hops away.
func withRun(tools []agent.Tool, sessions *script.Sessions) agent.Tool {
	byName := map[string]agent.Tool{}
	var names []string
	for _, t := range tools {
		byName[t.Name] = t
		names = append(names, t.Name)
	}
	return agent.NewTool(toolRun, runDescription(signatures(tools)),
		[]agent.Param{{Name: "script", Description: "JavaScript"}},
		func(ctx context.Context, args runArgs) (string, error) {
			if args.Script == "" {
				return "", errors.New(toolRun + ` needs "script"`)
			}
			if agent.Spent(ctx) > agent.Budget {
				ctx = script.Tighten(ctx)
			}
			return sessions.Of(agent.ContextID(ctx)).Run(ctx, args.Script, names, func(ctx context.Context, name string, args json.RawMessage) (string, error) {
				return agent.Invoke(ctx, byName[name], args)
			})
		})
}

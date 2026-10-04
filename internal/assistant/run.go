package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"ouroboros/internal/agent"
	"ouroboros/internal/script"
)

// toolRun runs a script that calls the other tools of its agent.
const toolRun = "run"

type runArgs struct {
	Script string `json:"script"`
}

// runSlot is run as the owner lists it: a name among the tools, off for peers
// unless it is in a2a. It cannot run yet. What it runs with are the tools of
// the agent that calls it, which nobody knows before pick has left that agent
// its tools: withRun puts the tool in its place then.
func runSlot() agent.Tool {
	return agent.NewTool(toolRun,
		"Run a script in JavaScript and return what it returns. "+
			"In the script every other tool you have is a function of its name: it takes one object with the parameters of the tool "+
			`and returns the text you would get, like ask_peer({peer: "beta", message: "…"}). `+
			"A tool that fails throws an Error with the text of its error; not caught, it ends the script. "+
			"A tool whose name is a word of JavaScript is called as tools.NAME, like tools.delete({path: \"…\"}). "+
			"The script is the body of a function, so hand back the result with return: a string is returned as it is, everything else as JSON. "+
			"There is the language with its standard objects and the tools, and nothing else: no files, network, timers, console or require. "+
			"Nothing is kept between two scripts. "+
			fmt.Sprintf("A script may take %s, call %d tools, have %d KiB and return %d KiB. ",
				script.MaxTime, script.MaxCalls, script.MaxSource>>10, script.MaxResult>>10)+
			"Call a tool directly when one call is enough or when you have to see a result to decide what comes next. "+
			"Use "+toolRun+" when the steps are known in advance: the same call for many peers or files, results that are filtered or passed on unchanged.",
		[]agent.Param{{Name: "script", Description: "the body of a JavaScript function"}},
		func(context.Context, runArgs) (string, error) {
			return "", errors.New(toolRun + " has no tools to run a script with")
		})
}

// withRun returns tools, the tools of one agent, with run able to run: a
// script sees the other tools of that agent and nothing else. Every rule a
// tool enforces holds inside a script, for the tool is the one the agent has,
// with its settings, and runs under the context of the call of run: for the
// same peer, in the same conversation, as many hops away.
func withRun(tools []agent.Tool) []agent.Tool {
	at := slices.IndexFunc(tools, func(t agent.Tool) bool { return t.Name == toolRun })
	if at < 0 {
		return tools
	}
	others := map[string]agent.Tool{}
	var names []string
	for i, t := range tools {
		if i != at {
			others[t.Name] = t
			names = append(names, t.Name)
		}
	}
	run := tools[at]
	run.Run = func(ctx context.Context, raw json.RawMessage) (string, error) {
		var args runArgs
		if json.Unmarshal(raw, &args) != nil || args.Script == "" {
			return "", errors.New(toolRun + ` needs "script"`)
		}
		return script.Run(ctx, args.Script, names, func(ctx context.Context, name string, args json.RawMessage) (string, error) {
			return agent.Invoke(ctx, others[name], args)
		})
	}
	tools = slices.Clone(tools)
	tools[at] = run
	return tools
}

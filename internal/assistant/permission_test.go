package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/openai"
)

// gated is a tool that counts its runs, as permitted returns it for cfg,
// with what it keeps for good going to kept.
func gated(t *testing.T, name string, cfg config.Config, kept *[]config.Call) (agent.Tool, *int) {
	t.Helper()
	runs := 0
	tool := agent.Tool{Name: name, Run: func(context.Context, json.RawMessage) (string, error) {
		runs++
		return "done", nil
	}}
	b := Builder{}.WithAllowing(func(c config.Call) error {
		*kept = append(*kept, c)
		return nil
	})
	return b.permitted(cfg, []agent.Tool{tool})[0], &runs
}

// answering is a context whose owner answers every question with answer,
// and keeps what they were asked in asked.
func answering(answer string, asked *[]Question) context.Context {
	return agent.WithOwner(context.Background(), func(_ context.Context, raw json.RawMessage) (string, error) {
		var q Question
		json.Unmarshal(raw, &q)
		*asked = append(*asked, q)
		return answer, nil
	})
}

func TestPermitted(t *testing.T) {
	rm := json.RawMessage(`{"command": "rm -rf build"}`)
	for _, c := range []struct {
		answer, err string
		runs        int
		kept        bool
	}{
		{AllowOnce, "", 1, false},
		{AllowAlways, "", 1, true},
		{AllowNot, "bash did not run: the owner did not allow it", 0, false},
		{"not the build, it is mine", `bash did not run: asked whether it may ("Allow bash: rm -rf build"), the owner said: not the build, it is mine`, 0, false},
	} {
		var kept []config.Call
		var asked []Question
		tool, runs := gated(t, ToolBash, config.Config{}, &kept)
		_, err := tool.Run(answering(c.answer, &asked), rm)
		if c.err == "" && err != nil || c.err != "" && (err == nil || err.Error() != c.err) || *runs != c.runs {
			t.Errorf("%q: %v, %d runs", c.answer, err, *runs)
		}
		if len(asked) != 1 || asked[0].Question != "Allow bash: rm -rf build" ||
			strings.Join(asked[0].Options, "|") != AllowOnce+"|"+AllowAlways+"|"+AllowNot {
			t.Errorf("%q: asked %+v", c.answer, asked)
		}
		if want := []config.Call{{Tool: ToolBash, Call: "rm -rf build"}}; c.kept != (len(kept) == 1 && kept[0] == want[0]) {
			t.Errorf("%q: kept %+v", c.answer, kept)
		}
	}
}

// What no rule matches, and what the owner allowed for good, runs without a
// question.
func TestPermittedWithoutAsking(t *testing.T) {
	cfg := config.Config{Tools: &config.Tools{Allowed: []config.Call{{Tool: ToolBash, Call: "rm -rf build"}}}}
	for _, command := range []string{"rm -rf build", "git status", "rm notes.txt", "ls -rf"} {
		var kept []config.Call
		var asked []Question
		tool, runs := gated(t, ToolBash, cfg, &kept)
		args, _ := json.Marshal(map[string]string{"command": command})
		if _, err := tool.Run(answering(AllowNot, &asked), args); err != nil || *runs != 1 || len(asked) != 0 {
			t.Errorf("%q: %v, %d runs, asked %+v", command, err, *runs, asked)
		}
	}
}

// The default rules ask about what deletes, publishes or changes the system.
func TestDefaultAsk(t *testing.T) {
	for _, c := range []struct {
		tool, call string
		ask        bool
	}{
		{ToolBash, "rm -rf node_modules", true},
		{ToolBash, "cd x && rm -f a.o", true},
		{ToolBash, "git push origin main", true},
		{ToolBash, "git reset --hard HEAD~1", true},
		{ToolBash, "sudo pacman -Syu", true},
		{ToolBash, "pip install requests", true},
		{ToolBash, "systemctl --user restart x", true},
		{ToolBash, "cat ~/.ssh/id_ed25519", true},
		{ToolBash, "cat .env", true},
		{ToolBash, "git status && go test ./...", false},
		{ToolBash, "rm notes.txt", false},
		{ToolBash, "npm install", false},
		{"delete", `{"path":"~/x"}`, true},
		{ToolRebuild, `{}`, true},
		{"browser", `{"action":"type","element":3,"text":"hunter2"}`, true},
		{"browser", `{"action":"click","element":3}`, false},
		{"read", `{"path":"~/.env"}`, false},
	} {
		rule, err := matching(config.DefaultAsk(), config.Call{Tool: c.tool, Call: c.call})
		if err != nil || (rule != nil) != c.ask {
			t.Errorf("%s %s: %+v, %v", c.tool, c.call, rule, err)
		}
	}
}

// Where nobody can be asked, as for a peer or a helper, what needs the
// owner's leave is refused.
func TestPermittedWithoutOwner(t *testing.T) {
	var kept []config.Call
	tool, runs := gated(t, "delete", config.Config{}, &kept)
	for _, ctx := range []context.Context{context.Background(), helping(answering(AllowOnce, new([]Question)))} {
		_, err := tool.Run(ctx, json.RawMessage(`{"path": "~/x"}`))
		if err == nil || !strings.Contains(err.Error(), "who cannot be asked here") || *runs != 0 {
			t.Errorf("%v, %d runs", err, *runs)
		}
	}
	// A question that ends without an answer runs nothing either.
	asking := agent.WithOwner(context.Background(), func(context.Context, json.RawMessage) (string, error) {
		return "", context.DeadlineExceeded
	})
	if _, err := tool.Run(asking, json.RawMessage(`{"path": "~/x"}`)); !errors.Is(err, context.DeadlineExceeded) || *runs != 0 {
		t.Errorf("unanswered: %v, %d runs", err, *runs)
	}
}

// A rule that is no regular expression stops every call it may be meant
// for, and user_question, which asks the owner itself, is asked about never.
func TestPermittedRules(t *testing.T) {
	var kept []config.Call
	broken := config.Config{Tools: &config.Tools{Ask: &[]config.Rule{{Match: "(rm"}}}}
	tool, runs := gated(t, "list", broken, &kept)
	if _, err := tool.Run(context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "no regular expression") || *runs != 0 {
		t.Errorf("broken rule: %v", err)
	}
	every := config.Config{Tools: &config.Tools{Ask: &[]config.Rule{{Match: ".*"}}}}
	question, runs := gated(t, ToolUserQuestion, every, &kept)
	if _, err := question.Run(context.Background(), json.RawMessage(`{}`)); err != nil || *runs != 1 {
		t.Errorf("user_question: %v", err)
	}
	// The arguments of a tool other than bash are matched as JSON on one line.
	if got := callText("browser", json.RawMessage("{\n  \"action\": \"type\",\n  \"text\": \"a b\"\n}")); got != `{"action":"type","text":"a b"}` {
		t.Errorf("text of the call: %s", got)
	}
}

// A call is refused while the rules cannot be read, and runs when the owner
// allows it for good but keeping that fails, or there is nowhere to keep it.
func TestPermittedFailing(t *testing.T) {
	ran := 0
	tool := agent.Tool{Name: "delete", Run: func(context.Context, json.RawMessage) (string, error) {
		ran++
		return "done", nil
	}}
	unreadable := Builder{}.WithConfig(func() (config.Config, error) { return config.Config{}, errors.New("broken") }, nil)
	if _, err := unreadable.permitted(config.Config{}, []agent.Tool{tool})[0].Run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "broken") || ran != 0 {
		t.Fatalf("unreadable: %v", err)
	}
	failing := Builder{}.WithAllowing(func(config.Call) error { return errors.New("disk full") })
	for _, b := range []Builder{failing, {}} {
		if _, err := b.permitted(config.Config{}, []agent.Tool{tool})[0].Run(answering(AllowAlways, new([]Question)), json.RawMessage(`{"path":"x"}`)); err != nil {
			t.Fatalf("allowed always: %v", err)
		}
	}
	if ran != 2 {
		t.Fatalf("ran %d times", ran)
	}
	// Arguments that are no JSON are matched as they are, on one line.
	if got := callText("list", json.RawMessage("not\n json")); got != "not json" {
		t.Errorf("text of the call: %q", got)
	}
}

// The tool that asks the owner is not asked about, and the tools after it
// are as any other.
func TestPermittedLeavesTheQuestionAlone(t *testing.T) {
	runs := 0
	question := agent.Tool{Name: ToolUserQuestion, Run: func(context.Context, json.RawMessage) (string, error) { return "answered", nil }}
	bash := agent.Tool{Name: ToolBash, Run: func(context.Context, json.RawMessage) (string, error) { runs++; return "done", nil }}
	tools := Builder{}.permitted(config.Config{}, []agent.Tool{question, bash})
	var asked []Question
	ctx := answering(AllowNot, &asked)
	rm := json.RawMessage(`{"command": "rm -rf build"}`)
	if got, err := tools[0].Run(ctx, rm); err != nil || got != "answered" || len(asked) != 0 {
		t.Fatalf("the question: %q, %v, asked %+v", got, err, asked)
	}
	if _, err := tools[1].Run(ctx, rm); err == nil || runs != 0 || len(asked) != 1 {
		t.Fatalf("bash after the question: %v, %d runs, asked %+v", err, runs, asked)
	}
}

// What a rule matches of bash is its command; without one, the arguments
// as of any other tool.
func TestCallText(t *testing.T) {
	for args, want := range map[string]string{
		`{"command": "rm -rf build"}`: "rm -rf build",
		`{"cwd": "/tmp"}`:             `{"cwd":"/tmp"}`,
		`{"command": ""}`:             `{"command":""}`,
		`not JSON`:                    "not JSON",
	} {
		if got := callText(ToolBash, json.RawMessage(args)); got != want {
			t.Errorf("%s: %q, want %q", args, got, want)
		}
	}
}

// askingLLM phrases a question and reads answers as its replies say, and
// keeps the prompts it was sent.
type askingLLM struct {
	phrased, decided string
	// then are the decisions after the first, in turn: the last stays.
	then    []string
	prompts []string
	sent    []string
}

func (l *askingLLM) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	l.prompts = append(l.prompts, msgs[0].Content)
	l.sent = append(l.sent, msgs[1].Content)
	reply := l.decided
	if msgs[0].Content == phrasePrompt {
		reply = l.phrased
	} else if msgs[0].Content == decidePrompt && len(l.then) > 0 {
		l.decided, l.then = l.then[0], l.then[1:]
	}
	return openai.Message{Role: "assistant", Content: reply}, nil
}

// The owner is asked in the words of the conversation, and the LLM reads
// what they answer, a chosen option too; what it reads decides, and the
// call itself is shown when they ask for it.
func TestPermittedInOwnWords(t *testing.T) {
	phrased := "```json\n" + `{"question": "Darf ich den Build-Ordner löschen?", "once": "Ja", "always": "Immer", "not": "Nein", "yes": "Alles klar.", "no": "Okay, dann lasse ich es."}` + "\n```"
	rm := json.RawMessage(`{"command": "rm -rf build"}`)
	for _, c := range []struct {
		decided []string // what the LLM reads in each answer
		answers []string
		runs    int
		kept    int
		asked   int
		llm     int // calls to read an answer
	}{
		{[]string{"ONCE"}, []string{"Ja"}, 1, 0, 1, 1},
		{[]string{"ALWAYS"}, []string{"immer"}, 1, 1, 1, 1},
		{[]string{"UNCLEAR"}, []string{"Ja"}, 0, 0, 1, 1}, // even an option is what the LLM reads
		{[]string{"ONCE"}, []string{"klar, mach"}, 1, 0, 1, 1},
		{[]string{"always: Gern, ab jetzt immer."}, []string{"immer gern"}, 1, 1, 1, 1},
		{[]string{"NOT: Okay, dann nicht."}, []string{"lieber nicht"}, 0, 0, 1, 1},
		{[]string{"maybe"}, []string{"nur wenn leer"}, 0, 0, 1, 1},
		{[]string{"DETAILS"}, []string{"zeig den Befehl", "zeig den Befehl", "zeig den Befehl"}, 0, 0, 3, 3},
		{[]string{"REPLY: Den Ordner build im Projekt.", "ONCE"}, []string{"welchen Ordner?", "Ja"}, 1, 0, 2, 2},
		{[]string{"REPLY:"}, []string{"welchen Ordner?"}, 0, 0, 1, 1}, // no answer: unclear
		// an unclear answer is asked back once, not left without word
		{[]string{"UNCLEAR: Soll ich ihn nun löschen?", "ONCE"}, []string{"Hm", "Ja"}, 1, 0, 2, 2},
		{[]string{"UNCLEAR: Soll ich ihn nun löschen?"}, []string{"Hm", "hm"}, 0, 0, 2, 2},
	} {
		llm := &askingLLM{phrased: phrased, decided: c.decided[0], then: c.decided[1:]}
		var kept []config.Call
		b := Builder{LLM: func(string) (agent.LLM, error) { return llm, nil }}.WithAllowing(func(call config.Call) error {
			kept = append(kept, call)
			return nil
		})
		runs := 0
		tool := b.permitted(config.Config{}, []agent.Tool{{Name: ToolBash, Run: func(context.Context, json.RawMessage) (string, error) {
			runs++
			return "done", nil
		}}})[0]
		var asked []Question
		var said []string
		ctx := agent.WithOwner(context.Background(), func(_ context.Context, raw json.RawMessage) (string, error) {
			var q Question
			json.Unmarshal(raw, &q)
			asked = append(asked, q)
			return c.answers[len(asked)-1], nil
		})
		ctx = agent.WithSaying(ctx, func(_, text string) { said = append(said, text) })
		result, err := tool.Run(ctx, rm)
		// A decision is said back at once: in the words the LLM gave it, or
		// as the question has it.
		back := map[string]string{"ONCE": "Alles klar.", "ALWAYS": "Alles klar.", "always: Gern, ab jetzt immer.": "Gern, ab jetzt immer.", "NOT: Okay, dann nicht.": "Okay, dann nicht."}[c.decided[len(c.decided)-1]]
		if strings.Join(said, "|") != back {
			t.Errorf("%q: said back %q, not %q", c.answers, said, back)
		}
		text := result
		if err != nil {
			text = err.Error()
		}
		// What the owner said in their own words follows the result; a
		// refusal tells the one answer, or all of it after a question.
		words := c.answers[0] != "Ja" && c.answers[0] != "immer" && (err == nil || len(asked) > 1)
		if words != strings.Contains(text, "owner: "+c.answers[len(asked)-1]+"\n]") {
			t.Errorf("%q: the exchange is not where it belongs in %q", c.answers, text)
		}
		if runs != c.runs || len(kept) != c.kept || len(asked) != c.asked || len(llm.sent) != 1+c.llm || (runs == 0) != (err != nil) {
			t.Errorf("%q: %v, %d runs, kept %v, asked %+v, LLM %d", c.answers, err, runs, kept, asked, len(llm.sent))
			continue
		}
		if asked[0].Question != "Darf ich den Build-Ordner löschen?" || strings.Join(asked[0].Options, "|") != "Ja|Immer|Nein" {
			t.Errorf("%q: asked %+v", c.answers, asked[0])
		}
		switch {
		case len(asked) < 2:
		case c.decided[0] == "DETAILS" && !strings.HasSuffix(asked[1].Question, "\n\nbash: rm -rf build"):
			t.Errorf("details: %q", asked[1].Question)
		case strings.HasPrefix(c.decided[0], "UNCLEAR"):
			if asked[1].Question != "Soll ich ihn nun löschen?" || strings.Join(asked[1].Options, "|") != "Ja|Immer|Nein" {
				t.Errorf("asked back: %+v", asked[1])
			}
		case c.decided[0] != "DETAILS" && asked[1].Question != "Den Ordner build im Projekt.\n\nDarf ich den Build-Ordner löschen?":
			t.Errorf("reply: %q", asked[1].Question)
		}
	}
}

// Where the LLM phrases nothing usable, the call is asked about as it is.
func TestPermittedPhrasedPlainly(t *testing.T) {
	for _, phrased := range []string{"no JSON", `{"question": "Darf ich?", "once": "Ja", "always": "Ja", "not": "Nein"}`, `{"question": ""}`} {
		got := asking{&askingLLM{phrased: phrased}, "m"}.phrase(context.Background(), ToolBash, "rm -rf build")
		if got != plainPermission(ToolBash, "rm -rf build") {
			t.Errorf("%q: %+v", phrased, got)
		}
	}
}

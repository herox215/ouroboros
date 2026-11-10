package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/openai"
)

// A question is read as the agent wrote it, each option on one line: what
// could not be shown, or chosen from, is refused with what is wrong.
func TestParseQuestion(t *testing.T) {
	q, err := ParseQuestion(json.RawMessage(`{"question":" Which one? ","options":[" a\n b ","c"],"recommended":"a  b"}`))
	if err != nil || q.Question != "Which one?" || strings.Join(q.Options, "|") != "a b|c" || q.Recommended != "a b" {
		t.Fatalf("got %+v, %v", q, err)
	}
	if q, err := ParseQuestion(json.RawMessage(`{"question":"?","options":["1","2","3","4","5","6"]}`)); err != nil || len(q.Options) != MaxOptions || q.Recommended != "" {
		t.Fatalf("the most options, none recommended: %+v, %v", q, err)
	}
	// Options written as a string of JSON, as models write them when they
	// call user_question as a tool of its own, are taken.
	if q, err := ParseQuestion(json.RawMessage(`{"question":"Which?","options":"[\"a\", \"b\"]","recommended":"a"}`)); err != nil || strings.Join(q.Options, "|") != "a|b" || q.Recommended != "a" {
		t.Fatalf("options as a string: %+v, %v", q, err)
	}
	// A question without options is one the owner answers in own words.
	if q, err := ParseQuestion(json.RawMessage(`{"question":"What is it called?"}`)); err != nil || q.Question != "What is it called?" || len(q.Options) != 0 {
		t.Fatalf("no options: %+v, %v", q, err)
	}
	for args, want := range map[string]string{
		`{"question":"?","recommended":"a"}`: `user_question: what is recommended, "a", is none of the options: write it exactly as there, or leave it out`,
		`[]`:                                 `user_question: invalid arguments: "question" is a string, "options" an array of strings`,
		`{"options":["a","b"]}`:              `user_question needs "question"`,
		`{"question":" ","options":[]}`:      `user_question needs "question"`,
		`{"question":"?","options":["a"]}`:   `user_question takes 2 to 6 options, or none, not 1`,
		`{"question":"?","options":["1","2","3","4","5","6","7"]}`: `user_question takes 2 to 6 options, or none, not 7`,
		`{"question":"?","options":["a"," "]}`:                     `user_question: option 2 is empty`,
		`{"question":"?","options":["a","b","a "]}`:                `user_question: the option "a" is there twice`,
		`{"question":"?","options":["a","b"],"recommended":"c"}`:   `user_question: what is recommended, "c", is none of the options: write it exactly as there, or leave it out`,
	} {
		if _, err := ParseQuestion(json.RawMessage(args)); err == nil || err.Error() != want {
			t.Errorf("%s:\n got %v\nwant %s", args, err, want)
		}
	}
}

// The tool hands the question to whoever holds the conversation and returns
// what the owner answered. It asks nobody for a peer, and says so where
// nobody can be asked.
func TestUserQuestion(t *testing.T) {
	tool := userQuestion()
	if !tool.OwnerOnly || tool.Name != ToolUserQuestion {
		t.Fatalf("the tool: %+v", tool)
	}
	var schema struct{ Required []string }
	if err := json.Unmarshal([]byte(tool.Schema), &schema); err != nil || len(schema.Required) != 1 || schema.Required[0] != "question" {
		t.Fatalf("direct tool schema: %s", tool.Schema)
	}
	args := json.RawMessage(`{"question":"Which?","options":["a","b"],"recommended":"b"}`)
	var asked string
	owner := agent.WithOwner(context.Background(), func(_ context.Context, question json.RawMessage) (string, error) {
		asked = string(question)
		return "b", nil
	})
	if got, err := tool.Run(owner, args); got != "b" || err != nil {
		t.Fatalf("got %q, %v", got, err)
	}
	if asked != `{"question":"Which?","options":["a","b"],"recommended":"b"}` {
		t.Fatalf("asked %s", asked)
	}
	// A question that cannot be read is asked nobody.
	asked = ""
	if _, err := tool.Run(owner, json.RawMessage(`{"question":"Which?","options":["a"]}`)); err == nil || asked != "" {
		t.Fatalf("one option: %v, asked %q", err, asked)
	}
	// Why the owner did not answer comes back as it is.
	gone := agent.WithOwner(context.Background(), func(context.Context, json.RawMessage) (string, error) {
		return "", context.DeadlineExceeded
	})
	if _, err := tool.Run(gone, args); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("no answer: %v", err)
	}
	if _, err := tool.Run(context.Background(), args); err == nil || err.Error() != "nobody can be asked here: put the question in your answer instead" {
		t.Fatalf("nobody to ask: %v", err)
	}
	peer := a2a.WithOrigin(owner, a2a.Origin{PublicKey: "k"})
	if _, err := tool.Run(peer, args); err == nil || err.Error() != "user_question is for the owner of this node only" || asked != "" {
		t.Fatalf("for a peer: %v, asked %q", err, asked)
	}
}

// The owner hears at once that their answer arrived, in the words the LLM
// finds for it; without an LLM, or where nobody answered, nothing is said.
func TestUserQuestionAcknowledged(t *testing.T) {
	llm := &askingLLM{decided: "\"Alles klar, dann SQLite.\""}
	for _, c := range []struct {
		ask    asking
		answer string
		err    error
		said   string
	}{
		{asking{llm, "m"}, "SQLite", nil, "Alles klar, dann SQLite."},
		{asking{}, "SQLite", nil, ""},
		{asking{llm, "m"}, "", errors.New("gone"), ""},
	} {
		var said []string
		ctx := agent.WithOwner(context.Background(), func(context.Context, json.RawMessage) (string, error) { return c.answer, c.err })
		ctx = agent.WithSaying(ctx, func(_, text string) { said = append(said, text) })
		got, err := userQuestion(c.ask.acknowledge).Run(ctx, json.RawMessage(`{"question":"Welche Datenbank?","options":["SQLite","Postgres"]}`))
		if got != c.answer || (err != nil) != (c.err != nil) || strings.Join(said, "|") != c.said {
			t.Errorf("%+v: %q, %v, said %q", c, got, err, said)
		}
	}
	if sent := llm.sent[0]; !strings.Contains(sent, "Welche Datenbank?\n(SQLite / Postgres)") || !strings.Contains(sent, "<answer>\nSQLite\n</answer>") || llm.prompts[0] != ackPrompt {
		t.Errorf("sent %q", sent)
	}
}

// A reply that is a question back gets no acknowledgement: it would promise
// what the agent cannot do, and an option still gets one.
func TestUserQuestionNoAckForQuestionBack(t *testing.T) {
	llm := &askingLLM{decided: "Okay, then I'll explain it."}
	for _, c := range []struct{ answer, said string }{
		{"??", ""},
		{"What is the problem with it?", ""},
		{"SQLite", "Okay, then I'll explain it."},
	} {
		var said []string
		ctx := agent.WithOwner(context.Background(), func(context.Context, json.RawMessage) (string, error) { return c.answer, nil })
		ctx = agent.WithSaying(ctx, func(_, text string) { said = append(said, text) })
		if _, err := userQuestion(asking{llm, "m"}.acknowledge).Run(ctx, json.RawMessage(`{"question":"Which?","options":["SQLite","Postgres"]}`)); err != nil || strings.Join(said, "|") != c.said {
			t.Errorf("%q: %v, said %q", c.answer, err, said)
		}
	}
}

// scriptedCalls is an LLM that calls user_question with each of its
// questions in turn, and ends with the results it saw.
type scriptedCalls struct {
	questions []string
	results   []string
}

func (l *scriptedCalls) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	if last := msgs[len(msgs)-1]; last.Role == "tool" {
		l.results = append(l.results, last.Content)
	}
	if len(l.questions) == 0 {
		return openai.Message{Role: "assistant", Content: "done"}, nil
	}
	q := l.questions[0]
	l.questions = l.questions[1:]
	return openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: q, Function: openai.FunctionCall{
		Name: ToolUserQuestion, Arguments: `{"question":"` + q + `"}`}}}}, nil
}

// The same question asked again after the owner answered it with a question
// is not asked: the agent is told to explain instead. A different question,
// or one after a real answer, is asked.
func TestUserQuestionRepeatGuard(t *testing.T) {
	answers := []string{"What is the problem?", "Postgres", "SQLite"}
	var asked []string
	ctx := agent.WithOwner(context.Background(), func(_ context.Context, q json.RawMessage) (string, error) {
		asked = append(asked, string(q))
		a := answers[0]
		answers = answers[1:]
		return a, nil
	})
	llm := &scriptedCalls{questions: []string{
		"How shall I solve the three points?",
		"Do you like how I solve the three points?",
		"Which database do you prefer?",
		"Which database do you prefer then?",
	}}
	if _, err := agent.New(llm, "m", "sys", userQuestion()).Respond(ctx, "c", "go"); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 3 || len(llm.results) != 4 || !strings.Contains(llm.results[1], repeatHint) || strings.Contains(llm.results[3], repeatHint) {
		t.Fatalf("asked %v, results %q", asked, llm.results)
	}
}

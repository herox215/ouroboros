package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
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
	// Its schema is one the functions of a script are listed from.
	if got := signatures([]agent.Tool{tool}); !strings.HasPrefix(got, "- user_question({options?: array, question: string, recommended?: string}): Ask your user") {
		t.Fatalf("as a function: %s", got)
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

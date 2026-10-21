package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"ouroboros/internal/agent"
)

// ToolUserQuestion is the tool with which the agent of the owner asks them
// a question, with answers to choose from if it has some, and waits for
// theirs.
const ToolUserQuestion = "user_question"

// How many answers a Question offers, if it offers any.
const (
	MinOptions = 2
	MaxOptions = 6
)

// Question is what the agent asks the owner with ToolUserQuestion: the
// question, the answers to choose from, and the one of them it would choose,
// "" if it has no preference. The owner may always answer in their own words
// instead: a front end offers that next to the Options, and nothing else for
// a question without any.
type Question struct {
	Question    string   `json:"question"`
	Options     []string `json:"options"`
	Recommended string   `json:"recommended,omitempty"`
}

// ParseQuestion reads a Question from the arguments of a call of
// ToolUserQuestion. Its options are each one line, and no two of them are
// the same; what is recommended is one of them.
func ParseQuestion(args json.RawMessage) (Question, error) {
	var q Question
	// options written as a string of JSON are taken, as Decode takes them
	if err := agent.Decode(ToolUserQuestion, args, &q); err != nil {
		return Question{}, errors.New(ToolUserQuestion + `: invalid arguments: "question" is a string, "options" an array of strings`)
	}
	q.Question = strings.TrimSpace(q.Question)
	if q.Question == "" {
		return Question{}, errors.New(ToolUserQuestion + ` needs "question"`)
	}
	if n := len(q.Options); n != 0 && (n < MinOptions || n > MaxOptions) {
		return Question{}, fmt.Errorf("%s takes %d to %d options, or none, not %d", ToolUserQuestion, MinOptions, MaxOptions, n)
	}
	for i, o := range q.Options {
		o = flat(o)
		if o == "" {
			return Question{}, fmt.Errorf("%s: option %d is empty", ToolUserQuestion, i+1)
		}
		if slices.Contains(q.Options[:i], o) {
			return Question{}, fmt.Errorf("%s: the option %q is there twice", ToolUserQuestion, o)
		}
		q.Options[i] = o
	}
	if q.Recommended = flat(q.Recommended); q.Recommended != "" && !slices.Contains(q.Options, q.Recommended) {
		return Question{}, fmt.Errorf("%s: what is recommended, %q, is none of the options: write it exactly as there, or leave it out",
			ToolUserQuestion, q.Recommended)
	}
	return q, nil
}

// flat is text on one line, without what is blank around it.
func flat(text string) string { return strings.Join(strings.Fields(text), " ") }

// userQuestion is the tool with which the agent asks its owner. Whoever
// holds the conversation shows the question and hands back the answer, see
// agent.WithOwner: the sessions of the node do. heard, if not nil, hears
// the answer before the tool returns it: it says back that it arrived.
func userQuestion(heard ...func(context.Context, Question, string)) agent.Tool {
	return agent.Tool{
		Name: ToolUserQuestion,
		Description: "Ask your user a question and wait for the answer, which it returns: " +
			"a decision that is theirs, what they prefer, or what their request leaves open. " +
			"With options they choose one with a key, the one you recommend selected, and you get it as you wrote it; " +
			"they can always answer in their own words instead, so add no option for that. Without options they write the answer. " +
			`Like ` + ToolUserQuestion + `({question: "Which database?", options: ["SQLite", "Postgres"], recommended: "SQLite"}). ` +
			"When the sending interface is known, the result is JSON with the unchanged answer in answer and its origin in source.interface. " +
			"The reply may include pictures, arrive without text, or change the topic; treat it as the owner's next message rather than forcing an offered option. " +
			"Ask nothing you can find out yourself. " +
			"It waits until the owner answers or the conversation is cancelled.",
		Schema: `{"type":"object","properties":{` +
			`"question":{"type":"string","description":"what you ask: one short, plain sentence in the language of the conversation, without code; it has to make sense without the options"},` +
			fmt.Sprintf(`"options":{"type":"array","items":{"type":"string"},"description":"%d to %d answers to choose from, each a few words on one line; leave out for a question only their own words answer"},`, MinOptions, MaxOptions) +
			`"recommended":{"type":"string","description":"the option you would choose, exactly as in options; leave out without a preference"}` +
			`},"required":["question"]}`,
		OwnerOnly: true,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			if err := ownerOnly(ctx, ToolUserQuestion); err != nil {
				return "", err
			}
			q, err := ParseQuestion(raw)
			if err != nil {
				return "", err
			}
			asked, _ := json.Marshal(q) // strings only: cannot fail
			answer, err := agent.AskOwnerReply(ctx, asked)
			if errors.Is(err, agent.ErrNoOwner) {
				return "", errors.New("nobody can be asked here: put the question in your answer instead")
			}
			if err == nil {
				for _, h := range heard {
					h(ctx, q, answer.Text)
				}
			}
			if err != nil || answer.Source.Interface == "" {
				return answer.Text, err
			}
			// The model can use the reply's origin, but it is not part of
			// the answer value (not even for a selected option).
			result, err := json.Marshal(answer)
			return string(result), err
		},
	}
}

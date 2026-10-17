package assistant

import (
	"context"
	"strconv"
	"strings"
	"time"

	"ouroboros/internal/openai"
)

// A review looks back on an answer that had signals, see Signal: the LLM of
// the agent reads what the log says of the answer and proposes one change of
// ouro that would have done it better, or none. It runs once the answer is
// given, and the owner gets what it proposes as a suggestion: it changes
// nothing by itself.

// reviewKey is the key of what a review proposes goes to, see WithReview.
type reviewKey struct{}

// WithReview makes an answer that runs with ctx and has signals be reviewed
// once it is given: what the review proposes goes to suggest, which is not
// called when it proposes nothing. Without it no answer is reviewed.
func WithReview(ctx context.Context, suggest func(string)) context.Context {
	return context.WithValue(ctx, reviewKey{}, suggest)
}

// Reviewer is where what the review of an answer that runs with ctx
// proposes goes, see WithReview: nil if it is not reviewed.
func Reviewer(ctx context.Context) func(string) {
	suggest, _ := ctx.Value(reviewKey{}).(func(string))
	return suggest
}

// suggestedIntro starts what the agent is asked when the owner took a
// suggestion, see SuggestedTask.
const suggestedIntro = "Make this change of ouro, which the review of an earlier answer suggested:"

// SuggestedTask is what the agent is asked to make the suggestion text the
// owner took, which the review of an answer with signals made: it checks it
// against the source first, as the review knew only the log. Its answer is
// not reviewed again: what a review says of making what a review said is no
// more than the first one knew.
func SuggestedTask(text string, signals []string) string {
	task := suggestedIntro + "\n\n" + text
	if len(signals) > 0 {
		task += "\n\nWhat was measured of that answer: " + strings.Join(signals, "; ") + "."
	}
	return task + "\n\nThe review saw only the log of that answer, not the source of ouro; you need not look for that log. " +
		"Check the change against the source first, and make it, with its tests, when it holds up. " +
		"If it does not, say why and change nothing."
}

// The bounds of a review.
const (
	reviewTime   = 2 * time.Minute // for the call of the LLM
	reviewTrail  = 12000           // bytes of the lines of the log it reads
	maxSuggested = 600             // bytes of what it proposes
)

// reviewEffort is how much the model thinks for a review: what it proposes
// is worth some thought, but no answer waits for it.
const reviewEffort = "medium"

// unchanged is what the LLM answers when nothing of ouro is to change, and
// change starts what it proposes: what starts otherwise is nothing.
const (
	unchanged = "NONE"
	change    = "CHANGE:"
)

// reviewPrompt asks the LLM to review an answer.
const reviewPrompt = `You review how ouro, an agent program, gave one answer to its owner, to make ouro better. ` +
	`ouro is a program with a system prompt, tools and defaults, and it can be changed. ` +
	`You get the tools of the agent; what the log says of the answer: the message of the owner, ` +
	`every call of the LLM and of a tool with how long it took and how it ended; ` +
	`and the signals that made this answer worth a look. ` +
	`A follow-up is a short message the owner wrote right after the answer: it may tell what the answer lacked, ` +
	`as when it only said what it would do, or nothing at all, as a thank you. ` +
	`Find the one change of ouro that would have made the answer better, also for the answers to come: ` +
	`a tool that is missing, slow or misleading, a default, or something in the prompt that led the agent wrong. ` +
	`A change of how the owner asked is none, and neither is a change of the model or of the provider. ` +
	`The tools under withheld are part of ouro, but the owner does not allow them to the agent: ` +
	`that the agent lacked one of them is no change of ouro either. ` +
	`When the signals come from the task itself, as a build that takes long or a wait the owner asked for, ` +
	`or from the provider, or when you find no such change, answer ` + unchanged + ` and nothing else. ` +
	`Otherwise answer ` + change + ` followed by one or two short sentences, in the language of the owner's message: ` +
	`what you would change, and the measure that shows why.`

// review asks the LLM of the agent what of ouro to change after an answer
// that had signals, with the lines of the log of it
// and the tools of the agent: "" if nothing. The call is part of no
// conversation; the access of the agent is asked first, then its fallbacks.
func (a *Assistant) review(ctx context.Context, signals []Signal, trail []string, tools toolsOf) (string, error) {
	_, cfg, err := a.current()
	if err != nil {
		return "", err
	}
	llm, err := a.builder.LLM(cfg.Access)
	if err != nil {
		return "", err
	}
	texts := make([]string, len(signals))
	for i, s := range signals {
		texts[i] = "- " + string(s.Kind) + ": " + s.Text
	}
	ctx = openai.WithEffort(ctx, reviewEffort)
	reply, err := a.builder.withFallbacks(llm, cfg).Complete(ctx, cfg.Model, []openai.Message{
		{Role: "system", Content: reviewPrompt},
		{Role: "user", Content: "<tools>\n" + strings.Join(tools.allowed, "\n") + "\n</tools>\n\n" +
			"<withheld>\n" + strings.Join(tools.withheld, ", ") + "\n</withheld>\n\n" +
			"<log>\n" + strings.Join(bounded(trail, reviewTrail), "\n") + "\n</log>\n\n" +
			"<signals>\n" + strings.Join(texts, "\n") + "\n</signals>"},
	}, nil)
	if err != nil {
		return "", err
	}
	return proposed(reply.Content), nil
}

// bounded is lines within max bytes: the first, which is the message of the
// owner, and as many of the last as fit, with a line that says how many are
// left out between them.
func bounded(lines []string, max int) []string {
	if len(lines) == 0 {
		return nil
	}
	size := len(lines[0])
	from := len(lines)
	for from > 1 && size+len(lines[from-1]) <= max {
		from--
		size += len(lines[from])
	}
	if from == 1 {
		return lines
	}
	left := strconv.Itoa(from-1) + " lines"
	if from == 2 {
		left = "1 line"
	}
	return append([]string{lines[0], "… " + left + " left out …"}, lines[from:]...)
}

// firstSentence is the first sentence of text, on one line: what the review
// is told of a tool.
func firstSentence(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if i := strings.Index(text, ". "); i >= 0 {
		return text[:i+1]
	}
	return text
}

// proposed is what the LLM proposes in reply, after change: "" for
// nothing, and at most maxSuggested bytes, where a character begins.
func proposed(reply string) string {
	text := strings.Join(strings.Fields(strings.Trim(strings.TrimSpace(reply), "*`")), " ")
	rest, ok := strings.CutPrefix(text, change)
	if !ok {
		return ""
	}
	text = strings.TrimSpace(strings.TrimLeft(rest, "*` "))
	if len(text) > maxSuggested {
		text = strings.ToValidUTF8(text[:maxSuggested], "") + "…"
	}
	return text
}

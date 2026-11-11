package assistant

import (
	"context"
	"slices"
	"strings"
	"sync"
	"unicode"

	"ouroboros/internal/agent"
)

// repeatHint is what ToolUserQuestion returns instead of asking again a
// question the owner just answered with a question or confusion of their own.
const repeatHint = "the owner did not understand the last question and answered it with a question of their own: " +
	"do not ask it again in other words. End your answer now and explain in it what the question is about, " +
	"with everything they need to decide, then ask it there"

// Words shared by two questions that make them the same question: at least
// repeatShared of them, and repeatOverlap of the shorter question's words.
const (
	repeatShared  = 2
	repeatOverlap = 0.5
)

// lastAsked is what an answer remembers of its last user_question.
type lastAsked struct {
	mu       sync.Mutex
	question string
	// unseen is true where the owner replied to it with a question or
	// confusion instead of one of its options, so that asking it again
	// would not help them.
	unseen bool
}

type lastAskedKey struct{}

// lastAskedOf is the memory of the answer of ctx, nil outside of an answer.
func lastAskedOf(ctx context.Context) *lastAsked {
	a := agent.AnswerOf(ctx)
	if a == nil {
		return nil
	}
	return a.Value(lastAskedKey{}, func() any { return new(lastAsked) }).(*lastAsked)
}

// repeats reports whether q asks again what the owner just did not
// understand; asking it once is allowed after the hint.
func (l *lastAsked) repeats(q Question) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	same := l.unseen && sameQuestion(l.question, q.Question)
	l.unseen = false
	return same
}

// remember keeps q and whether its answer was a reply to it.
func (l *lastAsked) remember(q Question, answer string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.question, l.unseen = q.Question, backQuestion(q, answer)
}

// backQuestion reports whether answer is no option of q but a question back.
func backQuestion(q Question, answer string) bool {
	answer = flat(answer)
	return strings.Contains(answer, "?") && !slices.ContainsFunc(q.Options, func(o string) bool { return strings.EqualFold(o, answer) })
}

// sameQuestion reports whether two questions share most of their words.
func sameQuestion(a, b string) bool {
	wa, wb := wordsOf(a), wordsOf(b)
	shared := 0
	for w := range wa {
		if wb[w] {
			shared++
		}
	}
	return shared >= repeatShared && float64(shared) >= repeatOverlap*float64(min(len(wa), len(wb)))
}

// wordsOf is the words of text of more than two letters, in lower case.
func wordsOf(text string) map[string]bool {
	words := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) > 2 {
			words[w] = true
		}
	}
	return words
}

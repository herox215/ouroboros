package assistant

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// A follow-up is a short message the owner writes soon after an answer, like
// "did anything happen?" or "?": it tells what the answer lacked better than
// any measure, as an answer that only said what it would do looks well in
// the log. The answer before it is reviewed with it, see SignalFollowUp.
const (
	followUpTime  = 15 * time.Minute // after the answer
	followUpRunes = 60               // of the message
)

// answered is what is kept of the last answer in a conversation, for a
// follow-up to review it with.
type answered struct {
	at      time.Time
	signals []Signal
	trail   []string // the lines of the log of it, see record
	task    bool     // it was to a suggestion the owner took, see SuggestedTask
}

// keep keeps what an answer in the conversation chat left, for a follow-up.
func (a *Assistant) keep(chat string, last answered) {
	a.reviewing.Lock()
	defer a.reviewing.Unlock()
	if a.last == nil {
		a.last = map[string]answered{}
	}
	a.last[chat] = last
}

// followed reports whether text, the next message in the conversation chat
// at now, follows up on the answer before it, and returns that answer with
// the signal of the follow-up. Either way the answer before is let go: only
// the next message follows up on it.
func (a *Assistant) followed(chat, text string, now time.Time) (answered, Signal, bool) {
	a.reviewing.Lock()
	last, ok := a.last[chat]
	delete(a.last, chat)
	a.reviewing.Unlock()
	text = strings.Join(strings.Fields(text), " ")
	after := now.Sub(last.at)
	if !ok || last.task || text == "" || utf8.RuneCountInString(text) > followUpRunes || after > followUpTime {
		return answered{}, Signal{}, false
	}
	return last, Signal{SignalFollowUp, fmt.Sprintf("the owner wrote next, %s later: %q", roughly(after), text)}, true
}

// forget lets go of the last answer in the conversation chat.
func (a *Assistant) forget(chat string) {
	a.reviewing.Lock()
	defer a.reviewing.Unlock()
	delete(a.last, chat)
}

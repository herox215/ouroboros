package matrix

import (
	"context"
	"strings"
	"time"

	"ouroboros/plugin"
)

// typing tracks successful ephemeral notifications for one connection only.
// Renew before the server's 30-second expiry; never persist typing state.
type typing map[*conversation]time.Time

// answering reports whether an answer is on its way in the session id, as
// its events tell, and does not wait for the owner: in the thread, while it
// writes the answer to what the owner said, or while work it handed on for a
// message it has not answered yet goes on, which only it tells, see
// plugin.SessionWorking. Work that goes on after its answer is background:
// the room sees it when it is done, and is not told of it by typing.
func answering(id string, events []plugin.SessionEvent) bool {
	if id == plugin.ThreadID {
		working, asking, owed, own := false, false, false, false
		for _, e := range events {
			switch e.Kind {
			case plugin.SessionWorking:
				working, own = e.N > 0, e.Own
			case plugin.SessionAsked:
				asking = !e.Done
			case plugin.SessionMessage:
				owed = true
			case plugin.SessionAnswer, plugin.SessionFailed, plugin.SessionCanceled:
				owed = false
			}
		}
		return working && !asking && (own || owed)
	}
	active := false
	for _, e := range events {
		switch e.Kind {
		case plugin.SessionMessage:
			active = true
		case plugin.SessionAsked:
			active = e.Done
		case plugin.SessionAnswer, plugin.SessionFailed, plugin.SessionCanceled, plugin.SessionRestart, plugin.SessionRestarted:
			active = false
		}
	}
	return active
}

func (t typing) update(ctx context.Context, conn connection, entry *conversation, active bool, now time.Time) {
	last, on := t[entry]
	if (!active && !on) || (active && on && now.Sub(last) < 15*time.Second) {
		return
	}
	peer, _, ok := strings.Cut(entry.Identity.Subject, "\n")
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	// Typing is best-effort: a failure must not suppress the actual answer.
	if conn.TypingTo(ctx, entry.Room, peer, active) != nil {
		return
	}
	if active {
		t[entry] = now
	} else {
		delete(t, entry)
	}
}

func (t typing) stop(conn connection) {
	// The run context is already canceled on disconnect. Use a bounded fresh
	// context to clear notifications, with server expiry as the fallback.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for entry := range t {
		t.update(ctx, conn, entry, false, time.Now())
	}
}

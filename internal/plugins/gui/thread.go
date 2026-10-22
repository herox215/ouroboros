package gui

import (
	"context"
	"errors"

	"ouroboros/plugin"
)

// Like the terminal UI, showing the thread first gives it the directory the
// UI started in. A busy thread keeps its project and is still shown.
func (w *Window) placeThread(ctx context.Context, p Profile, profile int) string {
	w.threadMu.Lock()
	defer w.threadMu.Unlock()
	if w.directory == "" || w.threads[profile] || ctx.Err() != nil {
		return ""
	}
	if err := p.sessions.SetDirectory(ctx, plugin.ThreadID, w.directory); err != nil {
		return "the thread keeps its project: " + err.Error()
	}
	w.threads[profile] = true
	return ""
}

// StopSession uses the same cancellation as the terminal UI. For the thread
// the node also stops the answers it handed to other sessions.
func (w *Window) StopSession(profile int, id string) error {
	serviceID, owner, err := sessionID(id)
	if err != nil {
		return err
	}
	if !owner {
		return errors.New("peer sessions are read-only")
	}
	p, err := w.ready(profile)
	if err != nil {
		return err
	}
	return p.sessions.Cancel(w.ctx, serviceID)
}

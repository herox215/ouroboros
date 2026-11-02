package gui

import (
	"errors"
)

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

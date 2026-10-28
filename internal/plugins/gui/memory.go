package gui

import "ouroboros/plugin"

// MemoryInterval is how often the archivist may revisit the memories of the
// selected profile, "off" when it is off.
func (w *Window) MemoryInterval(profile int) (string, error) {
	p, err := w.modelProfile(profile)
	if err != nil {
		return "", err
	}
	return p.owner.MemoryInterval()
}

// SetMemoryInterval sets how often the archivist may revisit the memories of
// the selected profile: a duration like "24h", or "off". It returns from
// when it holds.
func (w *Window) SetMemoryInterval(profile int, value string) (string, error) {
	p, err := w.modelProfile(profile)
	if err != nil {
		return "", err
	}
	return p.owner.SetMemoryInterval(value)
}

// MemoryChanges lists the latest changes the archivist made to the memories
// of the selected profile, the newest first.
func (w *Window) MemoryChanges(profile int, limit int) ([]plugin.MemoryChange, error) {
	p, err := w.modelProfile(profile)
	if err != nil {
		return nil, err
	}
	return p.owner.MemoryChanges(w.ctx, limit)
}

// UndoMemoryChange takes back one change the archivist made to the memories
// of the selected profile.
func (w *Window) UndoMemoryChange(profile int, id int64) error {
	p, err := w.modelProfile(profile)
	if err != nil {
		return err
	}
	return p.owner.UndoMemoryChange(w.ctx, id)
}

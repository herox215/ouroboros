package tui

import (
	"time"

	"ouroboros/plugin"
)

// threadStart leaves archived messages intact, hiding only the expired prefix.
// Entries without a timestamp remain visible until their time is known.
func (m *Model) threadStart(c *conversation) int {
	if c.contextID != plugin.ThreadID {
		return 0
	}
	cutoff := m.now().Add(-6 * time.Hour)
	for i, e := range c.entries {
		if e.at.IsZero() || !e.at.Before(cutoff) {
			return i
		}
	}
	return len(c.entries)
}

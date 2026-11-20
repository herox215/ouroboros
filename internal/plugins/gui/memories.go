package gui

import (
	"context"
	"errors"
)

// The window lists the memories of its profile, the ones the agent of the
// owner collected, each with its topic: a plain overview to read, and nothing
// to change in it. The terminal UI edits them, see /memory. It reads the
// store of the profile as the memory dialog does, which counts no use of a
// memory, and leaves out the memories that have expired.

// Memory is a memory, as the window lists it: what is shown of it, not its
// version or where it came from.
type Memory struct {
	ID      string `json:"id"`
	Topic   string `json:"topic"`
	Key     string `json:"key"`
	Content string `json:"content"`
	// Updated is when it was changed last, in RFC 3339.
	Updated string `json:"updated"`
	// Expires is when a memory of a state ends, in RFC 3339, absent for
	// never.
	Expires string `json:"expires,omitempty"`
}

// MemoryList is the memories the window lists, the latest updated first, and
// how many there are beyond them.
type MemoryList struct {
	Memories []Memory `json:"memories"`
	// Total is how many memories there are, those expired included.
	Total int `json:"total"`
	// Hidden is how many expired memories are left out.
	Hidden int `json:"hidden"`
}

// errNoMemories is what a window answers that cannot list the memories: one
// opened without the owner of the node, as tests do.
var errNoMemories = errors.New("this window lists no memories")

// Memories lists the memories of the profile that have not expired, the
// latest updated first, as they are now.
func (w *Window) Memories() (MemoryList, error) { return w.profile.memoryList(w.ctx) }

// memoryList lists the memories, as Window.Memories does.
func (p *profile) memoryList(ctx context.Context) (MemoryList, error) {
	if p.memories == nil {
		return MemoryList{}, errNoMemories
	}
	listed, err := p.memories(ctx)
	if err != nil {
		return MemoryList{}, err
	}
	out := MemoryList{Memories: make([]Memory, 0, len(listed.Memories)), Total: listed.Total, Hidden: listed.Hidden}
	for _, e := range listed.Memories {
		out.Memories = append(out.Memories, Memory{ID: e.ID, Topic: e.Topic, Key: e.Key, Content: e.Content, Updated: e.Updated, Expires: e.Expires})
	}
	return out, nil
}

package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"ouroboros/plugin"
)

// SetInitialDirectory sets the start path for new conversations, including the
// initial one. Call it before showing the UI and before Resume. Existing
// sessions keep their own directories. Every profile uses this start path.
func (m *Model) SetInitialDirectory(path string) {
	if m.profiles != nil {
		for _, model := range m.profiles.models {
			model.SetInitialDirectory(path)
		}
		return
	}
	m.initialDirectory = path
	if m.chat.fresh && !m.chat.watching {
		m.chat.directory = path
	}
}

// newChat allocates an ID belonging to this UI, not an existing session.
func (m *Model) newChat() *conversation {
	c := m.conversation(plugin.NewID())
	c.directory, c.fresh = m.initialDirectory, true
	return c
}

// directoryBackend is optional so backends without project directories keep
// working. The backend resolves paths and rejects changes while busy; the
// directory event, not the command, supplies the canonical absolute path.
type directoryBackend interface {
	SetSessionDirectory(ctx context.Context, id, path string) error
}

func (m *Model) showDirectory() tea.Cmd {
	path := m.chat.directory
	if path == "" {
		path = "workspace"
	}
	m.input.Reset()
	m.setNotice("directory: "+path, false)
	return nil
}

func (m *Model) changeDirectory(path string) tea.Cmd {
	if path == "" {
		return m.showDirectory()
	}
	if m.chat.peer != "" {
		m.setNotice("the conversation of a peer is only to read", true)
		return nil
	}
	backend, ok := m.backend.(directoryBackend)
	if !ok {
		m.setNotice("this backend does not support project directories", true)
		return nil
	}
	if path == "--workspace" {
		path = ""
	} else if strings.HasPrefix(path, "\"") || strings.HasPrefix(path, "'") {
		if len(path) < 2 || path[len(path)-1] != path[0] {
			m.setNotice("/cd: close the quote around the path", true)
			return nil
		}
		path = path[1 : len(path)-1]
		if path == "" {
			m.setNotice("/cd: use --workspace to reset the directory", true)
			return nil
		}
	}
	m.input.Reset()
	id := m.chat.contextID
	return func() tea.Msg {
		return actionMsg{"directory changed", backend.SetSessionDirectory(m.ctx, id, path)}
	}
}

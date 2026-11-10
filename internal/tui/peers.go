package tui

import (
	"cmp"
	"fmt"

	tea "charm.land/bubbletea/v2"
)

// PeerInfo is a peer as /peers lists it: a node that is online or a trusted
// one that is not, with the state of its connection to this node as
// list_peers prints it.
type PeerInfo struct {
	Name, ID, Model string
	State           string // "connected", "not connected", ...
	About           string // what a connected peer says of itself: its claim
}

// peersScreen lists the peers of the node, for /peers. Enter on one opens
// the conversations with it in /session, where they are read, continued
// from the peer's side and deleted.
type peersScreen struct {
	list   []PeerInfo
	err    error
	loaded bool
	sel    int
}

// peersListMsg is the list of the peers, as the network found them.
type peersListMsg struct {
	list []PeerInfo
	err  error
}

// openPeers opens the list of the peers of the node.
func (m *Model) openPeers() tea.Cmd {
	m.peerList.loaded, m.peerList.err, m.peerList.sel = false, nil, 0
	m.screen = screenPeers
	return func() tea.Msg {
		list, err := m.backend.Nodes(m.ctx)
		return peersListMsg{list, err}
	}
}

// peersListed shows the list.
func (m *Model) peersListed(msg peersListMsg) {
	s := &m.peerList
	s.list, s.err, s.loaded = msg.list, msg.err, true
	s.sel = min(s.sel, max(0, len(s.list)-1))
}

func (s *peersScreen) handleKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "up", "k":
		s.sel = max(0, s.sel-1)
	case "down", "j":
		s.sel = min(max(0, len(s.list)-1), s.sel+1)
	case "esc":
		m.screen = screenChat
	case "enter":
		if s.sel >= len(s.list) {
			return nil
		}
		cmd := m.openSessions()
		m.sessions.peerTab, m.sessions.filter = true, cmp.Or(s.list[s.sel].Name, s.list[s.sel].ID)
		m.sessions.sel = 0
		return tea.Batch(cmd, m.sessions.find(m))
	}
	return nil
}

// view shows each peer with its state.
func (s *peersScreen) view(m *Model) (string, []hit) {
	lines := []string{
		boldStyle.Render("Peers"), "",
		mutedStyle.Render("The nodes your agent can ask, as list_peers shows them to it."),
		mutedStyle.Render("What a peer says of itself is its claim."), "",
	}
	switch {
	case s.err != nil:
		lines = append(lines, errStyle.Render("The peers cannot be listed: "+s.err.Error()))
	case !s.loaded:
		lines = append(lines, mutedStyle.Render("…"))
	case len(s.list) == 0:
		lines = append(lines, mutedStyle.Render("No peers are online."))
	}
	for i, p := range s.list {
		mark, name := "  ", cmp.Or(p.Name, p.ID)
		if i == s.sel {
			mark, name = "› ", boldStyle.Foreground(accent).Render(name)
		}
		about := fmt.Sprintf("id %s · %s", p.ID, p.State)
		if p.Model != "" {
			about = fmt.Sprintf("id %s · model %s · %s", p.ID, p.Model, p.State)
		}
		lines = append(lines, mark+name, "    "+mutedStyle.Render(about))
		if p.About != "" {
			lines = append(lines, "    "+mutedStyle.Render(short(p.About, 60)))
		}
	}
	return m.dialog(lines), nil
}

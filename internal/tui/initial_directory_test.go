package tui

import (
	"context"
	"errors"
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"
	"ouroboros/plugin"
)

type initialDirectoryBackend struct {
	Backend
	calls            []string
	path             string
	setErr, watchErr error
}

func (b *initialDirectoryBackend) SetSessionDirectory(_ context.Context, id, path string) error {
	b.calls = append(b.calls, "set:"+id)
	if b.setErr == nil {
		b.path = path
	}
	return b.setErr
}

func (b *initialDirectoryBackend) Watch(_ context.Context, id string) ([]Event, <-chan Event, error) {
	b.calls = append(b.calls, "watch:"+id)
	return []Event{{Kind: "directory", Text: b.path}}, make(chan Event), b.watchErr
}

func TestInitialDirectoryAndNewConversation(t *testing.T) {
	m, original := newModel(t)
	b := &initialDirectoryBackend{Backend: original}
	m.backend = b
	m.SetInitialDirectory("/tmp/project")
	if m.chat.directory != "/tmp/project" || len(b.calls) != 0 {
		t.Fatal("initial path not available before Watch")
	}
	// Init schedules Watch among the other startup commands; only execute it.
	batch := m.Init()().(tea.BatchMsg)
	msg := batch[3]().(watchedMsg)
	m.watched(msg)
	id := m.Current()
	if msg.err != nil || !slices.Equal(b.calls, []string{"set:" + id, "watch:" + id}) {
		t.Fatalf("startup: %+v, %v", msg, b.calls)
	}
	m.chat.directory = "/tmp/changed"
	cmd := run(m, "/new")
	if m.Current() == id || m.chat.directory != "/tmp/project" {
		t.Fatal("/new did not use the start path")
	}
	msg = cmd().(watchedMsg)
	m.watched(msg)
	if msg.err != nil || b.path != "/tmp/project" || len(b.calls) != 4 {
		t.Fatalf("/new: %+v, %v", msg, b.calls)
	}
}

func TestInitialDirectoryDoesNotOverwriteExisting(t *testing.T) {
	m, original := newModel(t)
	b := &initialDirectoryBackend{Backend: original, path: "/tmp/existing"}
	m.backend = b
	m.SetInitialDirectory("/tmp/start")
	m.Resume("resumed")
	if m.chat.fresh || m.chat.directory != "" {
		t.Fatal("Resume retained new-conversation state")
	}
	msg := m.watch(m.chat)().(watchedMsg)
	m.watched(msg)
	if !slices.Equal(b.calls, []string{"watch:resumed"}) || m.chat.directory != "/tmp/existing" {
		t.Fatalf("Resume: %v, %q", b.calls, m.chat.directory)
	}
	m.sessionsLoaded(sessionsMsg{list: []plugin.SessionInfo{{ID: "listed", Title: "existing"}}})
	msg = m.switchTo(m.chats["listed"])().(watchedMsg)
	m.watched(msg)
	if !slices.Equal(b.calls, []string{"watch:resumed", "watch:listed"}) {
		t.Fatalf("selected: %v", b.calls)
	}
}

func TestInitialDirectoryWatchRetries(t *testing.T) {
	for _, setFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "watch", true: "directory"}[setFails], func(t *testing.T) {
			m, original := newModel(t)
			failure := errors.New("offline")
			b := &initialDirectoryBackend{Backend: original}
			if setFails {
				b.setErr = failure
			} else {
				b.watchErr = failure
			}
			m.backend = b
			m.SetInitialDirectory("/tmp/start")
			id := m.Current()
			msg := m.watch(m.chat)().(watchedMsg)
			if !errors.Is(msg.err, failure) {
				t.Fatalf("failure: %+v", msg)
			}
			if cmd := m.watched(msg); cmd == nil || m.chat.watching {
				t.Fatal("no retry scheduled")
			}
			b.setErr, b.watchErr = nil, nil
			_, cmd := m.Update(rewatchMsg{id: id})
			msg = cmd().(watchedMsg)
			m.watched(msg)
			want := []string{"set:" + id, "watch:" + id, "watch:" + id}
			if setFails {
				want = []string{"set:" + id, "set:" + id, "watch:" + id}
			}
			if msg.err != nil || !slices.Equal(b.calls, want) || m.chat.fresh {
				t.Fatalf("retry: %+v, %v", msg, b.calls)
			}
		})
	}
}

func TestInitialDirectoryForAllProfiles(t *testing.T) {
	first, _ := newModel(t)
	second, _ := newModel(t)
	m := WithProfiles([]*Model{first, second}, 0)
	m.SetInitialDirectory("/tmp/start")
	first.Resume("existing")
	for _, model := range []*Model{first, second} {
		if model.initialDirectory != "/tmp/start" {
			t.Fatal("profile has no start path")
		}
		model.newConversation()
		if model.chat.directory != "/tmp/start" || !model.chat.fresh {
			t.Fatal("new conversation has no start path")
		}
	}
}

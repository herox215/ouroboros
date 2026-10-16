package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ouroboros/internal/history"
)

// SetInitialDirectory sets the directory assigned to newly created sessions.
// Call it before serving front ends. Empty disables the default; existing
// sessions, including explicitly reset ones, are never changed.
func (s *Service) SetInitialDirectory(path string) error {
	directory, err := resolveDirectory(path, "")
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrStopped
	}
	s.initialDirectory = directory
	return nil
}

// SetDirectory sets the project directory of an idle owner session. Empty
// resets it to the profile workspace, without restoring the initial directory.
func (s *Service) SetDirectory(_ context.Context, id, path string) error {
	if err := valid(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrStopped
	}
	se := s.get(id)
	if se.cancel != nil || len(se.tasks) != 0 {
		return ErrBusy
	}
	base := se.directory
	if base == "" {
		base = s.Workspace
	}
	if base == "" {
		base = s.initialDirectory
	}
	directory, err := resolveDirectory(path, base)
	if err != nil {
		return err
	}
	if s.archive != nil {
		if err := s.archive.SetDirectory(history.Owner(id), directory); err != nil {
			return fmt.Errorf("the directory of the session cannot be kept: %w", err)
		}
	}
	se.directory, se.directed = directory, true
	s.tell(se, s.directoryEvent(se))
	return nil
}

func resolveDirectory(path, base string) (string, error) {
	if path == "" {
		return "", nil
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	} else if strings.HasPrefix(path, "~") {
		return "", fmt.Errorf("directory %q: only ~ and ~/ are supported", path)
	}
	if !filepath.IsAbs(path) && base != "" {
		path = filepath.Join(base, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	return filepath.EvalSymlinks(path)
}

// directoryEvent preserves the usage counters of the last conversation event.
// s.mu is held for this and the archive helpers below.
func (s *Service) directoryEvent(se *session) Event {
	e := Event{Kind: Directory, At: s.now().Round(0), Text: se.directory}
	if n := len(se.events); n > 0 {
		e.Context, e.Cache = se.events[n-1].Context, se.events[n-1].Cache
	}
	return e
}

func (s *Service) keepDirectory(se *session) {
	if s.archive != nil {
		if err := s.archive.SetDirectory(history.Owner(se.id), se.directory); err != nil {
			s.archive.log("the directory of the session %s cannot be kept: %v", se.id, err)
		}
	}
}

func (s *Service) directoryOf(se *session) {
	if s.archive == nil {
		return
	}
	directory, ok, err := s.archive.Directory(history.Owner(se.id))
	if err != nil {
		s.archive.log("the directory of the session %s cannot be read: %v", se.id, err)
	}
	se.directory, se.directed = directory, ok
}

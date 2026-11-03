package session

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// waitingMessage is the ID of the message to the thread some session of s
// waits for, once one does.
func waitingMessage(t *testing.T, s *Service) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, se := range s.sessions {
			if se.waiting != "" && se.asked != nil {
				id := se.waiting
				s.mu.Unlock()
				return id
			}
		}
		s.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no session waits for the thread")
	return ""
}

func TestBackendOwnerQuestionStopsWithService(t *testing.T) {
	s := service(t, &fakeAgent{})
	done := make(chan error, 1)
	go func() {
		_, err := s.OwnerQuestion(context.Background(), json.RawMessage(`{"question":"Connect?"}`))
		done <- err
	}()
	waitingMessage(t, s)
	s.Close()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

// A backend approval is a permission request to the thread: the thread
// asks the owner and passes on their answer with the message ID, which it
// may not give itself.
func TestBackendOwnerQuestion(t *testing.T) {
	s := service(t, &fakeAgent{})
	done := make(chan string, 1)
	go func() {
		text, err := s.OwnerQuestion(t.Context(), json.RawMessage(`{"question":"Connect?","options":["Approve connection","Reject connection"]}`))
		if err != nil {
			done <- err.Error()
		} else {
			done <- text
		}
	}()
	id := waitingMessage(t, s)
	if _, _, err := s.answerMessage(t.Context(), id, "Approve connection", true); err == nil || !strings.Contains(err.Error(), "only the owner") {
		t.Fatalf("own answer: %v", err)
	}
	if _, _, err := s.answerMessage(t.Context(), id, "Reject connection", false); err != nil {
		t.Fatal(err)
	}
	if text := <-done; text != "Reject connection" {
		t.Fatal(text)
	}
}

func TestBackendOwnerQuestionOfAStoppedService(t *testing.T) {
	s := New(&fakeAgent{})
	s.Close()
	if _, err := s.OwnerQuestion(context.Background(), json.RawMessage(`{"question":"Connect?"}`)); !errors.Is(err, ErrStopped) {
		t.Fatalf("got %v", err)
	}
}

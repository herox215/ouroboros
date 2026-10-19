package session

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestBackendOwnerQuestionStopsWithService(t *testing.T) {
	s := service(t, &fakeAgent{})
	_, live, err := s.Watch(t.Context(), ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.OwnerQuestion(context.Background(), json.RawMessage(`{"question":"Connect?"}`))
		done <- err
	}()
	until(t, live, Asked)
	s.Close()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestBackendOwnerQuestion(t *testing.T) {
	s := service(t, &fakeAgent{})
	_, live, err := s.Watch(t.Context(), ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() {
		text, err := s.OwnerQuestion(t.Context(), json.RawMessage(`{"question":"Connect?","options":["Approve connection","Reject connection"]}`))
		if err != nil {
			done <- err.Error()
		} else {
			done <- text
		}
	}()
	events := until(t, live, Asked)
	if events[len(events)-1].AsQuestion().Question != "Connect?" {
		t.Fatalf("%+v", events)
	}
	if err := s.Steer(t.Context(), ThreadID, "Reject connection"); err != nil {
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

package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
)

// A session that works for the thread asks the owner nothing itself: its
// question goes to the thread as a message with an ID, and the answer the
// thread gives with that ID is what the session goes on with.
func TestRoutedQuestionGoesThroughTheThread(t *testing.T) {
	var reports []string
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if strings.HasPrefix(text, backgroundHead) {
			reports = append(reports, text)
			return "noted", nil
		}
		return agent.AskOwner(ctx, json.RawMessage(`{"question":"A or B?","options":["A","B"]}`))
	}}
	s := service(t, a)
	_, local, _ := s.Watch(t.Context(), "work")
	if _, _, err := s.hand(t.Context(), "work", "decide", nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	until(t, local, Asked)
	s.mu.Lock()
	id, permission := s.sessions["work"].waiting, s.sessions["work"].waitPermission
	s.mu.Unlock()
	if id == "" || permission {
		t.Fatalf("waiting %q, permission %v", id, permission)
	}
	if _, _, err := s.answerMessage(t.Context(), "nonexistent", "B", false); err == nil {
		t.Fatal("an unknown message was answered")
	}
	s.mu.Lock()
	s.sessions["work"].waitPermission = true // as a permission request is
	s.mu.Unlock()
	if _, _, err := s.answerMessage(t.Context(), id, "B", true); err == nil {
		t.Fatal("the thread answered a permission request itself")
	}
	if _, _, err := s.answerMessage(t.Context(), id, "B", false); err != nil {
		t.Fatal(err)
	}
	if e := until(t, local, Answer); e[len(e)-1].Text != "B" {
		t.Fatalf("answer %q", e[len(e)-1].Text)
	}
	s.answers.Wait()
	if len(reports) == 0 || !strings.Contains(reports[0], "message "+id) || !strings.Contains(reports[0], "A or B?") {
		t.Fatalf("thread was told %q", reports)
	}
}

// A wait a restart interrupted goes on under the same message ID, so that
// the thread's answer with it arrives; stopping the thread ends a wait
// without an answer.
func TestThreadMessageResumesAndStops(t *testing.T) {
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		if strings.HasPrefix(text, backgroundHead) {
			return "noted", nil
		}
		m := assistant.MessengerOf(ctx)
		if m == nil || m.Resume == nil {
			return "no messenger", nil
		}
		if text == "stop" {
			_, _, err := m.Post(ctx, "A or B?", true, false)
			return "stopped", err
		}
		reply, err := m.Resume(ctx, "A or B?", "SAMEID01")
		return reply.Text, err
	}}
	s := service(t, a)
	_, local, _ := s.Watch(t.Context(), "work")
	if _, _, err := s.hand(t.Context(), "work", "resume", nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if e := until(t, local, Asked); e[len(e)-1].Message != "SAMEID01" {
		t.Fatalf("asked %+v", e[len(e)-1])
	}
	if _, _, err := s.answerMessage(t.Context(), "SAMEID01", "B", false); err != nil {
		t.Fatal(err)
	}
	if e := until(t, local, Answer); e[len(e)-1].Text != "B" {
		t.Fatalf("answer %q", e[len(e)-1].Text)
	}
	s.answers.Wait()
	if _, _, err := s.hand(t.Context(), "work", "stop", nil, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	until(t, local, Asked)
	s.mu.Lock()
	s.stopThread()
	s.mu.Unlock()
	until(t, local, Canceled)
	s.answers.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	if se := s.sessions["work"]; se.waiting != "" || se.asked != nil {
		t.Fatalf("still waits for %q", se.waiting)
	}
}

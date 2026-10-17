package matrix

import (
	"context"
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"ouroboros/plugin"
)

func deliveryConversation(t *testing.T) (*conversations, *conversation, *testSessions) {
	t.Helper()
	s, _ := conversationStore(t)
	sessions := &testSessions{}
	c, err := openConversations(s, sessions)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.receive(t.Context(), &recordingConnection{}, incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range c.entries {
		return c, entry, sessions
	}
	t.Fatal("no conversation")
	return nil, nil, nil
}

func TestDeliveryIDsSurviveReplayAndRepeatedText(t *testing.T) {
	c, entry, sessions := deliveryConversation(t)
	conn := &recordingConnection{}
	events := []plugin.SessionEvent{
		{Kind: plugin.SessionSay, PresentationID: "say-1", Text: "same", At: time.Unix(1, 0), Context: 100},
		{Kind: plugin.SessionAnswer, PresentationID: "answer-1", Text: "same", At: time.Unix(2, 0), Context: 200},
		{Kind: plugin.SessionFailed, PresentationID: "failure-1", Text: "private live error"},
		{Kind: plugin.SessionCanceled, PresentationID: "cancel-1"},
	}
	if err := c.publish(t.Context(), conn, entry, events); err != nil {
		t.Fatal(err)
	}
	if len(conn.messages()) != 4 {
		t.Fatalf("initial deliveries: %v", conn.messages())
	}
	c, err := openConversations(c.store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range c.entries {
		entry = e
	}
	for i := range events {
		events[i].At = time.Unix(99, 0)
		events[i].Context = 999
		events[i].Err = "different envelope"
	}
	// Replay can reconstruct an assistant message with a different presentation kind.
	events[0].Kind = plugin.SessionAnswer
	events[2].Text = "the answer failed"
	events = append([]plugin.SessionEvent{{Kind: plugin.SessionEffort, Text: "high"}}, events...)
	events = append(events, plugin.SessionEvent{Kind: plugin.SessionAnswer, PresentationID: "answer-2", Text: "same"})
	if err := c.publish(t.Context(), conn, entry, events); err != nil {
		t.Fatal(err)
	}
	if len(conn.messages()) != 5 {
		t.Fatalf("replayed or skipped output: %v", conn.messages())
	}
	if err := c.publish(t.Context(), conn, entry, events); err != nil {
		t.Fatal(err)
	}
	if len(conn.messages()) != 5 {
		t.Fatal("repeat poll redelivered output")
	}
}

func TestDeliveryFallbackIgnoresMetadataAndKeepsOccurrences(t *testing.T) {
	c, entry, _ := deliveryConversation(t)
	conn := &recordingConnection{}
	events := []plugin.SessionEvent{{Kind: plugin.SessionAnswer, Text: "same", At: time.Unix(1, 0)}}
	if err := c.publish(t.Context(), conn, entry, events); err != nil {
		t.Fatal(err)
	}
	events[0].At = time.Unix(2, 0)
	events[0].Context = 200
	events = append(events, plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "same"})
	if err := c.publish(t.Context(), conn, entry, events); err != nil {
		t.Fatal(err)
	}
	if got := conn.messages(); len(got) != 2 || got[0] != got[1] {
		t.Fatalf("occurrences: %v", got)
	}
}

func TestLegacyDeliveryBaselinePersistsAndRetries(t *testing.T) {
	c, entry, sessions := deliveryConversation(t)
	entry.SentVersion = 0
	entry.Sent = map[string]bool{"irreversible-old-hash": true}
	if err := c.save(); err != nil {
		t.Fatal(err)
	}
	c, err := openConversations(c.store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range c.entries {
		entry = e
	}
	conn := &recordingConnection{}
	events := []plugin.SessionEvent{{Kind: plugin.SessionAnswer, PresentationID: "old", Text: "old history"}}
	// A failed baseline save must not discard the old ledger or send history.
	if err := os.Remove(c.store.path("conversations.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(c.store.path("conversations.json"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := c.publish(t.Context(), conn, entry, events); err == nil {
		t.Fatal("expected baseline save failure")
	}
	if entry.SentVersion != 0 || !entry.Sent["irreversible-old-hash"] || len(conn.messages()) != 0 {
		t.Fatal("failed baseline changed delivery state")
	}
	if err := os.Remove(c.store.path("conversations.json")); err != nil {
		t.Fatal(err)
	}
	if err := c.publish(t.Context(), conn, entry, events); err != nil {
		t.Fatal(err)
	}
	c, err = openConversations(c.store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range c.entries {
		entry = e
	}
	if entry.SentVersion != 1 || !entry.Sent["id:old"] {
		t.Fatal("baseline not persisted")
	}
	events = append(events, plugin.SessionEvent{Kind: plugin.SessionAnswer, PresentationID: "new", Text: "new answer"})
	if err := c.publish(t.Context(), conn, entry, events); err != nil {
		t.Fatal(err)
	}
	if got := conn.messages(); !slices.Equal(got, []string{"!room:example.org @owner:example.org new answer"}) {
		t.Fatalf("baseline output: %v", got)
	}
}

func TestDeliveryRetriesFailedSend(t *testing.T) {
	c, entry, _ := deliveryConversation(t)
	failure := errors.New("send failed")
	conn := &recordingConnection{fail: []error{failure}}
	events := []plugin.SessionEvent{{Kind: plugin.SessionAnswer, PresentationID: "retry", Text: "hello"}}
	if err := c.publish(context.Background(), conn, entry, events); !errors.Is(err, failure) {
		t.Fatalf("send: %v", err)
	}
	if entry.Sent["id:retry"] {
		t.Fatal("marked failed send delivered")
	}
	if err := c.publish(t.Context(), conn, entry, events); err != nil {
		t.Fatal(err)
	}
	if len(conn.messages()) != 1 {
		t.Fatal("retry missing")
	}
}

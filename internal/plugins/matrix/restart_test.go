package matrix

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"ouroboros/plugin"
)

func TestRestartNotificationsPersistDelivery(t *testing.T) {
	c, entry, sessions := deliveryConversation(t)
	conn := &recordingConnection{}
	starting := plugin.SessionEvent{Kind: plugin.SessionRestart, PresentationID: "restart-start", Text: "/private/binary/path"}
	if err := c.publish(t.Context(), conn, entry, []plugin.SessionEvent{starting}); err != nil {
		t.Fatal(err)
	}
	c, err := openConversations(c.store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range c.entries {
		entry = e
	}
	completed := plugin.SessionEvent{Kind: plugin.SessionRestarted, PresentationID: "restart-done"}
	events := []plugin.SessionEvent{starting, completed}
	for range 2 {
		if err := c.publish(t.Context(), conn, entry, events); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"!room:example.org @owner:example.org Restarting…", "!room:example.org @owner:example.org Restarted."}
	if got := conn.messages(); !slices.Equal(got, want) {
		t.Fatalf("messages %q; want %q", got, want)
	}
	// Reopening after completion must retain its delivery acknowledgement.
	c, err = openConversations(c.store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range c.entries {
		entry = e
	}
	if err := c.publish(t.Context(), conn, entry, events); err != nil {
		t.Fatal(err)
	}
	if len(conn.messages()) != 2 {
		t.Fatal("completion redelivered after reconnect")
	}
	// Another rebuild gets another notification, not suppressed by identical text.
	completed.PresentationID = "another-restart-done"
	if err := c.publish(t.Context(), conn, entry, append(events, completed)); err != nil {
		t.Fatal(err)
	}
	if len(conn.messages()) != 3 {
		t.Fatal("later restart suppressed")
	}
}

func TestRestartCompletionDeliveredByPolling(t *testing.T) {
	c, _, sessions := deliveryConversation(t)
	sessions.set(plugin.SessionEvent{Kind: plugin.SessionRestarted, PresentationID: "ready"})
	conn := &outputConnection{output: make(chan string, 2)}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); c.poll(ctx, conn) }()
	defer func() { cancel(); <-done }()
	select {
	case got := <-conn.output:
		if got != "Restarted." {
			t.Fatalf("notification: %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("poll did not deliver restart notification")
	}
}

func TestRestartNotificationRetriesFailedSend(t *testing.T) {
	c, entry, _ := deliveryConversation(t)
	failure := errors.New("offline")
	conn := &recordingConnection{fail: []error{failure}}
	events := []plugin.SessionEvent{{Kind: plugin.SessionRestarted, PresentationID: "done"}}
	if err := c.publish(t.Context(), conn, entry, events); !errors.Is(err, failure) {
		t.Fatalf("send: %v", err)
	}
	if entry.Sent["id:done"] {
		t.Fatal("failed send acknowledged")
	}
	if err := c.publish(t.Context(), conn, entry, events); err != nil {
		t.Fatal(err)
	}
	if len(conn.messages()) != 1 {
		t.Fatal("completion not retried")
	}
}

package matrix

import (
	"encoding/json"
	"strings"
	"testing"

	"ouroboros/internal/openai"
	"ouroboros/plugin"
)

func TestMatrixMetadataReachesModelContext(t *testing.T) {
	store, _ := conversationStore(t)
	sessions := &sourceSessions{}
	conn := &outputConnection{output: make(chan string, 8)}
	c, err := openConversations(store, sessions)
	if err != nil {
		t.Fatal(err)
	}
	for _, reply := range []bool{false, true} {
		sessions.set()
		if reply {
			sessions.set(plugin.SessionEvent{Kind: plugin.SessionAsked})
		}
		metadata := map[string]any{"location": map[string]any{"uri": "geo:1,2", "active": true, "timestamp": int64(123)}}
		msg := incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "where?", Metadata: metadata}
		if err := c.receive(t.Context(), conn, msg); err != nil {
			t.Fatal(err)
		}
		source := sessions.source
		metadata["location"].(map[string]any)["uri"] = "geo:9,9"
		// The same source envelope survives archive/socket JSON and model rendering.
		data, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		var restored openai.MessageSource
		if err = json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		model := openai.MessagesForModel([]openai.Message{{Role: "user", Content: msg.Text, Source: restored}})[0]
		if !strings.Contains(model.Content, "gateway = matrix") || !strings.Contains(model.Content, `"uri":"geo:1,2"`) || strings.Contains(model.Content, "geo:9,9") {
			t.Fatalf("model context: %s", model.Content)
		}
		if source.Interface != "api" || source.Gateway != "matrix" {
			t.Fatalf("source: %+v", source)
		}
	}
	sessions.set()
	if err = c.receive(t.Context(), conn, incoming{RoomID: "!room:example.org", Sender: "@owner:example.org", Text: "no context"}); err != nil {
		t.Fatal(err)
	}
	if sessions.source.Metadata != "" {
		t.Fatal("metadata leaked into next message")
	}
}

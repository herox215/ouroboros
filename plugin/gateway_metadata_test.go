package plugin_test

import (
	"context"
	"reflect"
	"testing"

	"ouroboros/plugin"
)

func TestGatewayMetadataPerCall(t *testing.T) {
	metadata, err := plugin.NewGatewayMetadata(map[string]any{"room": map[string]any{"topic": "snapshot"}})
	if err != nil {
		t.Fatal(err)
	}
	forged, err := plugin.NewGatewayMetadata(map[string]any{"forged": true})
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range []string{"send", "reply", "send images", "reply images", "send files", "reply files", "send empty images", "reply empty images", "send empty files", "reply empty files"} {
		t.Run(operation, func(t *testing.T) {
			imageBackend := &gatewayImageBackend{}
			fileBackend := &gatewayAttachmentBackend{}
			var sessions plugin.GatewaySessions = imageBackend
			backend := &imageBackend.gatewayBackend
			if operation == "send files" || operation == "reply files" || operation == "send empty files" || operation == "reply empty files" {
				sessions = fileBackend
				backend = &fileBackend.gatewayBackend
			}
			g := plugin.NewGatewayAdapter(sessions, gatewayOwner, plugin.WithGatewayName("custom.bridge-42"))
			id, err := g.Open(t.Context(), gatewayAlice)
			if err != nil {
				t.Fatal(err)
			}
			images := []plugin.Image{{Type: "image/png", Data: []byte("harmless image fixture")}}
			files := []plugin.Attachment{{Name: "fixture.txt"}}
			send := func(ctx context.Context) error {
				switch operation {
				case "send":
					return g.Send(ctx, gatewayAlice, id, "hello")
				case "reply":
					return g.Reply(ctx, gatewayAlice, id, "hello")
				case "send images":
					return g.SendImages(ctx, gatewayAlice, id, "hello", images)
				case "reply images":
					return g.ReplyImages(ctx, gatewayAlice, id, "hello", images)
				case "send files":
					return g.SendAttachments(ctx, gatewayAlice, id, "hello", files)
				case "reply files":
					return g.ReplyAttachments(ctx, gatewayAlice, id, "hello", files)
				case "send empty images":
					return g.SendImages(ctx, gatewayAlice, id, "hello", nil)
				case "reply empty images":
					return g.ReplyImages(ctx, gatewayAlice, id, "hello", nil)
				case "send empty files":
					return g.SendAttachments(ctx, gatewayAlice, id, "hello", nil)
				default:
					return g.ReplyAttachments(ctx, gatewayAlice, id, "hello", nil)
				}
			}
			parent := plugin.WithMessageSource(t.Context(), plugin.MessageSource{Interface: "api", Gateway: "forged", Metadata: forged})
			ctx := plugin.WithGatewayMetadata(parent, metadata)
			// A call with an explicit snapshot must replace all inherited attribution.
			if err := send(ctx); err != nil {
				t.Fatal(err)
			}
			want := plugin.MessageSource{Interface: "api", Gateway: "custom.bridge-42", Metadata: metadata}
			if backend.source != want || backend.text != "hello" {
				t.Fatalf("forwarding: source=%+v text=%q, want %+v", backend.source, backend.text, want)
			}
			if operation == "send images" || operation == "reply images" {
				if !reflect.DeepEqual(imageBackend.images, images) {
					t.Fatalf("images lost: %+v", imageBackend.images)
				}
			}
			if operation == "send files" || operation == "reply files" {
				if !reflect.DeepEqual(fileBackend.files, files) {
					t.Fatalf("files lost: %+v", fileBackend.files)
				}
			}
			// Reusing this adapter and session must not retain the previous snapshot,
			// nor inherit a snapshot hidden inside a caller-supplied MessageSource.
			for _, next := range []context.Context{parent, t.Context(), plugin.WithGatewayMetadata(parent, ""), plugin.WithGatewayMetadata(parent, plugin.GatewayMetadata(`[]`))} {
				if err := send(next); err != nil {
					t.Fatal(err)
				}
				want.Metadata = ""
				if backend.source != want {
					t.Fatalf("metadata/origin leaked to next call: %+v, want %+v", backend.source, want)
				}
			}
			// Per-call normalization must not mutate the original reusable snapshot.
			if err := send(ctx); err != nil {
				t.Fatal(err)
			}
			want.Metadata = metadata
			if backend.source != want {
				t.Fatalf("original snapshot changed: %+v", backend.source)
			}
			if operation == "reply" || operation == "reply images" || operation == "reply files" || operation == "reply empty images" || operation == "reply empty files" {
				if backend.steer != 6 || backend.ask != 0 {
					t.Fatalf("wrong reply dispatch: ask=%d steer=%d", backend.ask, backend.steer)
				}
			} else if backend.ask != 6 || backend.steer != 0 {
				t.Fatalf("wrong send dispatch: ask=%d steer=%d", backend.ask, backend.steer)
			}
		})
	}
}

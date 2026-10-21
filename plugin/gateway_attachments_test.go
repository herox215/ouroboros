package plugin_test

import (
	"context"
	"errors"
	"testing"

	"ouroboros/plugin"
)

type gatewayAttachmentBackend struct {
	gatewayBackend
	files []plugin.Attachment
}

func (b *gatewayAttachmentBackend) AskAttachments(ctx context.Context, id, text string, files []plugin.Attachment) error {
	b.files = files
	return b.Ask(ctx, id, text)
}
func (b *gatewayAttachmentBackend) SteerAttachments(ctx context.Context, id, text string, files []plugin.Attachment) error {
	b.files = files
	return b.Steer(ctx, id, text)
}

func TestGatewayAttachments(t *testing.T) {
	for _, reply := range []bool{false, true} {
		backend := &gatewayAttachmentBackend{}
		denied := false
		var op plugin.GatewayOperation
		g := plugin.NewGatewayAdapter(backend, func(_ context.Context, _ plugin.GatewayIdentity, o plugin.GatewayOperation) (plugin.GatewayPermission, error) {
			op = o
			return plugin.GatewayPermission{OwnerSession: !denied}, nil
		}, plugin.WithGatewayName("matrix"))
		id, err := g.Open(t.Context(), gatewayAlice)
		if err != nil {
			t.Fatal(err)
		}
		send := g.SendAttachments
		wantOp := plugin.GatewaySend
		if reply {
			send = g.ReplyAttachments
			wantOp = plugin.GatewayReply
		}
		files := []plugin.Attachment{{Name: "empty"}}
		other := plugin.GatewayIdentity{Gateway: gatewayAlice.Gateway, Subject: "other"}
		if err := send(t.Context(), other, id, "", files); !errors.Is(err, plugin.ErrGatewaySession) {
			t.Fatalf("binding: %v", err)
		}
		denied = true
		if err := send(t.Context(), gatewayAlice, id, "", files); !errors.Is(err, plugin.ErrGatewayDenied) {
			t.Fatalf("revocation: %v", err)
		}
		denied = false
		if backend.ask+backend.steer != 0 {
			t.Fatal("denied input reached backend")
		}
		backend.err = plugin.ErrBusy
		if err := send(t.Context(), gatewayAlice, id, "caption", files); !errors.Is(err, plugin.ErrBusy) {
			t.Fatalf("forwarded error: %v", err)
		}
		if op != wantOp || backend.text != "caption" || backend.source.Gateway != "matrix" || len(backend.files) != 1 || (reply && backend.ask != 0) || (!reply && backend.steer != 0) {
			t.Fatalf("forwarding: %+v op=%s", backend, op)
		}
		legacyBackend := &gatewayBackend{}
		legacy := plugin.NewGatewayAdapter(legacyBackend, gatewayOwner)
		id, err = legacy.Open(t.Context(), gatewayAlice)
		if err != nil {
			t.Fatal(err)
		}
		send = legacy.SendAttachments
		if reply {
			send = legacy.ReplyAttachments
		}
		if err := send(t.Context(), gatewayAlice, id, "", files); !errors.Is(err, plugin.ErrGatewayAttachments) {
			t.Fatalf("unsupported: %v", err)
		}
		if err := send(t.Context(), gatewayAlice, id, "text", nil); err != nil || legacyBackend.ask+legacyBackend.steer != 1 {
			t.Fatalf("fallback: %v", err)
		}
	}
}

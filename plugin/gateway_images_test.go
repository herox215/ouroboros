package plugin_test

import (
	"context"
	"errors"
	"testing"

	"ouroboros/plugin"
)

type gatewayImageBackend struct {
	gatewayBackend
	images []plugin.Image
}

func (s *gatewayImageBackend) AskImages(ctx context.Context, id, text string, images []plugin.Image) error {
	s.images = images
	return s.Ask(ctx, id, text)
}

func TestGatewayImageInput(t *testing.T) {
	backend := &gatewayImageBackend{}
	revoked := false
	var op plugin.GatewayOperation
	g := plugin.NewGatewayAdapter(backend, func(_ context.Context, _ plugin.GatewayIdentity, operation plugin.GatewayOperation) (plugin.GatewayPermission, error) {
		op = operation
		return plugin.GatewayPermission{OwnerSession: !revoked}, nil
	})
	id, err := g.Open(t.Context(), gatewayAlice)
	if err != nil {
		t.Fatal(err)
	}
	images := []plugin.Image{{Type: "image/png", Data: []byte("picture")}}
	other := plugin.GatewayIdentity{Gateway: gatewayAlice.Gateway, Subject: "other"}
	if err := g.SendImages(t.Context(), other, id, "", images); !errors.Is(err, plugin.ErrGatewaySession) {
		t.Fatalf("identity: %v", err)
	}
	if err := g.SendImages(t.Context(), gatewayAlice, "unknown", "", images); !errors.Is(err, plugin.ErrGatewaySession) {
		t.Fatalf("binding: %v", err)
	}
	revoked = true
	if err := g.SendImages(t.Context(), gatewayAlice, id, "", images); !errors.Is(err, plugin.ErrGatewayDenied) {
		t.Fatalf("revoked: %v", err)
	}
	revoked = false
	if backend.ask != 0 {
		t.Fatal("unauthorized image reached backend")
	}
	backend.err = plugin.ErrBusy
	if err := g.SendImages(t.Context(), gatewayAlice, id, "caption", images); !errors.Is(err, plugin.ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	if op != plugin.GatewaySend || backend.steer != 0 || backend.source.Interface != "api" || backend.text != "caption" || len(backend.images) != 1 || string(backend.images[0].Data) != "picture" {
		t.Fatalf("forwarding: %+v, %s", backend, op)
	}
	textOnly := &gatewayBackend{}
	legacy := plugin.NewGatewayAdapter(textOnly, gatewayOwner)
	id, err = legacy.Open(t.Context(), gatewayAlice)
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.SendImages(t.Context(), gatewayAlice, id, "caption", images); !errors.Is(err, plugin.ErrGatewayImages) {
		t.Fatalf("unsupported: %v", err)
	}
	if textOnly.ask != 0 {
		t.Fatal("pictures silently lost")
	}
	if err := legacy.SendImages(t.Context(), gatewayAlice, id, "text", nil); err != nil || textOnly.ask != 1 {
		t.Fatalf("text fallback: %v", err)
	}
}

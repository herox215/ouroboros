package plugin_test

import (
	"ouroboros/plugin"
	"testing"
)

func TestGatewaySourceAttribution(t *testing.T) {
	for _, name := range []string{"matrix", "whatsapp", "future.transport-42", ""} {
		t.Run(name, func(t *testing.T) {
			backend := &gatewayImageBackend{}
			g := plugin.NewGatewayAdapter(backend, gatewayOwner, plugin.WithGatewayName(name))
			id, err := g.Open(t.Context(), gatewayAlice)
			if err != nil {
				t.Fatal(err)
			}
			g, err = plugin.NewGatewayAdapterWithBindings(backend, gatewayOwner, g.Bindings(), plugin.WithGatewayName(name))
			if err != nil {
				t.Fatal(err)
			}
			// The trusted adapter must replace inherited attribution, not leak it.
			ctx := plugin.WithMessageSource(t.Context(), plugin.MessageSource{Interface: "api", Gateway: "wrong"})
			for operation, send := range map[string]func() error{
				"send": func() error { return g.Send(ctx, gatewayAlice, id, "hello") },
				"image": func() error {
					return g.SendImages(ctx, gatewayAlice, id, "caption", []plugin.Image{{Type: "image/png", Data: []byte("picture")}})
				},
				"text fallback": func() error { return g.SendImages(ctx, gatewayAlice, id, "hello", nil) },
				"reply":         func() error { return g.Reply(ctx, gatewayAlice, id, "yes") },
			} {
				if err := send(); err != nil {
					t.Fatalf("%s: %v", operation, err)
				}
				if want := (plugin.MessageSource{Interface: "api", Gateway: name}); backend.source != want {
					t.Fatalf("%s source: %+v, want %+v", operation, backend.source, want)
				}
			}
		})
	}
}

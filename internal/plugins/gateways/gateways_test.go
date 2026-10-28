package gateways

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/kernel"
	"ouroboros/plugin"
)

// Providers deliberately start after the tools: discovery must not be cached.
func fixture(t *testing.T, providers ...plugin.GatewayOutput) (map[string]plugin.Tool, *plugin.Context) {
	t.Helper()
	var pctx *plugin.Context
	k := kernel.New()
	err := k.Start(Plugin(), plugin.New("fixtures", func(ctx *plugin.Context, _ struct{}) error {
		pctx = ctx
		for _, p := range providers {
			plugin.Add(ctx, p)
		}
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.Stop)
	tools := make(map[string]plugin.Tool)
	for _, tool := range plugin.All[plugin.Tool](pctx) {
		tools[tool.Name] = tool
	}
	if len(tools) != 5 {
		t.Fatalf("got %d tools", len(tools))
	}
	for _, tool := range tools {
		if !tool.OwnerOnly || !json.Valid([]byte(tool.Schema)) {
			t.Fatalf("invalid owner-only tool: %+v", tool)
		}
	}
	return tools, pctx
}

func imageOK(context.Context, plugin.GatewayImageRequest) (string, error)   { return "image", nil }
func audioOK(context.Context, plugin.GatewayAudioRequest) (string, error)   { return "audio", nil }
func avatarOK(context.Context, plugin.GatewayAvatarRequest) (string, error) { return "avatar", nil }

func TestDispatchAndForwarding(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var images, audio, avatars int
	tools, _ := fixture(t, plugin.GatewayOutput{
		Name: "fake",
		SendImage: func(got context.Context, req plugin.GatewayImageRequest) (string, error) {
			images++
			if got != ctx {
				t.Fatal("original context was replaced")
			}
			want := plugin.GatewayImageRequest{Path: "relative/image.png", Destination: "opaque destination"}
			if images == 2 {
				want = plugin.GatewayImageRequest{Attachment: 3}
			}
			if req != want {
				t.Fatalf("image request %+v, want %+v", req, want)
			}
			return "sent fixture", nil
		},
		SendAudio: func(got context.Context, req plugin.GatewayAudioRequest) (string, error) {
			audio++
			if got != ctx {
				t.Fatal("original context was replaced")
			}
			want := plugin.GatewayAudioRequest{Path: "relative/click.wav", Destination: "opaque destination"}
			if req != want {
				t.Fatalf("audio request %+v, want %+v", req, want)
			}
			return "sent audio", nil
		},
		SetAvatar: func(got context.Context, req plugin.GatewayAvatarRequest) (string, error) {
			avatars++
			if got != ctx {
				t.Fatal("original context was replaced")
			}
			want := []plugin.GatewayAvatarRequest{{Attachment: 1}, {Path: "avatar.png"}, {Attachment: 2}}[avatars-1]
			if req != want {
				t.Fatalf("avatar request %+v, want %+v", req, want)
			}
			return "set fixture", nil
		},
	})
	for _, raw := range []string{`{"gateway":"fake","path":"relative/image.png","destination":"opaque destination"}`, `{"attachment":3}`} {
		if got, err := tools[plugin.GatewaySendImageTool].Run(ctx, json.RawMessage(raw)); err != nil || got != "sent fixture" {
			t.Fatalf("image: %q %v", got, err)
		}
	}
	if got, err := tools[plugin.GatewaySendAudioTool].Run(ctx, json.RawMessage(`{"gateway":"fake","path":"relative/click.wav","destination":"opaque destination"}`)); err != nil || got != "sent audio" {
		t.Fatalf("audio: %q %v", got, err)
	}
	for _, raw := range []string{`{}`, `{"gateway":"fake","path":"avatar.png"}`, `{"attachment":2}`} {
		if got, err := tools[plugin.GatewaySetAvatarTool].Run(ctx, json.RawMessage(raw)); err != nil || got != "set fixture" {
			t.Fatalf("avatar: %q %v", got, err)
		}
	}
	if images != 2 || audio != 1 || avatars != 3 {
		t.Fatalf("calls: %d images, %d audio, %d avatars", images, audio, avatars)
	}
}

func TestAudioRouting(t *testing.T) {
	tools, _ := fixture(t, plugin.GatewayOutput{Name: "one", SendAudio: audioOK})
	if got, err := tools[plugin.GatewaySendAudioTool].Run(t.Context(), json.RawMessage(`{"gateway":"one","path":"click.wav"}`)); err != nil || got != "audio" {
		t.Fatalf("audio: %q %v", got, err)
	}
	if _, err := tools[plugin.GatewaySendAudioTool].Run(t.Context(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("without a path: %v", err)
	}
	tools, _ = fixture(t, plugin.GatewayOutput{Name: "one", SendImage: imageOK})
	if _, err := tools[plugin.GatewaySendAudioTool].Run(t.Context(), json.RawMessage(`{"gateway":"one","path":"click.wav"}`)); err == nil || !strings.Contains(err.Error(), "does not support") {
		t.Fatalf("unsupported: %v", err)
	}
}

func TestRoutingErrors(t *testing.T) {
	for _, avatar := range []bool{false, true} {
		name := plugin.GatewaySendImageTool
		supported := plugin.GatewayOutput{Name: "one", SendImage: imageOK}
		unsupported := plugin.GatewayOutput{Name: "other", SetAvatar: avatarOK}
		if avatar {
			name = plugin.GatewaySetAvatarTool
			supported = plugin.GatewayOutput{Name: "one", SetAvatar: avatarOK}
			unsupported = plugin.GatewayOutput{Name: "other", SendImage: imageOK}
		}
		second := supported
		second.Name = "two"
		for _, tc := range []struct {
			label     string
			providers []plugin.GatewayOutput
			gateway   string
			want      string
		}{
			{"none", nil, "", "no gateway provider"},
			{"no matching capability", []plugin.GatewayOutput{unsupported}, "", "no gateway provider"},
			{"ambiguous", []plugin.GatewayOutput{supported, second}, "", "ambiguous"},
			{"unknown", []plugin.GatewayOutput{supported}, "missing", "unknown"},
			{"unsupported", []plugin.GatewayOutput{supported, unsupported}, "other", "does not support"},
			{"duplicate", []plugin.GatewayOutput{supported, supported}, "one", "duplicate"},
			{"empty name", []plugin.GatewayOutput{{}}, "", "empty name"},
		} {
			t.Run(name+"/"+tc.label, func(t *testing.T) {
				tools, _ := fixture(t, tc.providers...)
				raw, _ := json.Marshal(map[string]any{"gateway": tc.gateway, "attachment": 1})
				if _, err := tools[name].Run(t.Context(), raw); err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("want %q, got %v", tc.want, err)
				}
			})
		}
		tools, _ := fixture(t, supported, unsupported)
		if _, err := tools[name].Run(t.Context(), []byte(`{"attachment":1}`)); err != nil {
			t.Fatalf("one capable provider: %v", err)
		}
		tools, _ = fixture(t, supported, second)
		if _, err := tools[name].Run(t.Context(), []byte(`{"gateway":"two","attachment":1}`)); err != nil {
			t.Fatalf("explicit provider: %v", err)
		}
	}
}

func TestInputValidation(t *testing.T) {
	calls := 0
	tools, _ := fixture(t, plugin.GatewayOutput{Name: "fake",
		SendImage: func(context.Context, plugin.GatewayImageRequest) (string, error) { calls++; return "", nil },
		SetAvatar: func(context.Context, plugin.GatewayAvatarRequest) (string, error) { calls++; return "", nil },
	})
	for _, name := range []string{plugin.GatewaySendImageTool, plugin.GatewaySetAvatarTool} {
		invalid := []string{`{"attachment":-1}`, `{"path":"a.png","attachment":-1}`, `{"path":"a.png","attachment":1}`, `{"attachment":1.5}`, `{"attachment":"invalid"}`}
		if name == plugin.GatewaySendImageTool {
			invalid = append(invalid, `{}`, `{"attachment":0}`)
		}
		for _, raw := range invalid {
			if _, err := tools[name].Run(t.Context(), json.RawMessage(raw)); err == nil {
				t.Errorf("%s accepted %s", name, raw)
			}
		}
	}
	if calls != 0 {
		t.Fatalf("invalid input reached backend %d times", calls)
	}
}

// The Claude API rejects a tool schema that holds oneOf, allOf or anyOf at its top
// level; validateInput alone enforces which arguments go together.
func TestSchemaHasNoTopLevelCombinator(t *testing.T) {
	tools, _ := fixture(t)
	for _, name := range []string{plugin.GatewaySendImageTool, plugin.GatewaySendAudioTool, plugin.GatewaySetAvatarTool, plugin.GatewayVotePollTool} {
		var schema map[string]any
		if err := json.Unmarshal([]byte(tools[name].Schema), &schema); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, key := range []string{"oneOf", "allOf", "anyOf"} {
			if _, ok := schema[key]; ok {
				t.Errorf("%s schema has top-level %s", name, key)
			}
		}
	}
}

func TestListingAndLateDiscovery(t *testing.T) {
	tools, ctx := fixture(t)
	if got, err := tools["gateway_list"].Run(t.Context(), []byte(`{}`)); err != nil || got != "[]" {
		t.Fatalf("empty list: %s %v", got, err)
	}
	plugin.Add(ctx, plugin.GatewayOutput{Name: "late", ImageDescription: "Image restrictions", AvatarDescription: "Public avatar restrictions", SendImage: imageOK})
	got, err := tools["gateway_list"].Run(t.Context(), []byte(`{}`))
	want := `[{"name":"late","send_image":true,"send_audio":false,"set_avatar":false,"image_description":"Image restrictions","audio_description":"","avatar_description":"Public avatar restrictions","vote_poll":false,"poll_description":""}]`
	if err != nil || got != want {
		t.Fatalf("list: %s %v", got, err)
	}
	if got, err := tools[plugin.GatewaySendImageTool].Run(t.Context(), []byte(`{"attachment":1}`)); err != nil || got != "image" {
		t.Fatalf("late dispatch: %q %v", got, err)
	}
	plugin.Add(ctx, plugin.GatewayOutput{Name: "late", SetAvatar: avatarOK})
	for name, tool := range tools {
		raw := `{"attachment":1}`
		if name == plugin.GatewayVotePollTool {
			raw = `{"poll_id":"poll","option_ids":[]}`
		}
		if name == plugin.GatewaySendAudioTool {
			raw = `{"path":"click.wav"}`
		}
		if _, err := tool.Run(t.Context(), []byte(raw)); err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Errorf("%s failed to reject duplicate: %v", name, err)
		}
	}
}

func TestPeerDenied(t *testing.T) {
	tools, _ := fixture(t, plugin.GatewayOutput{Name: "fake",
		SendImage: func(context.Context, plugin.GatewayImageRequest) (string, error) {
			t.Fatal("peer reached backend")
			return "", nil
		},
		VotePoll: func(context.Context, plugin.GatewayPollVoteRequest) (string, error) {
			t.Fatal("peer reached vote backend")
			return "", nil
		},
		SetAvatar: func(context.Context, plugin.GatewayAvatarRequest) (string, error) {
			t.Fatal("peer reached backend")
			return "", nil
		},
	})
	for _, origin := range []a2a.Origin{{PublicKey: "peer"}, {}} {
		ctx := a2a.WithOrigin(t.Context(), origin)
		for name, tool := range tools {
			raw := `{"attachment":1}`
			if name == plugin.GatewayVotePollTool {
				raw = `{"poll_id":"poll","option_ids":[]}`
			}
			if name == plugin.GatewaySendAudioTool {
				raw = `{"path":"click.wav"}`
			}
			if _, err := tool.Run(ctx, []byte(raw)); err == nil || !strings.Contains(err.Error(), "owner-only") {
				t.Errorf("%s allowed peer: %v", name, err)
			}
		}
	}
}

func TestSourceDoesNotRoute(t *testing.T) {
	tools, _ := fixture(t,
		plugin.GatewayOutput{Name: "one", SendImage: imageOK, SetAvatar: avatarOK},
		plugin.GatewayOutput{Name: "two", SendImage: imageOK, SetAvatar: avatarOK},
	)
	ctx := plugin.WithMessageSource(t.Context(), plugin.MessageSource{Interface: "api", Gateway: "one"})
	for _, name := range []string{plugin.GatewaySendImageTool, plugin.GatewaySetAvatarTool} {
		if _, err := tools[name].Run(ctx, []byte(`{"attachment":1}`)); err == nil || !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("%s routed by source: %v", name, err)
		}
	}
}

func TestBackendErrorForwarded(t *testing.T) {
	want := errors.New("fixture refusal")
	tools, _ := fixture(t, plugin.GatewayOutput{Name: "fake",
		SendImage: func(context.Context, plugin.GatewayImageRequest) (string, error) { return "image detail", want },
		SetAvatar: func(context.Context, plugin.GatewayAvatarRequest) (string, error) { return "avatar detail", want },
	})
	for _, name := range []string{plugin.GatewaySendImageTool, plugin.GatewaySetAvatarTool} {
		if got, err := tools[name].Run(t.Context(), []byte(`{"attachment":1}`)); !errors.Is(err, want) || !strings.Contains(got, "detail") {
			t.Fatalf("%s: %q %v", name, got, err)
		}
	}
}

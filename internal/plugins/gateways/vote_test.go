package gateways

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"ouroboros/plugin"
)

func voteOK(context.Context, plugin.GatewayPollVoteRequest) (string, error) { return "voted", nil }

func TestVoteDispatch(t *testing.T) {
	ctx := plugin.WithMessageSource(t.Context(), plugin.MessageSource{Gateway: "unsupported"})
	calls := 0
	refusal := errors.New("provider refusal")
	tools, _ := fixture(t, plugin.GatewayOutput{Name: "unsupported", SendImage: imageOK}, plugin.GatewayOutput{
		Name: "polls", PollDescription: "Public poll restrictions",
		VotePoll: func(got context.Context, req plugin.GatewayPollVoteRequest) (string, error) {
			calls++
			if got != ctx {
				t.Fatal("context replaced")
			}
			want := plugin.GatewayPollVoteRequest{PollID: "opaque poll", OptionIDs: []string{"b", "a"}, Destination: "opaque target"}
			if calls == 2 {
				want = plugin.GatewayPollVoteRequest{PollID: "opaque poll", OptionIDs: []string{}}
			}
			if !reflect.DeepEqual(req, want) {
				t.Fatalf("request %+v, want %+v", req, want)
			}
			return "provider detail", refusal
		},
	})
	for _, raw := range []string{`{"gateway":"polls","destination":"opaque target","poll_id":"opaque poll","option_ids":["b","a"]}`, `{"poll_id":"opaque poll","option_ids":[]}`} {
		got, err := tools[plugin.GatewayVotePollTool].Run(ctx, []byte(raw))
		if got != "provider detail" || !errors.Is(err, refusal) {
			t.Fatalf("%q %v", got, err)
		}
	}
	if calls != 2 {
		t.Fatalf("calls: %d", calls)
	}
	raw, err := tools["gateway_list"].Run(ctx, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Name            string
		VotePoll        bool   `json:"vote_poll"`
		PollDescription string `json:"poll_description"`
	}
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].VotePoll || !entries[1].VotePoll || entries[1].PollDescription != "Public poll restrictions" {
		t.Fatalf("listing: %s", raw)
	}
}

func TestVoteRouting(t *testing.T) {
	one := plugin.GatewayOutput{Name: "one", VotePoll: voteOK}
	two := plugin.GatewayOutput{Name: "two", VotePoll: voteOK}
	unsupported := plugin.GatewayOutput{Name: "image", SendImage: imageOK, SetAvatar: avatarOK}
	for _, tc := range []struct {
		label         string
		providers     []plugin.GatewayOutput
		gateway, want string
	}{
		{"none", nil, "", "no gateway provider"},
		{"no capability", []plugin.GatewayOutput{unsupported}, "", "no gateway provider"},
		{"ambiguous", []plugin.GatewayOutput{one, two}, "", "ambiguous"},
		{"unknown", []plugin.GatewayOutput{one}, "missing", "unknown"},
		{"unsupported", []plugin.GatewayOutput{one, unsupported}, "image", "does not support"},
		{"duplicate", []plugin.GatewayOutput{one, one}, "one", "duplicate"},
		{"empty name", []plugin.GatewayOutput{{}}, "", "empty name"},
		{"explicit", []plugin.GatewayOutput{one, two}, "two", ""},
		{"one capable", []plugin.GatewayOutput{one, unsupported}, "", ""},
	} {
		t.Run(tc.label, func(t *testing.T) {
			tools, _ := fixture(t, tc.providers...)
			raw, _ := json.Marshal(map[string]any{"gateway": tc.gateway, "poll_id": "poll", "option_ids": []string{}})
			ctx := plugin.WithMessageSource(t.Context(), plugin.MessageSource{Gateway: "one"})
			got, err := tools[plugin.GatewayVotePollTool].Run(ctx, raw)
			if tc.want == "" {
				if err != nil || got != "voted" {
					t.Fatalf("%q %v", got, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %s, got %v", tc.want, err)
			}
		})
	}
	tools, pctx := fixture(t)
	plugin.Add(pctx, one)
	if got, err := tools[plugin.GatewayVotePollTool].Run(t.Context(), []byte(`{"poll_id":"poll","option_ids":[]}`)); err != nil || got != "voted" {
		t.Fatalf("late dispatch: %q %v", got, err)
	}
}

func TestVoteValidationAndBounds(t *testing.T) {
	calls := 0
	tools, _ := fixture(t, plugin.GatewayOutput{Name: "polls", VotePoll: func(context.Context, plugin.GatewayPollVoteRequest) (string, error) { calls++; return "ok", nil }})
	tool := tools[plugin.GatewayVotePollTool]
	invalid := []string{
		`{}`, `{"option_ids":[]}`, `{"poll_id":"","option_ids":[]}`, `{"poll_id":"  ","option_ids":[]}`,
		`{"poll_id":"p"}`, `{"poll_id":"p","option_ids":null}`, `{"poll_id":"p","option_ids":"[]"}`,
		`{"poll_id":"p","option_ids":{}}`, `{"poll_id":"p","option_ids":[1]}`, `{"poll_id":"p","option_ids":[null]}`,
		`{"poll_id":"p","option_ids":[""]}`, `{"poll_id":"p","option_ids":[" "]}`, `{"poll_id":"p","option_ids":["a","a"]}`,
		`{"poll_id":"p","option_ids":[],"extra":true}`, `{"poll_id":1,"option_ids":[]}`,
	}
	ids := make([]string, 21)
	for i := range ids {
		ids[i] = fmt.Sprint(i)
	}
	tooMany, _ := json.Marshal(map[string]any{"poll_id": "p", "option_ids": ids})
	invalid = append(invalid, string(tooMany))
	for _, raw := range invalid {
		if _, err := tool.Run(t.Context(), []byte(raw)); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if calls != 0 {
		t.Fatalf("invalid calls: %d", calls)
	}
	boundary, _ := json.Marshal(map[string]any{"poll_id": "p", "option_ids": ids[:20]})
	if _, err := tool.Run(t.Context(), boundary); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("boundary calls: %d", calls)
	}
}

func TestVoteSchema(t *testing.T) {
	tools, _ := fixture(t)
	tool := tools[plugin.GatewayVotePollTool]
	var schema struct {
		Required             []string
		AdditionalProperties bool
		Properties           map[string]struct {
			Type        string
			MinLength   int
			MaxItems    int
			UniqueItems bool
			Items       struct {
				Type      string
				MinLength int
			}
			Description string
		}
	}
	if err := json.Unmarshal([]byte(tool.Schema), &schema); err != nil {
		t.Fatal(err)
	}
	if !tool.OwnerOnly || !reflect.DeepEqual(schema.Required, []string{"poll_id", "option_ids"}) || schema.AdditionalProperties {
		t.Fatalf("schema: %s", tool.Schema)
	}
	options := schema.Properties["option_ids"]
	if len(schema.Properties) != 4 || options.Type != "array" || options.MaxItems != 20 || !options.UniqueItems || options.Items.Type != "string" || options.Items.MinLength != 1 || schema.Properties["poll_id"].MinLength != 1 {
		t.Fatalf("schema: %s", tool.Schema)
	}
	for _, key := range []string{"gateway", "destination", "poll_id"} {
		if schema.Properties[key].Type != "string" {
			t.Fatalf("%s is not a string", key)
		}
	}
}

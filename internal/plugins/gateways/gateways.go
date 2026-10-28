// Package gateways exposes owner-only, backend-neutral gateway operations.
package gateways

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ouroboros/plugin"
)

type imageArgs struct {
	Gateway     string `json:"gateway"`
	Path        string `json:"path"`
	Attachment  int    `json:"attachment"`
	Destination string `json:"destination"`
}

type audioArgs struct {
	Gateway     string `json:"gateway"`
	Path        string `json:"path"`
	Destination string `json:"destination"`
}

type avatarArgs struct {
	Gateway    string `json:"gateway"`
	Path       string `json:"path"`
	Attachment int    `json:"attachment"`
}

type voteArgs struct {
	Gateway     string          `json:"gateway"`
	Destination string          `json:"destination"`
	PollID      string          `json:"poll_id"`
	OptionIDs   json.RawMessage `json:"option_ids"`
}

// Plugin contributes tools, not providers. Providers are discovered on each call,
// after all plugins have started, and are never inferred from conversation source.
func Plugin() plugin.Plugin {
	return plugin.New("gateways", func(pctx *plugin.Context, _ struct{}) error {
		list := plugin.NewTool("gateway_list", "List gateway output providers, their supported capabilities and public descriptions. Does not expose account settings or credentials.", nil,
			func(ctx context.Context, _ struct{}) (string, error) {
				if err := owner(ctx); err != nil {
					return "", err
				}
				providers, err := outputs(pctx)
				if err != nil {
					return "", err
				}
				type entry struct {
					Name              string `json:"name"`
					SendImage         bool   `json:"send_image"`
					SendAudio         bool   `json:"send_audio"`
					SetAvatar         bool   `json:"set_avatar"`
					ImageDescription  string `json:"image_description"`
					AudioDescription  string `json:"audio_description"`
					AvatarDescription string `json:"avatar_description"`
					VotePoll          bool   `json:"vote_poll"`
					PollDescription   string `json:"poll_description"`
				}
				entries := make([]entry, 0, len(providers))
				for _, p := range providers {
					entries = append(entries, entry{
						Name:              p.Name,
						SendImage:         p.SendImage != nil,
						SendAudio:         p.SendAudio != nil,
						SetAvatar:         p.SetAvatar != nil,
						ImageDescription:  p.ImageDescription,
						AudioDescription:  p.AudioDescription,
						AvatarDescription: p.AvatarDescription,
						VotePoll:          p.VotePoll != nil,
						PollDescription:   p.PollDescription,
					})
				}
				data, err := json.Marshal(entries)
				return string(data), err
			})
		list.OwnerOnly = true
		plugin.Add(pctx, list)

		image := plugin.NewTool(plugin.GatewaySendImageTool, "Send an image through a gateway. Supply exactly one local path or positive 1-based image attachment index. Use gateway_list for provider capabilities and destination rules. Omit gateway only when exactly one provider supports sending images.",
			[]plugin.Param{{Name: "gateway", Description: "Exact provider name; optional only with one supporting provider", Optional: true}},
			func(ctx context.Context, args imageArgs) (string, error) {
				if err := owner(ctx); err != nil {
					return "", err
				}
				if err := validateInput(args.Path, args.Attachment, false); err != nil {
					return "", err
				}
				p, err := selectOutput(pctx, args.Gateway, plugin.GatewaySendImageTool)
				if err != nil {
					return "", err
				}
				return p.SendImage(ctx, plugin.GatewayImageRequest{Path: args.Path, Attachment: args.Attachment, Destination: args.Destination})
			})
		image.Schema = `{"type":"object","properties":{"gateway":{"type":"string","description":"Exact provider name; omit only when exactly one provider supports sending images."},"path":{"type":"string","minLength":1,"description":"Local image path; exclusive with attachment. Backend file permissions apply."},"attachment":{"type":"integer","minimum":1,"description":"1-based image index in the most recent user message with images; live conversation only, not after restart; exclusive with path."},"destination":{"type":"string","description":"Optional destination interpreted by the backend; omit only when its target is unambiguous."}},"additionalProperties":false}`
		image.OwnerOnly = true
		plugin.Add(pctx, image)

		audio := plugin.NewTool(plugin.GatewaySendAudioTool, "Send an audio file through a gateway. Use gateway_list for provider capabilities and destination rules. Omit gateway only when exactly one provider supports sending audio.",
			[]plugin.Param{{Name: "gateway", Description: "Exact provider name; optional only with one supporting provider", Optional: true}},
			func(ctx context.Context, args audioArgs) (string, error) {
				if err := owner(ctx); err != nil {
					return "", err
				}
				if args.Path == "" {
					return "", errors.New("give the path of the audio file")
				}
				p, err := selectOutput(pctx, args.Gateway, plugin.GatewaySendAudioTool)
				if err != nil {
					return "", err
				}
				return p.SendAudio(ctx, plugin.GatewayAudioRequest{Path: args.Path, Destination: args.Destination})
			})
		audio.Schema = `{"type":"object","properties":{"gateway":{"type":"string","description":"Exact provider name; omit only when exactly one provider supports sending audio."},"path":{"type":"string","minLength":1,"description":"Local audio path. Backend file permissions apply."},"destination":{"type":"string","description":"Optional destination interpreted by the backend; omit only when its target is unambiguous."}},"required":["path"],"additionalProperties":false}`
		audio.OwnerOnly = true
		plugin.Add(pctx, audio)

		avatar := plugin.NewTool(plugin.GatewaySetAvatarTool, "Change a gateway account's public profile picture only when the owner requests it. This image may become publicly visible. Use gateway_list for provider restrictions. Omit gateway only when exactly one provider supports avatars. Supply a local path or an image attachment; without either, attachment 1 is used.",
			[]plugin.Param{{Name: "gateway", Description: "Exact provider name; optional only with one supporting provider", Optional: true}},
			func(ctx context.Context, args avatarArgs) (string, error) {
				if err := owner(ctx); err != nil {
					return "", err
				}
				if err := validateInput(args.Path, args.Attachment, true); err != nil {
					return "", err
				}
				if args.Path == "" && args.Attachment == 0 {
					args.Attachment = 1
				}
				p, err := selectOutput(pctx, args.Gateway, plugin.GatewaySetAvatarTool)
				if err != nil {
					return "", err
				}
				return p.SetAvatar(ctx, plugin.GatewayAvatarRequest{Path: args.Path, Attachment: args.Attachment})
			})
		avatar.Schema = `{"type":"object","properties":{"gateway":{"type":"string","description":"Exact provider name; omit only when exactly one provider supports avatars."},"path":{"type":"string","minLength":1,"description":"Local image path; exclusive with attachment. Changes a public profile picture."},"attachment":{"type":"integer","minimum":1,"default":1,"description":"1-based image index in the most recent user message with images, default 1 when path is omitted; live conversation only, not after restart; exclusive with path."}},"additionalProperties":false,"not":{"required":["path","attachment"]}}`
		avatar.OwnerOnly = true
		plugin.Add(pctx, avatar)

		vote := plugin.NewTool(plugin.GatewayVotePollTool, "Vote in a gateway poll using opaque poll and option IDs. An empty option_ids array withdraws the vote. Use gateway_list for provider capabilities and destination rules. Omit gateway only when exactly one provider supports voting.", nil,
			func(ctx context.Context, args voteArgs) (string, error) {
				// The wrapping Run below refuses peers before this runs.
				if strings.TrimSpace(args.PollID) == "" {
					return "", errors.New("poll_id must be nonempty")
				}
				var options []string
				if err := json.Unmarshal(args.OptionIDs, &options); err != nil || options == nil {
					return "", errors.New("option_ids must be an array of strings; use [] to withdraw a vote")
				}
				if len(options) > 20 {
					return "", errors.New("option_ids must contain at most 20 IDs")
				}
				seen := make(map[string]bool, len(options))
				for _, id := range options {
					if strings.TrimSpace(id) == "" {
						return "", errors.New("option_ids must not contain empty IDs")
					}
					if seen[id] {
						return "", errors.New("option_ids must not contain duplicate IDs")
					}
					seen[id] = true
				}
				p, err := selectOutput(pctx, args.Gateway, plugin.GatewayVotePollTool)
				if err != nil {
					return "", err
				}
				return p.VotePoll(ctx, plugin.GatewayPollVoteRequest{PollID: args.PollID, OptionIDs: options, Destination: args.Destination})
			})
		vote.Schema = `{"type":"object","properties":{"gateway":{"type":"string","description":"Exact provider name; omit only when exactly one provider supports voting."},"destination":{"type":"string","description":"Optional destination interpreted by the provider; omit only when its target is unambiguous."},"poll_id":{"type":"string","minLength":1,"description":"Nonempty opaque poll ID supplied by the provider."},"option_ids":{"type":"array","items":{"type":"string","minLength":1},"maxItems":20,"uniqueItems":true,"description":"Opaque option IDs to select; an empty array withdraws the vote."}},"required":["poll_id","option_ids"],"additionalProperties":false}`
		// Enforce the closed schema at runtime as well as in the model schema.
		runVote := vote.Run
		vote.Run = func(ctx context.Context, raw json.RawMessage) (string, error) {
			if err := owner(ctx); err != nil {
				return "", err
			}
			var args voteArgs
			if err := plugin.Decode(plugin.GatewayVotePollTool, raw, &args); err != nil {
				return "", err
			}
			return runVote(ctx, raw)
		}
		vote.OwnerOnly = true
		plugin.Add(pctx, vote)
		return nil
	})
}

func owner(ctx context.Context) error {
	if _, peer := plugin.OriginFrom(ctx); peer {
		return errors.New("gateway operations are owner-only")
	}
	return nil
}

func validateInput(path string, attachment int, defaultAttachment bool) error {
	if attachment < 0 {
		return errors.New("attachment must be a positive 1-based image index")
	}
	if path != "" && attachment != 0 {
		return errors.New("choose a path or an attachment, not both")
	}
	if !defaultAttachment && path == "" && attachment == 0 {
		return errors.New("give exactly one path or attachment")
	}
	return nil
}

func outputs(ctx *plugin.Context) ([]plugin.GatewayOutput, error) {
	providers := plugin.All[plugin.GatewayOutput](ctx)
	seen := make(map[string]bool, len(providers))
	for _, p := range providers {
		if strings.TrimSpace(p.Name) == "" {
			return nil, errors.New("gateway provider has an empty name")
		}
		if seen[p.Name] {
			return nil, fmt.Errorf("duplicate gateway provider name %q", p.Name)
		}
		seen[p.Name] = true
	}
	return providers, nil
}

func selectOutput(ctx *plugin.Context, name string, tool string) (plugin.GatewayOutput, error) {
	providers, err := outputs(ctx)
	if err != nil {
		return plugin.GatewayOutput{}, err
	}
	supports := func(p plugin.GatewayOutput) bool {
		switch tool {
		case plugin.GatewaySetAvatarTool:
			return p.SetAvatar != nil
		case plugin.GatewayVotePollTool:
			return p.VotePoll != nil
		case plugin.GatewaySendAudioTool:
			return p.SendAudio != nil
		}
		return p.SendImage != nil
	}
	capability := "sending images"
	switch tool {
	case plugin.GatewaySetAvatarTool:
		capability = "setting avatars"
	case plugin.GatewayVotePollTool:
		capability = "voting in polls"
	case plugin.GatewaySendAudioTool:
		capability = "sending audio"
	}
	var matches []plugin.GatewayOutput
	for _, p := range providers {
		if name != "" && p.Name != name {
			continue
		}
		if supports(p) {
			matches = append(matches, p)
		} else if name != "" {
			return plugin.GatewayOutput{}, fmt.Errorf("gateway %q does not support %s", name, capability)
		}
	}
	if name != "" && len(matches) == 0 {
		return plugin.GatewayOutput{}, fmt.Errorf("unknown gateway provider %q", name)
	}
	if len(matches) == 0 {
		return plugin.GatewayOutput{}, fmt.Errorf("no gateway provider supports %s", capability)
	}
	if len(matches) > 1 {
		return plugin.GatewayOutput{}, fmt.Errorf("ambiguous gateway for %s; specify gateway", capability)
	}
	return matches[0], nil
}

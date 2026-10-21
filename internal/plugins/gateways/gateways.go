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

type avatarArgs struct {
	Gateway    string `json:"gateway"`
	Path       string `json:"path"`
	Attachment int    `json:"attachment"`
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
					SetAvatar         bool   `json:"set_avatar"`
					ImageDescription  string `json:"image_description"`
					AvatarDescription string `json:"avatar_description"`
				}
				entries := make([]entry, 0, len(providers))
				for _, p := range providers {
					entries = append(entries, entry{p.Name, p.SendImage != nil, p.SetAvatar != nil, p.ImageDescription, p.AvatarDescription})
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
				p, err := selectOutput(pctx, args.Gateway, false)
				if err != nil {
					return "", err
				}
				return p.SendImage(ctx, plugin.GatewayImageRequest{Path: args.Path, Attachment: args.Attachment, Destination: args.Destination})
			})
		image.Schema = `{"type":"object","properties":{"gateway":{"type":"string","description":"Exact provider name; omit only when exactly one provider supports sending images."},"path":{"type":"string","minLength":1,"description":"Local image path; exclusive with attachment. Backend file permissions apply."},"attachment":{"type":"integer","minimum":1,"description":"1-based image index in the most recent user message with images; live conversation only, not after restart; exclusive with path."},"destination":{"type":"string","description":"Optional destination interpreted by the backend; omit only when its target is unambiguous."}},"additionalProperties":false,"oneOf":[{"required":["path"],"not":{"required":["attachment"]}},{"required":["attachment"],"not":{"required":["path"]}}]}`
		image.OwnerOnly = true
		plugin.Add(pctx, image)

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
				p, err := selectOutput(pctx, args.Gateway, true)
				if err != nil {
					return "", err
				}
				return p.SetAvatar(ctx, plugin.GatewayAvatarRequest{Path: args.Path, Attachment: args.Attachment})
			})
		avatar.Schema = `{"type":"object","properties":{"gateway":{"type":"string","description":"Exact provider name; omit only when exactly one provider supports avatars."},"path":{"type":"string","minLength":1,"description":"Local image path; exclusive with attachment. Changes a public profile picture."},"attachment":{"type":"integer","minimum":1,"default":1,"description":"1-based image index in the most recent user message with images, default 1 when path is omitted; live conversation only, not after restart; exclusive with path."}},"additionalProperties":false,"not":{"required":["path","attachment"]}}`
		avatar.OwnerOnly = true
		plugin.Add(pctx, avatar)
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

func selectOutput(ctx *plugin.Context, name string, avatar bool) (plugin.GatewayOutput, error) {
	providers, err := outputs(ctx)
	if err != nil {
		return plugin.GatewayOutput{}, err
	}
	supports := func(p plugin.GatewayOutput) bool {
		if avatar {
			return p.SetAvatar != nil
		}
		return p.SendImage != nil
	}
	capability := "sending images"
	if avatar {
		capability = "setting avatars"
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

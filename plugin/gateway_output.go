package plugin

import "context"

// Neutral tool names shared by gateway output providers.
const (
	GatewaySendImageTool = "gateway_send_image"
	GatewaySetAvatarTool = "gateway_set_avatar"
)

// GatewayImageRequest selects exactly one local path or 1-based image attachment.
// Destination is interpreted by the provider; an empty value uses its default.
type GatewayImageRequest struct {
	Path        string `json:"path"`
	Attachment  int    `json:"attachment"`
	Destination string `json:"destination"`
}

// GatewayAvatarRequest selects a local path or a 1-based image attachment.
type GatewayAvatarRequest struct {
	Path       string `json:"path"`
	Attachment int    `json:"attachment"`
}

// GatewayOutput contributes neutral output capabilities with Add. Consumers
// discover these with All at invocation time. Name must be nonempty and unique.
// Nil callbacks denote unsupported capabilities. Descriptions must be safe to
// show to the owner: never include credentials or other secrets.
// Callbacks receive the original tool context and remain responsible for their
// backend's permissions, file access, attachment resolution and destination rules.
type GatewayOutput struct {
	Name              string
	ImageDescription  string
	AvatarDescription string
	SendImage         func(context.Context, GatewayImageRequest) (string, error)
	SetAvatar         func(context.Context, GatewayAvatarRequest) (string, error)
}

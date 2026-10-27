package plugin

import "context"

// Neutral tool names shared by gateway output providers.
const (
	GatewaySendImageTool = "gateway_send_image"
	GatewaySendFileTool  = "gateway_send_file"
	GatewaySetAvatarTool = "gateway_set_avatar"
	GatewayVotePollTool  = "gateway_vote_poll"
)

// GatewayImageRequest selects exactly one local path or 1-based image attachment.
// Destination is interpreted by the provider; an empty value uses its default.
type GatewayImageRequest struct {
	Path        string `json:"path"`
	Attachment  int    `json:"attachment"`
	Destination string `json:"destination"`
}

// GatewayFileRequest selects one local file. Destination is interpreted by the
// provider; an empty value uses its default.
type GatewayFileRequest struct {
	Path        string `json:"path"`
	Destination string `json:"destination"`
}

// GatewayAvatarRequest selects a local path or a 1-based image attachment.
type GatewayAvatarRequest struct {
	Path       string `json:"path"`
	Attachment int    `json:"attachment"`
}

// GatewayPollVoteRequest selects poll options by their opaque provider IDs.
// An empty OptionIDs slice withdraws the vote. Destination is provider-specific.
type GatewayPollVoteRequest struct {
	PollID      string   `json:"poll_id"`
	OptionIDs   []string `json:"option_ids"`
	Destination string   `json:"destination"`
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
	FileDescription   string
	AvatarDescription string
	PollDescription   string
	SendImage         func(context.Context, GatewayImageRequest) (string, error)
	SendFile          func(context.Context, GatewayFileRequest) (string, error)
	SetAvatar         func(context.Context, GatewayAvatarRequest) (string, error)
	VotePoll          func(context.Context, GatewayPollVoteRequest) (string, error)
}

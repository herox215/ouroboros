package plugin

import "context"

// Neutral tool names shared by gateway output providers.
const (
	GatewaySendImageTool = "gateway_send_image"
	GatewaySendAudioTool = "gateway_send_audio"
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

// GatewayAudioRequest selects one local audio file. Destination is interpreted by the
// provider; an empty value uses its default.
type GatewayAudioRequest struct {
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
	AudioDescription  string
	AvatarDescription string
	PollDescription   string
	// Available says whether the provider can be used right now, nil for one
	// that always can: a window of ouro shows a picture only while it is open,
	// and the tool leaves out what is not there, so that a gateway that is
	// there stays the only one it can mean. It is read when the tool is
	// called, never kept.
	Available func() bool
	SendImage func(context.Context, GatewayImageRequest) (string, error)
	SendAudio func(context.Context, GatewayAudioRequest) (string, error)
	SetAvatar func(context.Context, GatewayAvatarRequest) (string, error)
	VotePoll  func(context.Context, GatewayPollVoteRequest) (string, error)
}

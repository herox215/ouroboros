package session

import (
	"context"

	"ouroboros/internal/node"
)

// Instance names the image of ouro the node runs: ID changes each time the
// node starts anew, from a new build or not, and Executable is the binary it
// runs. A window started from an older build compares it with the one it saw
// before, to go on in the build that runs, see the gui plugin.
type Instance struct {
	ID         string `json:"id"`
	Executable string `json:"executable"`
}

// executable finds the binary the node runs; tests replace it.
var executable = node.Executable

// Instance tells which image of ouro holds the sessions.
func (s *Service) Instance(ctx context.Context) (Instance, error) {
	if err := ctx.Err(); err != nil {
		return Instance{}, err
	}
	exe, err := executable()
	if err != nil {
		return Instance{}, err
	}
	return Instance{ID: processInstance, Executable: exe}, nil
}

// Instance tells which image of ouro the node that runs is, without starting
// it: ErrNotRunning while none runs.
func (c *Client) Instance(ctx context.Context) (Instance, error) {
	var out Instance
	err := c.do(ctx, "GET", "/instance", nil, &out)
	return out, err
}

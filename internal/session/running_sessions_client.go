package session

import "context"

// RunningSessions reads live owner-session titles without starting the node.
func (c *Client) RunningSessions(ctx context.Context) ([]string, error) {
	var out []string
	err := c.do(ctx, "GET", "/running-sessions", nil, &out)
	return out, err
}

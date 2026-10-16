package session

import "context"

// Activity is content-free activity of owner sessions in one profile. Peer
// requests and persistent background services are deliberately not included.
type Activity struct {
	Running int `json:"running"` // sessions with an answer or delegated work
	Waiting int `json:"waiting"` // sessions awaiting an owner response
}

// ActivitySource is an optional capability, not a requirement on every API.
type ActivitySource interface {
	Activity(context.Context) (Activity, error)
}

// Activity samples live state without loading archives or retaining the node.
func (s *Service) Activity(ctx context.Context) (Activity, error) {
	if err := ctx.Err(); err != nil {
		return Activity{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var out Activity
	for _, se := range s.sessions {
		if se.cancel != nil || len(se.tasks) > 0 {
			out.Running++
		}
		if se.asked != nil {
			out.Waiting++
		}
	}
	return out, nil
}

// Activity reads the profile activity without starting or keeping the node.
func (c *Client) Activity(ctx context.Context) (Activity, error) {
	var out Activity
	err := c.do(ctx, "GET", "/activity", nil, &out)
	return out, err
}

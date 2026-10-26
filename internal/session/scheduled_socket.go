package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
)

type scheduledRequest struct {
	Name      string `json:"name"`
	Text      string `json:"text"`
	Directory string `json:"directory"`
	Ask       bool   `json:"ask"`
}

type scheduledResponse struct {
	Text     string `json:"text"`
	Error    string `json:"error,omitempty"`
	Canceled bool   `json:"canceled,omitempty"`
	Deadline bool   `json:"deadline,omitempty"`
}

func (s *Service) serveScheduled(w http.ResponseWriter, r *http.Request) {
	var in scheduledRequest
	if !read(w, r, &in) {
		return
	}
	id, result, err := s.RunScheduled(r.Context(), in.Name, in.Text, in.Directory, in.Ask)
	if err != nil {
		reply(w, nil, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	enc := json.NewEncoder(w)
	if err := enc.Encode(newResponse{ID: id}); err != nil {
		return
	}
	http.NewResponseController(w).Flush()
	select {
	case out := <-result:
		wire := scheduledResponse{Text: out.Text, Canceled: errors.Is(out.Err, context.Canceled), Deadline: errors.Is(out.Err, context.DeadlineExceeded)}
		if out.Err != nil {
			wire.Error = out.Err.Error()
		}
		enc.Encode(wire)
	case <-r.Context().Done():
	}
}

// RunScheduled starts scheduled work and keeps the request open until its
// terminal result. Canceling ctx cancels the server-side answer as well.
func (c *Client) RunScheduled(ctx context.Context, name, text, directory string, ask bool) (string, <-chan ScheduledResult, error) {
	resp, err := c.request(ctx, http.MethodPost, "/run-scheduled", scheduledRequest{Name: name, Text: text, Directory: directory, Ask: ask})
	if err != nil {
		return "", nil, err
	}
	dec := json.NewDecoder(resp.Body)
	var first newResponse
	if err := dec.Decode(&first); err != nil {
		resp.Body.Close()
		return "", nil, err
	}
	result := make(chan ScheduledResult, 1)
	go func() {
		defer resp.Body.Close()
		defer close(result)
		var wire scheduledResponse
		err := dec.Decode(&wire)
		switch {
		case ctx.Err() != nil:
			err = ctx.Err()
		case err != nil:
		case wire.Canceled:
			err = context.Canceled
		case wire.Deadline:
			err = context.DeadlineExceeded
		case wire.Error != "":
			err = errors.New(wire.Error)
		}
		result <- ScheduledResult{Text: wire.Text, Err: err}
	}()
	return first.ID, result, nil
}

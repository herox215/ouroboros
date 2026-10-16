package session

import (
	"context"
	"errors"
	"strings"

	"ouroboros/internal/history"
)

// ToolDetails is an optional extension of API for front ends that show tool
// arguments and output on demand. Watch and Read never include tool output.
type ToolDetails interface {
	ToolResult(ctx context.Context, id, callID string) (ToolResult, error)
}

// ToolResult is the original argument text and, when Found, the tool output.
// A call without an archived result has Found false, even with arguments.
type ToolResult struct {
	Arguments string `json:"arguments"`
	Content   string `json:"content"`
	Found     bool   `json:"found"`
}

// ToolResult looks up an owner session (unqualified) or an archive-qualified
// peer conversation without loading or creating a session. Nested script
// calls have no separate archive record and return an explicit error.
func (s *Service) ToolResult(ctx context.Context, id, callID string) (ToolResult, error) {
	if err := ctx.Err(); err != nil {
		return ToolResult{}, err
	}
	if !strings.Contains(id, "/") {
		if err := valid(id); err != nil {
			return ToolResult{}, err
		}
		id = history.Owner(id)
	} else if !peerID(id) {
		return ToolResult{}, ErrNoSession
	}
	s.mu.Lock()
	a := s.archive
	s.mu.Unlock()
	if a == nil {
		return ToolResult{}, ErrNoSession
	}
	call, content, found, err := a.ToolResult(id, callID)
	if errors.Is(err, history.ErrNotArchived) {
		return ToolResult{}, ErrNoSession
	}
	if err != nil {
		return ToolResult{}, err
	}
	return ToolResult{Arguments: call.Arguments, Content: content, Found: found}, nil
}

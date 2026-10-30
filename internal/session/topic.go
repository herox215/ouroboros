package session

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"ouroboros/internal/assistant"
	"ouroboros/internal/chat"
	"ouroboros/internal/config"
	"ouroboros/internal/history"
)

// concern returns the immutable original request, never a changing title or
// summary. It is already durable in the conversation archive, so compaction of
// the agent's working context cannot change it. s.mu is held.
func (s *Service) concern(id string) string {
	if se := s.sessions[id]; se != nil {
		return concernBound(firstMessage(se.events), 4000)
	}
	if anchor, ok := s.concernAnchors[id]; ok {
		return anchor
	}
	if s.archive == nil {
		return ""
	}
	// Only the start is needed; do not resume every archived session just to
	// give the router its roster. A missing anchor is not inferred from a title.
	page, err := s.archive.Page(history.Owner(id), 0)
	if err != nil {
		s.archive.log("the original concern of session %s cannot be read: %v", id, err)
		return ""
	}
	events, _ := replay(page.Records)
	anchor := concernBound(firstMessage(events), 4000)
	if anchor != "" {
		if s.concernAnchors == nil {
			s.concernAnchors = make(map[string]string)
		}
		s.concernAnchors[id] = anchor
	}
	return anchor
}

func concernBound(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return strings.ToValidUTF8(text[:limit-len("…")], "") + "…"
}

// concernEvidence keeps only the latest conversational statements. Tool
// payloads are neither needed nor trustworthy evidence of the owner's intent.
func concernEvidence(events []Event) []chat.Message {
	var evidence []chat.Message
	for i := len(events) - 1; i >= 0 && len(evidence) < 6; i-- {
		e := events[i]
		role, text := "", e.Text
		switch e.Kind {
		case Message, Queued:
			role = "user"
		case Answer, Say:
			role = "assistant"
		case Asked:
			if e.Done {
				role = "user"
			} else {
				role, text = "assistant", e.AsQuestion().Question
			}
		}
		if role != "" && strings.TrimSpace(text) != "" {
			evidence = append(evidence, chat.Message{Role: role, Content: concernBound(text, 2000)})
		}
	}
	for i, j := 0, len(evidence)-1; i < j; i, j = i+1, j-1 {
		evidence[i], evidence[j] = evidence[j], evidence[i]
	}
	return evidence
}

// checkedConcern records the target snapshot a decision is about. Verification
// does network I/O without s.mu held. Delivery must reject a stale decision if
// work or a direct owner message changed the target in the meantime.
type checkedConcern struct {
	target         *session
	events, queued int
	asked          chan queued
	thread         *session
	threadRevision int
}

func (t threading) checkConcern(ctx context.Context, id, text string, pictures []chat.Image, pendingReply bool) (checkedConcern, assistant.TopicDecision, error) {
	if err := ctx.Err(); err != nil {
		return checkedConcern{}, assistant.TopicDecision{}, err
	}
	s := t.s
	s.mu.Lock()
	allowed := false
	if pendingReply {
		thread := s.sessions[ThreadID]
		allowed = thread != nil && len(thread.questions) > 0 && thread.questions[0].se.id == id && thread.questions[0].se.asked != nil
	}
	if !pendingReply {
		for _, r := range s.routable() {
			if r.id == id {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		s.mu.Unlock()
		return checkedConcern{}, assistant.TopicDecision{}, fmt.Errorf("no session %q among those of the last %s: hand the message to one of the list, or start a new one with %s", id, config.Span(s.window()), assistant.ToolSessionNew)
	}
	se := s.get(id)
	snapshot := checkedConcern{target: se, events: concernRevision(se.events), queued: len(se.queue), asked: se.asked}
	check := assistant.TopicCheck{Anchor: s.concern(id), Recent: concernEvidence(se.events), Message: text, AnchorImages: slices.Clone(se.concernPictures), Images: slices.Clone(pictures)}
	if se.asked != nil {
		check.Pending = awaited(se.events)
	}
	if thread := s.sessions[ThreadID]; thread != nil {
		check.Context = concernEvidence(thread.events)
		snapshot.thread, snapshot.threadRevision = thread, concernRevision(thread.events)
	}
	s.mu.Unlock()
	if strings.TrimSpace(check.Anchor) == "" && len(check.AnchorImages) == 0 {
		return snapshot, assistant.TopicDecision{}, fmt.Errorf("CheckTopic: no recoverable original concern for %q (original images may no longer be available); ask the owner to describe the concern before continuing", id)
	}
	decision, err := s.agent.CheckTopic(ctx, check)
	if err != nil {
		return snapshot, decision, fmt.Errorf("CheckTopic could not verify delivery; no message sent: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return snapshot, decision, err
	}
	if strings.TrimSpace(decision.Reason) == "" {
		return snapshot, decision, fmt.Errorf("CheckTopic: invalid decision; no message sent")
	}
	switch decision.Disposition {
	case assistant.TopicContinue:
		return snapshot, decision, nil
	case assistant.TopicNew:
		return snapshot, decision, fmt.Errorf("CheckTopic: new concern (%s); no message sent. Choose an evidenced earlier concern or start a new one with %s", decision.Reason, assistant.ToolSessionNew)
	case assistant.TopicUnclear:
		return snapshot, decision, fmt.Errorf("CheckTopic: unclear concern (%s); no message sent. Resolve the reference or ask the owner a short clarification", decision.Reason)
	default:
		return snapshot, decision, fmt.Errorf("CheckTopic: invalid decision; no message sent")
	}
}

// replyToConcern checks the fast path for a question relayed into the shared
// conversation. A topic change must reach the router instead of consuming an
// unrelated pending question. Rejected or unavailable checks leave that wait
// intact and let the thread resolve the new input normally.
func (s *Service) replyToConcern(ctx context.Context, message queued) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	thread := s.sessions[ThreadID]
	if thread == nil || len(thread.questions) == 0 || thread.questions[0].se.asked == nil {
		s.mu.Unlock()
		return false, nil
	}
	question := thread.questions[0]
	if question.se.backendQuestion {
		// Backend approvals have no topical agent context to contaminate. Their
		// caller validates the exact reply; never let a topic model block them.
		answered := s.answerRelayedImages(message)
		s.mu.Unlock()
		return answered, nil
	}
	id := question.se.id
	s.mu.Unlock()
	checked, decision, err := (threading{s}).checkConcern(ctx, id, message.text, message.images, true)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	thread = s.sessions[ThreadID]
	if thread == nil || len(thread.questions) == 0 || thread.questions[0] != question || !checked.unchanged(s, id) {
		return false, nil
	}
	if s.archive != nil {
		s.archive.log("CheckTopic: reply continues session %s: %s", id, decision.Reason)
	}
	return s.answerRelayedImages(message), nil
}

// concernRevision ignores streaming tokens and tool progress: these must not
// prevent a checked follow-up from steering ongoing work.
func concernRevision(events []Event) int {
	var revision int
	for _, e := range events {
		switch e.Kind {
		case Message, Queued, Answer, Say, Asked:
			revision++
		}
	}
	return revision
}

// changed reports which evidence invalidated a decision, with target changes
// taking precedence. s.mu is held immediately before delivery.
func (c checkedConcern) changed(s *Service, id string) string {
	if s.sessions[id] != c.target || concernRevision(c.target.events) != c.events ||
		len(c.target.queue) != c.queued || c.target.asked != c.asked {
		return "target"
	}
	thread := s.sessions[ThreadID]
	if thread != c.thread || (thread != nil && concernRevision(thread.events) != c.threadRevision) {
		return "thread context"
	}
	return ""
}

// unchanged is called with s.mu held immediately before delivery.
func (c checkedConcern) unchanged(s *Service, id string) bool {
	return c.changed(s, id) == ""
}

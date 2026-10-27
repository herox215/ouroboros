package session

import (
	"context"
	"errors"
	"sync"

	"ouroboros/internal/chat"
	"ouroboros/internal/history"
)

// Peer runs answer, an answer of the agent to a peer, as an answer of the
// node: the node is neither stopped nor replaced while it is on its way, and
// a tool of it may ask for a new build of ouro, like one of a peer the owner
// listed rebuild for. The node takes it once no answer is on its way, and
// the peer goes on with its conversation there, see Recall. The jobs of the
// answer, in the conversation contextID of the peer of ctx, are among Jobs.
func (s *Service) Peer(ctx context.Context, contextID string, answer func(context.Context) (string, error)) (string, error) {
	s.mu.Lock()
	p := s.node
	p.mu.Lock()
	closed, replaceable := s.closed, p.replace != nil
	if !closed {
		p.answering++
	}
	p.mu.Unlock()
	s.mu.Unlock()
	if closed {
		return "", ErrStopped
	}
	var mu sync.Mutex
	var successor string
	if replaceable {
		ctx = WithRestart(ctx, func(exe string) {
			mu.Lock()
			defer mu.Unlock()
			successor = exe
		})
	}
	ctx, forget := s.peerJobs(ctx, contextID)
	reply, err := answer(ctx)
	forget()
	s.mu.Lock()
	defer s.mu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.answering--
	mu.Lock()
	if successor != "" && p.successor == "" { // the first build asked for is the one
		p.successor = successor
	}
	mu.Unlock()
	p.settle()
	return reply, err
}

// Recall returns what the agent said with the peer whose public key is
// publicKey in its conversation id, as the archive kept it: what the agent
// remembers of it, cut as any conversation is. Nil for one that is not
// there.
func (s *Service) Recall(publicKey, id string) []chat.Message {
	s.mu.Lock()
	a := s.archive
	s.mu.Unlock()
	if a == nil {
		return nil
	}
	records, err := a.Records(history.Party(publicKey) + "/" + history.Name(id))
	if err != nil {
		if !errors.Is(err, history.ErrNotArchived) {
			a.log("the conversation %s of a peer cannot be read from the archive: %v", id, err)
		}
		return nil
	}
	_, msgs := replay(records)
	return msgs
}

package peers

import (
	"context"
	"errors"
	"slices"
	"testing"

	"ouroboros/internal/a2a"
)

// An agent asks a peer, answers what its agent asks back, and tells it
// something that starts nothing. The peer that is answered is sent no
// request.
func TestAskAnswerAndTell(t *testing.T) {
	alpha, beta := serve(t, "alpha"), serve(t, "beta")
	trust(alpha, beta)
	gaveUp := make(chan error, 1)
	beta.respond = func(ctx context.Context, text string) (string, error) {
		got, err := a2a.AskBack(ctx, "which "+text+"?")
		if err != nil {
			gaveUp <- err
		}
		return "beta took " + got, err
	}
	network := alpha.network(beta.Node)
	ctx := context.Background()
	asked, err := network.Ask(ctx, beta.Node, "c", "tea")
	if err != nil || asked.Text != "which tea?" || asked.Question == "" {
		t.Fatalf("asked back: %+v, %v", asked, err)
	}
	got, err := network.Answer(ctx, beta.Node, "c", asked.Question, "green")
	if err != nil || got != (a2a.Reply{Text: "beta took green"}) || beta.asked != 1 {
		t.Fatalf("answered: %+v, %v, %d runs", got, err, beta.asked)
	}
	if err := network.Tell(ctx, beta.Node, "c", "thanks"); err != nil || !slices.Equal(beta.notes, []string{"c: thanks"}) || beta.asked != 1 {
		t.Fatalf("told: %v, notes %q, %d runs", err, beta.notes, beta.asked)
	}
	// A question that is given up stops the agent that asked it.
	asked, _ = network.Ask(ctx, beta.Node, "c", "coffee")
	if err := network.GiveUp(ctx, beta.Node, "c", asked.Question); err != nil || !errors.Is(<-gaveUp, a2a.ErrGaveUp) {
		t.Fatalf("given up: %v", err)
	}
	// Who cannot answer a question gets it as what the agent said.
	if got, err := network.Send(ctx, beta.Node, "c2", "milk"); err != nil || got[:11] != "which milk?" || !errors.Is(<-gaveUp, a2a.ErrGaveUp) {
		t.Fatalf("sent: %q, %v", got, err)
	}
	answering := a2a.WithOrigin(ctx, a2a.Origin{PublicKey: beta.PublicKey})
	if _, err := network.Ask(answering, beta.Node, "c", "tea"); !errors.Is(err, ErrSender) {
		t.Fatalf("a request to the peer that is answered: %v", err)
	}
}

// Answers and notes go to connected peers only: a stranger gets nothing, and
// is not asked to connect.
func TestAnswerAndTellGoOnlyToConnectedPeers(t *testing.T) {
	alpha, stranger := serve(t, "alpha"), serve(t, "stranger")
	network := alpha.network(stranger.Node)
	ctx := context.Background()
	if _, err := network.Answer(ctx, stranger.Node, "c", "q", "a"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("answer: %v", err)
	}
	if err := network.Tell(ctx, stranger.Node, "c", "hi"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("tell: %v", err)
	}
	if err := network.GiveUp(ctx, stranger.Node, "c", "q"); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("give up: %v", err)
	}
	if stranger.hits != 0 || len(proposals(t, alpha)) != 0 {
		t.Fatalf("the stranger was reached %d times, proposed %+v", stranger.hits, proposals(t, alpha))
	}
}

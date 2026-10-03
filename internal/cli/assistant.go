package cli

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/contacts"
	"ouroboros/internal/openai"
	"ouroboros/internal/tui"
)

const (
	toolListPeers = "list_peers"
	toolAskPeer   = "ask_peer"
)

const assistantPrompt = `You are %q, the personal agent of the owner of this Ouroboros node. ` +
	`The agents of other people (peers) are reachable on the network: ` + toolListPeers + ` shows who is there, ` +
	toolAskPeer + ` sends one of them a message and returns its answer. ` +
	`Ask a peer when the user wants you to or when a peer is likely to know what you do not. ` +
	`A peer only sees the message you send, so include all the context it needs. ` +
	`The user sees neither your message to a peer nor its answer: tell them briefly and in your own words ` +
	`what the peer said, like "Luna says she is fine", instead of quoting it at length. ` +
	`Answer in the language of the user. Be helpful and concise.`

// assistant returns the agent the owner of this node chats with. Unlike the
// agent that answers peers it can write to other nodes. It is kept between
// calls so that it remembers its conversations, and rebuilt when the node is
// configured anew.
func (a *App) assistant() (*agent.Agent, error) {
	cfg, err := config.Load(a.settings.Home)
	if err != nil {
		return nil, err
	}
	if !cfg.Complete() {
		return nil, errors.New("node is not configured, run: ouro start")
	}
	key := [4]string{cfg.BaseURL, cfg.APIKey, cfg.Model, cfg.Name}
	a.botMu.Lock()
	defer a.botMu.Unlock()
	if a.bot == nil || a.botKey != key {
		a.bot = agent.New(openai.New(cfg.BaseURL, cfg.APIKey), cfg.Model, fmt.Sprintf(assistantPrompt, cfg.Name), a.peerTools()...)
		a.botKey = key
	}
	return a.bot, nil
}

func (a *App) peerTools() []agent.Tool {
	return []agent.Tool{{
		Name:        toolListPeers,
		Description: "List the peers that are online, with their name, ID, model and whether they are connected to this node.",
		Schema:      `{"type":"object","properties":{}}`,
		Run:         a.listPeers,
	}, {
		Name: toolAskPeer,
		Description: "Send a message to the agent of a peer and return its answer. " +
			"A peer that is not connected yet gets a connection request instead, which a human there has to accept.",
		Schema: `{"type":"object","properties":{` +
			`"peer":{"type":"string","description":"name or ID of the peer, as listed by ` + toolListPeers + `"},` +
			`"message":{"type":"string","description":"what to tell or ask the peer"}},` +
			`"required":["peer","message"]}`,
		Run: a.askPeer,
	}}
}

func (a *App) listPeers(context.Context, json.RawMessage) (string, error) {
	cfg, err := config.Load(a.settings.Home)
	if err != nil {
		return "", err
	}
	nodes, err := a.discover()
	if err != nil {
		return "", err
	}
	known, err := a.contacts().Snapshot()
	if err != nil {
		return "", err
	}
	var lines []string
	for _, n := range nodes {
		if n.ID == cfg.ID {
			continue
		}
		state := "not connected"
		switch {
		case slices.ContainsFunc(known.Trusted, func(c contacts.Contact) bool { return c.PublicKey == n.PublicKey }):
			state = "connected"
		case slices.ContainsFunc(known.Outgoing, func(r contacts.Request) bool { return r.PublicKey == n.PublicKey }):
			state = "connection requested"
		}
		lines = append(lines, fmt.Sprintf("%s (id %s, model %s, %s)", n.Name, n.ID, n.Model, state))
	}
	if len(lines) == 0 {
		return "No peers are online.", nil
	}
	return strings.Join(lines, "\n"), nil
}

type askPeerArgs struct {
	Peer    string `json:"peer"`
	Message string `json:"message"`
}

func (a *App) askPeer(ctx context.Context, raw json.RawMessage) (string, error) {
	var args askPeerArgs
	if json.Unmarshal(raw, &args) != nil || args.Peer == "" || args.Message == "" {
		return "", errors.New(toolAskPeer + ` needs "peer" and "message"`)
	}
	cfg, err := config.Load(a.settings.Home)
	if err != nil {
		return "", err
	}
	node, err := a.resolve(args.Peer)
	if err != nil {
		return "", err
	}
	if node.ID != "" && node.ID == cfg.ID {
		return "", fmt.Errorf("%s is this node itself, not a peer", args.Peer)
	}
	client, err := a.client(cfg)
	if err != nil {
		return "", err
	}
	// One context per peer and conversation: the peer remembers what was said before.
	contextID := agent.ContextID(ctx) + "/" + cmp.Or(node.ID, node.Addr())
	name := cmp.Or(node.Name, node.Addr())
	reply, err := client.Send(ctx, "http://"+node.Addr()+"/", contextID, args.Message)
	switch {
	case a2a.IsPending(err):
		a.requestConnection(node)
		return fmt.Sprintf("%s is not connected to this node yet. A connection request was sent; "+
			"a human there has to accept it before %s answers.", name, name), nil
	case a2a.IsBlocked(err):
		return "", fmt.Errorf("%s refused to connect", name)
	}
	return reply, err
}

// peerStep translates a tool call of the assistant into what its owner is
// shown of it: which peer the assistant is waiting for. What the two agents
// say to each other stays between them.
func peerStep(s agent.Step) (tui.Step, bool) {
	var args askPeerArgs
	if s.Tool != toolAskPeer || json.Unmarshal(s.Args, &args) != nil || args.Peer == "" {
		return tui.Step{}, false
	}
	return tui.Step{Peer: args.Peer, Done: s.Done}, true
}

// chat talks to the assistant: once with the given message, otherwise in the
// terminal UI or, without a terminal, line by line.
func (a *App) chat(ctx context.Context, args []string) error {
	if len(args) == 0 && a.Interactive {
		return a.tui(ctx)
	}
	bot, err := a.assistant()
	if err != nil {
		return err
	}
	contextID := a2a.NewID()
	if len(args) > 0 {
		reply, err := bot.Respond(ctx, contextID, strings.Join(args, " "))
		if err != nil {
			return err
		}
		fmt.Fprintln(a.out, reply)
		return nil
	}

	fmt.Fprintln(a.out, "chatting with your agent (Ctrl-D to quit)")
	for {
		fmt.Fprint(a.out, "› ")
		line, err := a.readLine()
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(a.out)
			return nil
		}
		if err != nil {
			return err
		}
		if line == "" {
			continue
		}
		reply, err := bot.Respond(ctx, contextID, line)
		if err != nil {
			reply = "✗ " + err.Error()
		}
		fmt.Fprintf(a.out, "%s\n\n", reply)
	}
}

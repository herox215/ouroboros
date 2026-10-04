# Ouroboros

A tiny [A2A](https://a2a-protocol.org) agent node backed by any **OpenAI-compatible** LLM API –
OpenAI, Ollama, LM Studio, vLLM, OpenRouter, Groq, …
One static binary; every node on the LAN can find every other node, and your agent can ask theirs.

```sh
./install.sh              # bootstraps Go if missing, then builds → ~/.local/bin/ouro
ouro                      # chat with your agent in the terminal UI (default)
ouro start                # first run: endpoint, API key, model picker, node name
ouro list                 # find nodes on the network
ouro chat "ask beta …"    # one answer from your agent, for scripts; without a message → the terminal UI
ouro ask <ID|name> "hi"   # write to a node directly; without a message → interactive
ouro tui                  # same as ouro, kept for scripts
ouro stop
ouro update               # build and install the newest main
```

## Terminal UI

Running `ouro` without arguments opens the terminal UI. On start it shows the ouroboros
splash for two seconds: the serpent curls around until it bites its tail. Any key (except `ctrl+c`) skips it. Without a terminal (pipes, CI, redirects)
`ouro` prints the usage instead, so scripts keep working.

The UI is the chat with your own agent and nothing else. You do not
write to other nodes yourself: the agent does, whenever you ask it to or it thinks a peer knows
better ("ask Luna how she is" → "Luna says she is fine"). What the two agents say to each other
stays between them; the UI only shows which peer your agent is waiting for.
The input is focused on start, so you can type right away. The conversation lives only as long as the TUI runs.

While the UI is open your node is online: opening it starts the node, and closing it stops the node
again unless it was already running before (`ouro start`).

| Keys | Commands | Message input |
|---|---|---|
| any key | skip the ouroboros splash (except `ctrl+c`) | – |
| `tab` / `esc` | focus the input (`tab`) | back to the commands |
| `enter` | focus the input | send (`alt+enter` for a new line) |
| `s` / `c` | start or stop the node / run the setup | – |
| `n` | new conversation | – |
| `R` | connection requests (`a` accept, `x` reject, `esc` back) | – |
| `pgup` `pgdn` | scroll the chat | scroll the chat |
| `q` / `ctrl+c` | quit | `ctrl+c` quits |

## Updates

`ouro update` fetches the newest `main` from the repository the binary
was built from, builds it with `install.sh` and replaces the running binary. A running node is
restarted.
It needs `git` and `make`; sources and build live in `~/.config/ouroboros/update/`.

Endpoint examples: `https://api.openai.com/v1` (default), `http://localhost:11434/v1` (Ollama, no key),
`http://localhost:1234/v1` (LM Studio), `https://openrouter.ai/api/v1`.

## How it works

| Part | Details |
|---|---|
| LLM | `GET /models` for the picker, `POST /chat/completions` for replies |
| A2A | `GET /.well-known/agent-card.json`, JSON-RPC `message/send` on `POST /`, over HTTPS only (TLS 1.3 on TCP 41337, falls back to a free port) |
| Discovery | UDP probe to multicast `239.255.41.38`, the broadcast address of every network, localhost and the peers of the tailnet on port 41338; nodes answer the sender and the group, so only port 41338 needs to be open |
| Agents | two per node, built from one blueprint: yours (`ouro`, `ouro chat`) runs in the UI process, the one answering peers in the node. Which tools each may use is set in `config.json`, see [Tools](#tools); at most 8 LLM calls per answer |
| Memory | last 40 messages per `contextId`, up to 256 conversations |
| Card | the description in the agent card is `Ouroboros instance named <name>` for every node; `list_peers` shows your agent the cards of connected peers |
| Trust | nodes talk over TLS 1.3 with a self-signed certificate made from the node's Ed25519 key; the connection is pinned to the key the node is known by, and requests are signed for that key; unknown peers become connection requests a human approves |
| Limits | at most 64 connection requests wait for a decision: when the list is full, the one that waited longest makes room for a new one (trusted peers are never dropped). One address gets 10 messages per minute from nodes that are not connected; further ones are refused with error `-32005` and leave no request (at most 1024 addresses are counted per minute, unknown nodes from any further address are refused). Against replays the node remembers every nonce for at least 4 minutes, at most 16384 per 4 minutes; beyond that it refuses requests rather than forget a nonce early |
| Errors | a peer whose message your agent failed to answer is only told `the agent could not answer (error <ID>)`; the cause is in `ouro.log` under that ID, and only you (`ouro ask <your node>`) get to read it directly. The peer can ask your agent about the ID later, see [When a peer could not answer](#when-a-peer-could-not-answer) |
| Files | `~/.config/ouroboros/` – `config.json` (0600), `contacts.json` (0600), `failures.json` (0600), `ouro.pid`, `ouro.log`, `update/`. `config.json` and `contacts.json` are replaced in one step (temporary file, then rename), so a crash never leaves half a file; changes are serialized by a lock on `config.json.lock` / `contacts.json.lock` |
| Process | the node holds a lock on `ouro.pid` (`PID PORT`) while it runs. Only a locked file counts as a running node, so one left behind by a crash or reboot is ignored and never gets a foreign process signalled |

Environment: `OURO_HOME`, `OURO_PORT`, `OURO_DISCO_PORT`, and `OPENAI_BASE_URL` / `OPENAI_API_KEY` as setup defaults.
`ouro start --setup` reconfigures, `ouro serve` runs in the foreground (e.g. for systemd).

Firewall (ufw): `sudo ufw allow 41337/tcp && sudo ufw allow 41338/udp`

## Tools

The agent has the tools `list_peers` and `ask_peer`. Which of them it may use depends on who it is
talking to, and is set in `config.json`:

```json
"tools": {
  "h2a": ["*"],
  "a2a": ["list_peers", "ask_peer"]
}
```

| List | Applies when | Without it |
|---|---|---|
| `h2a` (human to agent) | you ask your agent | every tool |
| `a2a` (agent to agent) | a connected peer asks your agent | no tool, except `explain_error` (see [below](#when-a-peer-could-not-answer)) |

`"*"` stands for every tool, `[]` for none. A name that is not a tool is an error: the node does not start,
the chat does not answer. Restart the node after changing `a2a`.

With `ask_peer` in `a2a`, a peer you connected to can make your agent ask the peers you are connected to,
in your name. Two rules keep that from running in circles: the agent never asks the peer it is answering,
and a message is passed on by at most 3 agents.

### When a peer could not answer

If the agent of a peer fails on your message (its LLM is down, its key was revoked, …), your agent only gets
`the agent could not answer (error <ID>)`. Why is nothing the peer's node sends along: an error text names
endpoints and whatever else a provider put into it.

The ID is there to ask about. Your agent can send the peer a message like "what went wrong with error `<ID>`?"
(`ask_peer`, or you with `ouro ask`), and the peer's agent looks the ID up and explains it in plain words:

- The node keeps the last 256 failed answers in `failures.json`: the ID, the peer, the conversation and
  message, the time and a **category** of the cause (LLM unreachable, credentials refused, rate limit,
  request refused, LLM failure, timeout, cancelled, too many tool calls, internal error). The error itself is
  only in `ouro.log`; neither the file nor the explanation contains its text, an endpoint or a key.
- The agent that answers peers looks an ID up with the tool `explain_error`. It always has this tool, whatever
  `a2a` lists, and it is not a tool you can list. It finds only failures of the peer that is asking: with the
  ID of somebody else's error a peer gets the same "nothing on record" as with a made-up one.
- The explanation comes from the peer's agent, so it is there once that agent can answer again. While its LLM
  is still down, asking about the ID fails like any other message, with a new ID. Your agent knows this and
  does not promise you an explanation it does not have.

## Beyond the local network

VPNs like Tailscale forward neither multicast nor broadcast, so nodes there are probed directly:

- **Tailscale** (and Headscale): the online peers of the tailnet are probed automatically, as reported by
  `tailscale status`. Nothing to configure.
- Layer 2 VPNs with broadcast such as ZeroTier work like a LAN.

A node reachable on several ways is listed once.

## Trust

Every node owns an Ed25519 key pair (generated once on first run, also when the node and the UI
start at the same time, and stored in `config.json`). Requests
are signed and the sender's public key is announced with discovery. The ID of a node is the
fingerprint of its public key (12 hex characters), so it stays the same as long as the key does;
an answer to a discovery probe whose ID does not fit its key is dropped. A node only answers
trusted peers:

1. Alice's agent writes to Bob (ask it to, or use `ouro ask`): Bob records a **connection request** and answers `pending`; no LLM call happens.
2. In Bob's terminal UI press `R` to open the requests screen and then `a` to accept (or `x` to reject).
3. Bob notifies Alice, both sides store each other as trusted and can chat.

Rejected peers are blocked. Trust is mutual: one acceptance connects both sides.

A decision that cannot be stored (full disk, `contacts.json` not writable) is not made: the UI says that
accepting or rejecting failed, the request stays in the list for another try, and the cause is in `ouro.log`.
The same goes for the node: a peer whose message, acceptance or rejection could not be recorded is answered
with an internal error instead of being left to believe it worked, and the cause is logged. Contacts that
cannot be read trust nobody.

Everything between nodes is encrypted, and both directions are authenticated with these keys:

- The **connection** is TLS 1.3; plain HTTP is refused. A node presents a self-signed certificate made
  from its key each time it starts (no certificate authority, nothing but the key is stored). Whoever
  connects pins it: the handshake fails unless the certificate is for the public key the node is known
  by, which is the key a peer was stored with or the one a discovered node announced. Nothing of the
  request is sent before that. Whoever announces the ID and key of a peer of yours without holding the
  key is not listed as connected, gets no message and cannot answer for it. This covers messages,
  decisions on connection requests, the agent card, and your own `ouro` talking to your node.
- A **request** is signed for one node and one method: the signature covers the recipient's public key
  (the key the connection is pinned to), the JSON-RPC method, the body, a timestamp and a nonce. A node
  refuses what was signed for another node, so a request cannot be passed on to someone else who trusts
  the sender. This is how the node that is asked knows who asks.
- A **response** and the **agent card** carry no signature of their own: they come over the connection
  that proved who answers.

| What | Pinned to |
|---|---|
| a connected peer | the public key stored in `contacts.json` when you connected; the node announcing it has to pass the handshake to count as connected |
| a discovered node you are not connected to | the public key it announced (its ID is the fingerprint of that key) |
| a node addressed as `host:port` | the key of the certificate presented on first contact; the message that follows only goes to the holder of that key |
| the answer to a connection request | the public key of the node that asked |

A node you address as `host:port` has no stored key: it is whoever answers at that address.
Discovery itself is neither encrypted nor signed: names, models, public keys and ports of the nodes on a
network are visible to everyone on it, and an announcement only counts for what the handshake confirms.

## Code

```
cmd/ouro             entry point
plugin               what a plugin is written against: the one package it imports (+ example, a plugin to copy)
internal/cli         the list of plugins that make up ouro, and the frame they do not get
internal/plugins     the standard plugins, one package each: commands, setup dialog, wiring
internal/kernel      plugin kernel: services, contributions, start order by declared dependencies
internal/tui         terminal UI (Bubble Tea)
internal/node        the local node: Server runs it, Local controls the background one
internal/assistant   blueprint of the agents (owner and peers) and the tools list_peers, ask_peer, explain_error
internal/peers       the other nodes: who is online, who is connected, how to talk to them
internal/home        the node's directory: config, identity, contacts, failed answers, PID and log file
internal/a2a         A2A protocol: types, HTTP handler, client
internal/agent       conversation memory and tool loop on top of an LLM
internal/openai      minimal OpenAI-compatible client (+ openaitest fake)
internal/discovery   LAN discovery, plus sources of hosts to probe directly (Tailscale)
internal/daemon      background process + PID file
internal/contacts    trusted peers and connection requests
internal/diagnosis   why answers to peers failed, by category, for the agent to explain
internal/identity    Ed25519 key pair of a node
internal/config      persisted configuration
internal/update      self-update from the newest main
```

### Extending

ouro is a small kernel (`internal/kernel`) and a list of plugins (`Plugins` in `internal/cli/cli.go`). A plugin
is a package of its own, written against one package, `ouroboros/plugin`: that is all it imports of ouro. The
standard plugins are in `internal/plugins`; `plugin/example` is a complete one to copy.

A plugin says what it needs in the struct its start function takes, one field per service. ouro starts every
plugin once, after the plugins that provide what it needs. If a service is missing, provided twice, or plugins
need each other in a circle, ouro does not start at all and says which plugins and services are in the way.

This plugin lets two nodes greet each other. It adds a method to the node, and a command that runs that
method on a peer:

```go
package example

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"ouroboros/plugin"
)

const Method = "example/greet"

type greeting struct {
	From string `json:"from"`
}

// What the plugin needs: services of other plugins.
type needs struct {
	Console *plugin.Console
	Network *plugin.Network
}

func Plugin() plugin.Plugin {
	return plugin.New("example", func(ctx *plugin.Context, n needs) error {
		// The receiving side: a method connected peers can run.
		plugin.Add(ctx, plugin.Method{Name: Method, Handle: greet})
		// The calling side: ouro greet NODE NAME
		plugin.Add(ctx, plugin.Command{Name: "greet", Args: "NODE NAME", Help: "greet a connected node as NAME",
			Run: func(c context.Context, args []string) error {
				if len(args) != 2 {
					return errors.New("usage: ouro greet NODE NAME")
				}
				node, err := n.Network.Resolve(c, args[0])
				if err != nil {
					return err
				}
				reply, err := n.Network.Call(c, node, Method, greeting{From: args[1]})
				if err != nil {
					return err
				}
				fmt.Fprintln(n.Console.Out, reply.Text())
				return nil
			}})
		return nil
	})
}

func greet(_ context.Context, from plugin.Caller, params json.RawMessage) (*plugin.Message, *plugin.Error) {
	var g greeting
	if json.Unmarshal(params, &g) != nil || g.From == "" {
		return nil, &plugin.Error{Code: plugin.CodeInvalidParams, Message: `needs "from"`}
	}
	return plugin.Reply(fmt.Sprintf("hello %s, I know you by the key %.8s", g.From, from.PublicKey)), nil
}
```

Add `example.Plugin()` to `Plugins` on both nodes and rebuild. `Network.Call` goes the way every message
goes: signed with the key of the node for the peer and the method, over TLS pinned to the key of the peer, and
only to a peer the owner is connected to (`plugin.ErrNotConnected` otherwise, with nothing sent). On the other
side `greet` runs only after the same checks as the built-in methods: a request that is unsigned, replayed,
signed for another node or another method, or from a node the owner is not connected to never reaches it.

| To add | In the start function | Example |
|---|---|---|
| a command | `plugin.Add` a `plugin.Command` | `internal/plugins/talk` |
| a tool for the agents | `plugin.Add` a `plugin.Tool` (`plugin.NewTool`) | `internal/plugins/peers` |
| an A2A method for connected peers | `plugin.Add` a `plugin.Method`; peers run it with `Network.Call` | `plugin/example` |
| a way to find nodes | `plugin.Add` a `plugin.PeerSource` | `internal/plugins/tailscale` |
| another front end | `plugin.Add` a command that runs it | `internal/plugins/tui` |
| another LLM API | return a `plugin.LLM` from `plugin.NewService`, instead of the openai plugin | `internal/plugins/openai` |
| a service for other plugins | return it from `plugin.NewService`; they name its type in their needs | `internal/plugins/discovery` |

Not plugins, and out of their reach: the identity of the node, request signatures, trust and contacts, the
built-in A2A methods, the wire format, update and start/stop. They are the fixed frame, wired up in `Plugins`
and never handed to the kernel: a plugin that needs or provides one of their types, or a type through which
one can be had, stops ouro from starting, as does a second plugin that provides a service that exists. A
method added under a built-in name is never called, and `Network.Call` does not run the built-in ones.

Plugins are compiled in. There is no loading at runtime.

```sh
make test    # vet + tests with race detector
make cover   # coverage including the end-to-end test
```

## License

Ouroboros is licensed under the [Apache License 2.0](LICENSE) (`Apache-2.0`); the copyright notice is in
[NOTICE](NOTICE). The binary links third-party modules under the MIT and BSD-3-Clause licenses, listed with
their license texts in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES). Pass all three files on with the binary.

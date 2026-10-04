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

The UI is the chat with your own agent. You do not
write to other nodes yourself: the agent does, whenever you ask it to or it thinks a peer knows
better ("ask Luna how she is" → "Luna says she is fine"). What the two agents say to each other
stays between them; the UI only shows which peer your agent is waiting for.
The input is focused on start, so you can type right away. The conversation lives only as long as the TUI runs.

The UI is a plugin like any other (`internal/plugins/tui`), and it is the front end of the standard ouro: the
plugin says so itself, nothing else names it. An ouro built without it prints the usage when it is started
without a command, and `ouro chat` talks line by line. If the list of plugins holds more than one front end,
ouro does not pick one: `ouro` and `ouro chat` fail and name the commands that open them (`ouro tui`, …),
which keep working.

Other plugins add to the UI, see [Extending](#extending):

- a **screen** next to the chat, opened with its key where the commands are and left with `esc`, `q` or the
  same key. It may have keys of its own. The footer lists what the plugins added.
- a **key** where the commands are, which runs something and reports in the footer.
- what is shown of a **tool call** while the agent waits for it. `asking beta…` is what the plugin of
  `ask_peer` shows; a tool without such a line shows as the agent thinking.

A key has one meaning. The UI keeps its own keys (the table below), and of two plugins that claim the same
key the one that was started first gets it, a screen before a plain key. What loses its key is left out, and the UI says so in red in its
footer when it opens, until the first key is pressed: `key s is taken by the terminal UI: the screen "Stats"
is left out`. The UI opens all the same, and so does everything else of that plugin.

While the UI is open your node is online: opening it starts the node, and closing it stops the node
again unless it was already running before (`ouro start`).

| Keys | Commands | Message input |
|---|---|---|
| any key | skip the ouroboros splash (except `ctrl+c`) | – |
| `tab` / `esc` | focus the input (`tab`) | back to the commands |
| `enter` | focus the input | send (`alt+enter` for a new line) |
| `s` / `c` | start or stop the node / run the setup | – |
| `n` | new conversation | – |
| `R` | connection requests, of peers and what your agent proposes (`a` accept, `x` reject, `esc` back) | – |
| keys of plugins | open a screen (`esc` back) or run what the plugin added | – |
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
| Channels | JSON-RPC `channel/open` on `POST /` with `Upgrade: ouro-channel/1`: the node answers `101 Switching Protocols` and the TLS connection of the request carries messages in both directions until one side closes it, see [Channels](#channels). Not part of A2A |
| Discovery | UDP probe to multicast `239.255.41.38`, the broadcast address of every network, localhost and the peers of the tailnet on port 41338; nodes answer the sender and the group, so only port 41338 needs to be open |
| Agents | two per node, built from one blueprint: yours (`ouro`, `ouro chat`) runs in the UI process, the one answering peers in the node. Which tools each may use is set in `config.json`, see [Tools](#tools); at most 8 LLM calls per answer |
| Memory | last 40 messages per conversation, up to 256 conversations; a conversation of a peer is its `contextId` together with its key, so no peer continues or reads the conversation of another |
| Card | the description in the agent card is `Ouroboros instance named <name>` for every node; `list_peers` shows your agent the cards of connected peers. `capabilities.streaming` is `false`: in A2A it stands for `message/stream` (the answer to a message as server-sent events), which a node does not have. That peers can open channels is listed under `capabilities.extensions`; which channels is not, as anyone can read the card |
| Trust | nodes talk over TLS 1.3 with a self-signed certificate made from the node's Ed25519 key; the connection is pinned to the key the node is known by, and requests are signed for that key; a message only goes to a peer both owners agreed to connect to, and a node asks another to connect with `contact/request`, which carries no message |
| Limits | at most 64 connection requests wait for a decision: when the list is full, the one that waited longest makes room for a new one (trusted peers are never dropped). What your agent proposes waits in the same list but never takes the place of a request: with 64 waiting it is not recorded. One address gets 10 messages per minute from nodes that are not connected; further ones are refused with error `-32005` and leave no request (at most 1024 addresses are counted per minute, unknown nodes from any further address are refused). Against replays the node remembers every nonce for at least 4 minutes, at most 16384 per 4 minutes; beyond that it refuses requests rather than forget a nonce early. A connected peer has at most 8 channels open to a node at a time, a further one is refused with error `-32006`; a channel without a message in either direction for 5 minutes is closed; a message of a channel has at most 1 MiB; the channels of a peer that was removed from `contacts.json` or refused are closed within a second, and all channels when the node stops |
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
in your name, and nobody else: for a peer the agent neither writes to a node you are not connected to nor
proposes a connection. Two rules keep that from running in circles: the agent never asks the peer it is
answering, and a message is passed on by at most 3 agents.

`ask_peer` takes the name or ID of a node that is online, as `list_peers` shows it, and no address: an
agent cannot be sent to a `host:port` somebody put into its head. Only you address a node that way, with
`ouro ask`.

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

1. Alice's agent wants to write to Bob (she asks it to, or uses `ouro ask`). Bob is not connected, so
   **nothing is sent**, not even a request: Bob is proposed to Alice, with his ID and key.
2. In Alice's terminal UI `R` shows `bob · your agent wants to connect`. With `a` she agrees, and only
   then Bob gets a **connection request** (`contact/request`). It carries no message; Bob records it and
   answers `pending`, no LLM call happens. With `x` the proposal is dropped and Bob never hears of it.
3. In Bob's terminal UI press `R` and then `a` to accept (or `x` to reject).
4. Bob notifies Alice, both sides store each other as trusted and can chat. Until then Alice's agent is
   told that Bob has not decided, and what she wanted to say stays with her.

So a connection takes one decision of each owner, and no message reaches a node before both are made. A
name is only what a node calls itself: whoever shows up as `bob` while the Bob you know is away is a
stranger, gets nothing, and waits in your requests under his own key for you to notice. Neither your agent
nor a plugin can connect on its own, whatever a peer talks it into. A node that says it is connected to
you already (its acceptance got lost on the way) is trusted when it is asked again, but only if you asked
it before.

Rejected peers are blocked. Trust is mutual: one acceptance connects both sides. Nodes from before
`contact/request` do not know the method: update both sides.

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
- A **channel** is opened by such a request, signed for the node, and is the connection that request came
  over: pinned and encrypted like every other, and only opened for a peer you are connected to. It is
  closed as soon as you are not any more.

| What | Pinned to |
|---|---|
| a connected peer | the public key stored in `contacts.json` when you connected; the node announcing it has to pass the handshake to count as connected |
| a discovered node you are not connected to | the public key it announced (its ID is the fingerprint of that key) |
| a node addressed as `host:port` | the key of the certificate presented on first contact; what follows only goes to the holder of that key, and a message only if that key is a peer of yours. Otherwise the holder is proposed to you like any stranger |
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
internal/a2a         A2A protocol: types, HTTP handler, client; channels between nodes
internal/agent       conversation memory and tool loop on top of an LLM
internal/openai      minimal OpenAI-compatible client (+ openaitest fake)
internal/discovery   LAN discovery, plus sources of hosts to probe directly (Tailscale)
internal/daemon      background process + PID file
internal/contacts    trusted peers and connection requests
internal/diagnosis   why answers to peers failed, by category, for the agent to explain
internal/identity    Ed25519 key pair of a node
internal/config      persisted configuration
internal/update      self-update from the newest main
scripts, hooks       the local quality checks and the pre-commit hook, see [Checks](#checks)
```

### Extending

ouro is a small kernel (`internal/kernel`) and a list of plugins (`Plugins` in `internal/cli/cli.go`). A plugin
is a package of its own, written against one package, `ouroboros/plugin`: that is all it imports of ouro. The
standard plugins are in `internal/plugins`; `plugin/example` is a complete one to copy.

A plugin says what it needs in the struct its start function takes, one field per service. ouro starts every
plugin once, after the plugins that provide what it needs. If a service is missing, provided twice, or plugins
need each other in a circle, ouro does not start at all and says which plugins and services are in the way.

Two nodes work together in two ways: a **method** is a question with one answer, a
[**channel**](#channels) carries what flows in both directions. This plugin lets two nodes greet each other.
It adds a method to the node, and three ways to run that method on a peer: a command, a tool for the agents,
and a screen in the terminal UI. The part that wires it up, from `plugin/example/example.go`:

```go
package example

const Method = "example/greet"
const Tool = "greet_peer"

// What the plugin needs: services of other plugins.
type needs struct {
	Console *plugin.Console
	Network *plugin.Network
}

func Plugin() plugin.Plugin {
	return plugin.New("example", func(ctx *plugin.Context, n needs) error {
		g := &greeter{network: n.Network, answers: map[string]string{}}
		// The receiving side: a method connected peers can run.
		plugin.Add(ctx, plugin.Method{Name: Method, Handle: greet})
		// The calling side: ouro greet NODE NAME
		plugin.Add(ctx, plugin.Command{Name: "greet", Args: "NODE NAME", Help: "greet a connected node as NAME",
			Run: func(c context.Context, args []string) error {
				if len(args) != 2 {
					return errors.New("usage: ouro greet NODE NAME")
				}
				reply, err := g.greet(c, args[0], args[1])
				if err != nil {
					return err
				}
				fmt.Fprintln(n.Console.Out, reply)
				return nil
			}})

		// For the agents: the tool, and what the owner is shown while the agent waits for its result.
		plugin.Add(ctx, plugin.NewTool(Tool, "Greet a connected peer and return its answer.",
			[]plugin.Param{{Name: "peer", Description: "name or ID of the peer"}, {Name: "from", Description: "who greets"}},
			func(c context.Context, args toolArgs) (string, error) { return g.greet(c, args.Peer, args.From) }))
		plugin.Add(ctx, plugin.ToolView{Tool: Tool, Show: func(args json.RawMessage) string {
			var call toolArgs
			if json.Unmarshal(args, &call) != nil || call.Peer == "" {
				return ""
			}
			return "greeting " + call.Peer
		}})

		// For the terminal UI: g opens the screen, on which a greets everyone.
		plugin.Add(ctx, plugin.Screen{Key: "g", Title: "Greetings", Body: g.screen,
			Keys: []plugin.Key{{Key: "a", Help: "greet all", Run: g.greetAll}}})
		return nil
	})
}

// greet runs Method on the node target as from and returns its answer.
func (g *greeter) greet(ctx context.Context, target, from string) (string, error) {
	node, err := g.network.Resolve(ctx, target)
	if err != nil {
		return "", err
	}
	reply, err := g.network.Call(ctx, node, Method, greeting{From: from})
	if err != nil {
		return "", err
	}
	return reply.Text(), nil
}

// greet answers a greeting: the method, on the node that is greeted.
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

#### Channels

A method is one request with one answer, as JSON, of at most 1 MiB. What flows goes over a **channel**: audio,
a terminal, events, a file of any size. One plugin adds a `plugin.ChannelHandler`, the plugin of a connected
peer opens it by name with `Network.Open`, and both hold a `*plugin.Channel` until one of them closes it. The
same plugin (`plugin/example`) sends a file to a peer and hears after every piece how much has arrived:

```go
const Channel = "example/checksum"

// In the start function, next to the method:
plugin.Add(ctx, plugin.ChannelHandler{Name: Channel, Handle: checksum})

// The receiving side. When it returns, ouro closes the channel.
func checksum(_ context.Context, ch *plugin.Channel) {
	sum, arrived := sha256.New(), 0
	for {
		piece, err := ch.Receive()
		if err != nil {
			return // the peer closed the channel, or ouro did
		}
		if len(piece) == 0 { // the end of the file
			ch.Send(fmt.Appendf(nil, "sha256 %x", sum.Sum(nil)))
			return
		}
		sum.Write(piece)
		arrived += len(piece)
		ch.Send(fmt.Appendf(nil, "%d bytes arrived", arrived))
	}
}

// The opening side, in the command "ouro checksum NODE FILE".
ch, err := n.Network.Open(c, node, Channel)
if err != nil {
	return err
}
defer ch.Close()
piece := make([]byte, 256<<10)
for {
	read, err := file.Read(piece)
	if err != nil && err != io.EOF {
		return err
	}
	ch.Send(piece[:read]) // at the end of the file: the empty message
	reply, err := ch.Receive()
	if err != nil {
		return err
	}
	fmt.Fprintf(n.Console.Out, "%s: %s\n", plugin.Label(node), reply)
	if read == 0 {
		return nil
	}
}
```

Both ends have the same six calls, and may use them from several goroutines:

| Call | What it does |
|---|---|
| `Send(msg []byte) error` | sends one message of at most `plugin.MaxMessage` bytes (`plugin.ErrTooLarge` beyond; send what is larger in pieces). It waits while the peer does not receive, so nothing piles up |
| `Receive() ([]byte, error)` | the next message of the peer, whole and in the order it was sent |
| `Close() error` | closes the channel: the peer still gets what was sent, then `io.EOF` |
| `CloseWith(reason string) error` | the same, but the peer gets a `*plugin.ChannelClosed` with the reason (256 bytes of it) |
| `Context() context.Context` | done as soon as the channel is closed by anyone; `context.Cause` says why. It is the context `Handle` gets |
| `Peer() string` | the public key of the node on the other side |

Once a channel is closed, `Send` and `Receive` return why: `io.EOF` if the peer closed it,
`plugin.ErrChannelClosed` if this side did, a `*plugin.ChannelClosed` if a reason was given, or the error of
the connection that broke. A channel opened with `Network.Open` lives no longer than the context it was opened
with.

A plugin never signs and never decides whom to trust; the channel is part of the frame:

- **Opening** is the built-in method `channel/open`, a request like every other: signed with the key of the
  node for the peer, with a timestamp and a nonce, the name of the channel in the signed body, over TLS pinned
  to the key of the peer, and only sent to a peer the owner is connected to. The peer checks it like a call of
  a method and then looks whether its owner is connected to the caller. Whoever is unsigned, replayed, signed
  for another node or another channel, unknown or refused gets a JSON-RPC error, and no handler runs. Whoever
  passes gets `101 Switching Protocols`, and the connection of the request is the channel. A built-in name is
  never a channel, and `Network.Call` does not run `channel/open`.
- **The limits** are kept by ouro, whatever the plugins at the two ends do:

  | Limit | Value | Then |
  |---|---|---|
  | channels one peer has open to a node (`plugin.MaxChannelsPerPeer`) | 8 | one more is refused with `plugin.CodeTooManyChannels` (`-32006`) |
  | time without a message in either direction (`plugin.ChannelIdleTimeout`) | 5 minutes | closed with `plugin.ErrIdle` |
  | size of one message (`plugin.MaxMessage`) | 1 MiB | `Send` fails with `plugin.ErrTooLarge`; a peer that sends more breaks the channel |
  | the peer is no longer connected: removed from `contacts.json` or refused | checked every second, as the UI changes the contacts in another process; at once in the process that rejects | every channel with that peer is closed with `plugin.ErrDisconnected`, on the node that was asked and on the one that opened |
  | the node stops | – | every channel is closed with `plugin.ErrStopped`: the ones peers opened and the ones its plugins opened |
  | time to open a channel | 30 seconds | `Network.Open` fails |
  | time a closed channel waits for the peer to take what was sent | 2 seconds | the connection is cut |

- **The transport** is the connection of the opening request, taken over with the `Upgrade` of HTTP/1.1
  (`Upgrade: ouro-channel/1`), from the standard library and without a further dependency. A2A has
  `message/stream` for what flows, but that is server-sent events: text from the node that was asked to the
  node that asked, as the answer to one message. A channel needs both directions at once, and bytes. So
  `message/stream` is not implemented and the agent card does not claim `streaming`; channels are listed there
  as an extension. On the connection each side sends frames: one byte for the kind (`0` a message, `1` the end
  of the channel), four bytes for the length (big endian, at most 1 MiB), then the message, or for the end
  the reason as text (empty for none).

Which peer may open which channel is not decided yet: every connected peer may open every channel a plugin
adds, as it may call every method.

| To add | In the start function | Example |
|---|---|---|
| a command | `plugin.Add` a `plugin.Command` | `internal/plugins/talk` |
| a tool for the agents | `plugin.Add` a `plugin.Tool` (`plugin.NewTool`) | `internal/plugins/peers` |
| what the terminal UI shows of a tool call | `plugin.Add` a `plugin.ToolView` | `plugin/example`, `internal/plugins/peers` |
| a screen of the terminal UI | `plugin.Add` a `plugin.Screen` | `plugin/example` |
| a key of the terminal UI | `plugin.Add` a `plugin.Key`, or list it in `Screen.Keys` | `plugin/example` |
| an A2A method for connected peers | `plugin.Add` a `plugin.Method`; peers run it with `Network.Call` | `plugin/example` |
| a channel to connected peers, for what flows | `plugin.Add` a `plugin.ChannelHandler`; peers open it with `Network.Open` | `plugin/example` |
| a way to find nodes | `plugin.Add` a `plugin.PeerSource` | `internal/plugins/tailscale` |
| another front end | `plugin.Add` a `plugin.FrontEnd` and the command that opens it, instead of the tui plugin | `internal/plugins/tui` |
| another LLM API | return a `plugin.LLM` from `plugin.NewService`, instead of the openai plugin | `internal/plugins/openai` |
| a service for other plugins | return it from `plugin.NewService`; they name its type in their needs | `internal/plugins/discovery` |

What a plugin hands to the terminal UI is text and functions, nothing of how the UI is drawn. A screen is a
key, a title and `Body`, which returns the text to show; a key is `Run`, which returns the line for the
footer. Both run off the UI, so they may ask the network, and with a context that ends when the user quits.
`Body` is called when the screen opens, after one of its keys ran and every ten seconds while it is open. A
key is named as the terminal reports it: `g`, `G`, `ctrl+g`. See [Terminal UI](#terminal-ui) for what happens
when two plugins want the same key.

A front end is what opens when `ouro` runs without a command, and what `ouro chat` opens without a message.
It talks to the agent of the owner through the service `*plugin.Assistant`, and shows a tool call as
`plugin.ToolLine` says. ouro opens a front end only if the list of plugins holds exactly one: with none it
prints the usage, with several it fails and names them.

The terminal UI is also where the owner starts and stops the node and accepts or rejects connection
requests. Those are operations of the frame, so no service offers them. They are in `plugin.Owner`, and that
is all that is in it: the status of the node, start, stop, the setup, the requests that are waiting, accept
and reject. The contacts, the key and the configuration cannot be had from it. A front end takes the `Owner`
as an argument of its `Plugin` function and gets it in `Plugins`, where the tui plugin gets it today. A plugin
cannot ask the kernel for it: `plugin.Owner` is sealed like the contacts, so that a plugin nobody put into
that list by hand, such as one an agent wrote, never decides whom the node trusts.

Not plugins, and out of their reach: the identity of the node, request signatures, trust and contacts, the
built-in A2A methods, opening a channel and its limits, the wire format, update and start/stop. They are the
fixed frame, wired up in `Plugins` and never handed to the kernel (a front end starts and stops the node
through the `plugin.Owner` it is given there): a plugin that needs or provides one of their types, or a type
through which
one can be had, stops ouro from starting, as does a second plugin that provides a service that exists. A
method added under a built-in name is never called, and `Network.Call` does not run the built-in ones.

Plugins are compiled in. There is no loading at runtime.

## Checks

The quality checks run on your machine: there is no CI. One command runs all of them, and a Git hook
runs them before every commit.

```sh
make check   # everything below, on the working tree
make hooks   # once per clone: run the checks before every commit
```

| Command | Checks | Fails when |
|---|---|---|
| `make fmt-check` | formatting with `gofmt`; lists files, changes none | a Go file is not formatted |
| `make lint` | `staticcheck` with every check on ([staticcheck.conf](staticcheck.conf)) | it reports anything |
| `make test` | `go vet`, then the tests with the race detector | vet reports anything, a test fails, a race is seen |
| `make cover-check` | statement coverage of the production code | one statement is not covered |
| `make mutation` | mutation tests with `gremlins` ([.gremlins.yaml](.gremlins.yaml)) | never because of survivors; they are reported |
| `make check` | all of the above, in this order | any of them fails |

`make cover` only measures and prints the percentages; `make tools` builds the two tools ahead of time.

### Prerequisites

- **Go** as named in `go.mod`. It is found in `PATH`, in `~/.local/go/bin` (where `./install.sh` puts it; set
  `OURO_GO_DIR` for another place) and in `/usr/local/go/bin`. `gofmt`, `go vet` and the coverage tools come
  with it.
- **make**, **git** and a POSIX shell with `awk`, `sed`, `grep`. `timeout` (coreutils) is used to limit the
  mutation tests in the hook; without it they run to the end.
- **Linux or macOS**, with pseudo-terminals (`/dev/ptmx`): the end-to-end test runs `ouro` at one. The macOS
  half of that test is written but was never run; the checks were set up and measured on Linux.
- **Network on the first run**: `staticcheck` and `gremlins` are built from source with `go install pkg@version`
  into `.tools/` of the checkout. Their versions are pinned in [scripts/tool-versions](scripts/tool-versions),
  the only place. They are not dependencies of the module: `go.mod`, `go.sum` and the binary stay as they are,
  and nothing is installed outside the checkout (the downloaded sources go to the Go module cache).
  Changing a version there, or updating Go, builds the tool anew.

### Coverage

`make cover-check` requires that **every statement of the production code is covered**, which is 100% counted
from the blocks of the coverage profile (`cover.out`): statements are added up, nothing is rounded, and one
uncovered statement fails the check. Every uncovered block is printed as `file:line`, followed by the count per
package.

- **What counts as covered**: a statement that ran in the unit tests of any package, or in the binary the
  end-to-end test (`cmd/ouro/main_test.go`) builds and drives: with piped input, at a pseudo-terminal, where
  the API key is typed without being shown, and with an environment it cannot use. Both write into one profile.
- **What is measured**: every package of the module, also one without tests (it then counts as uncovered) and
  `cmd/ouro/main.go`. A package that is missing from the profile fails the check.
- **What is left out**: only the packages listed in [coverage-exclusions.txt](coverage-exclusions.txt), each with
  its reason. Today these are `internal/openai/openaitest`, `internal/update/updatetest` and
  `internal/cli/clitest`: the fakes and the helpers that the tests of several packages share (224 statements),
  test code, not production code. Only packages that the `ouro` binary does not link can be listed; the check
  refuses any other line. `_test.go` files are never measured, Go does not instrument them.
- **What 100% does not say**: it counts statements, not branches. In `cmd/ouro/main.go` the error of putting
  ouro together and the error of the command leave through the same statement. Only the second one happens in
  the end-to-end test: the plugins ouro is made of never fail to start.

### Mutation tests

[gremlins](https://gremlins.dev) changes one operator at a time and runs the tests of the package the change is
in. A mutant the tests fail on is *killed*; one they pass with *survived* and is reported as `file:line:column`.

| Mutation | Example |
|---|---|
| `CONDITIONALS_NEGATION` | `==` ↔ `!=`, `<` ↔ `>=`, `>` ↔ `<=` |
| `CONDITIONALS_BOUNDARY` | `<` ↔ `<=`, `>` ↔ `>=` |
| `ARITHMETIC_BASE` | `+` ↔ `-`, `*` ↔ `/`, `%` → `*` |
| `INCREMENT_DECREMENT` | `++` ↔ `--` |
| `INVERT_NEGATIVES` | `-x` → `+x` |
| `INVERT_LOGICAL` | `&&` ↔ `\|\|` |
| `INVERT_LOOPCTRL` | `break` ↔ `continue` |

Scope and limits:

- Mutated is the production code: every package except the ones in `coverage-exclusions.txt`.
  `make mutation PKGS="internal/a2a internal/agent"` limits the run to some packages. A package is the Go files
  in its directory; the packages below it have their own turn.
- A mutant is tried against the tests **of its own package** only. Code that only the tests of another package
  or the end-to-end test reach is listed as *not covered* and not tried. Statement coverage counts those runs,
  mutation testing does not.
- Statements are not removed, constants and strings are not changed, and the bitwise and assignment mutations
  of gremlins are off: operators only.
- A mutant whose tests do not finish in time is *timed out* and counts as noticed.
- **Packages that send signals to process IDs are not mutated**, today `internal/daemon` and `cmd/ouro` (its
  test kills the node). A mutant there lets a PID of 0 or -1 through, and the code under test then signals its
  own process group (that happened when the checks were set up: the run terminated itself) or every process
  you own. `scripts/mutation.sh` finds these packages by the calls (`syscall.Kill`, `unix.Kill`,
  `os.FindProcess`), in code and tests, and names them in the report. Such code needs its assertions reviewed
  by hand.
- The mutants of other packages reach that code unchanged, through `daemon.Stop`. Whatever they hand it, one
  function sends the signal, and it refuses a PID of 1 or less (`terminate` in `internal/daemon`, with a test).

**Survivors do not fail the run**; no threshold is agreed on. They are for review, and each is one of two things:

- a **gap in the assertions**: the change alters what the code does and no test notices. Add the assertion.
- an **equivalent mutant**: the change cannot alter what the code does (for example `<` ↔ `<=` where both
  sides are never equal). Nothing to test. List it in `mutation-equivalents.txt` as
  `file:line:column MUTATION  # reason`, and it is reported as accepted instead of as a survivor. When the
  line moves, the entry no longer matches and the mutant comes back for another look.

The report is in `.mutation/report.txt`, written anew by every run, with the output of gremlins for each
package next to it. How to read it:

| Part of the report | Meaning | What to do |
|---|---|---|
| first line | the counts of the run | nothing: there is no score to reach |
| `Survived` | the tests pass with this operator changed | decide: missing assertion, or equivalent (see above) |
| `Not covered` | no test of the package itself runs this line | nothing, if another package or the end-to-end test does: `make cover-check` proves that |
| `Timed out` | the tests did not finish with the change | nothing: it was noticed |
| `Accepted as equivalent` | survivors listed in `mutation-equivalents.txt` | nothing; a line that no longer matches is named |
| `Not mutated` | the packages left out because they signal processes | review their tests by hand |

The whole module today: 1019 mutants, 734 killed, 180 survived, 4 accepted as equivalent, 11 timed out, 90 not
covered; 2 packages not mutated. Of the survivors, 153 are in the drawing of `internal/tui` (most in the
arithmetic of the logo), 12 in the parts of `internal/discovery` that depend on the network interfaces of the
machine, and 4 in `internal/safefile`, where what the mutant leaves out only shows after a power failure or
a write that fails. None is left in `internal/a2a`, `contacts`, `identity`, `kernel`, `peers` and `diagnosis`.

### Git hook

```sh
make hooks           # install the pre-commit hook into this clone
make hooks CHAIN=1   # the same, keeping a pre-commit hook that is already there
make unhooks         # remove it again
```

The hook is `hooks/pre-commit`, versioned with the code. `make hooks` writes a few lines to `.git/hooks/pre-commit`
that run it, so it changes along with the branch you are on.

- **It is local.** Every clone needs its own `make hooks` (the worktrees of a clone share it).
  `git commit --no-verify` skips it. Nothing on the server enforces it: it is not merge protection, and a
  commit that never saw the checks can be pushed.
- **An existing setup is kept.** If there already is a pre-commit hook, or `core.hooksPath` points somewhere
  else, `make hooks` installs nothing and says how to run both. With `CHAIN=1` the existing hook is moved to
  `pre-commit.local` and runs first; `make unhooks` moves it back. Other hooks are not touched.
- **It checks what you commit.** The staged files are copied to a temporary directory and the checks run
  there: unstaged changes and untracked files play no part, and neither your working tree nor the index is
  changed. Nothing is formatted, fixed or staged for you.
- **It runs** `fmt-check`, `lint`, `test` and `cover-check`, and rejects the commit when one fails, with the
  findings and the command to run that check alone. Then it runs the mutation tests **for the packages whose
  Go files are staged**, for at most 5 minutes, and prints the survivors without rejecting the commit.
- A commit that stages no Go code and nothing of the checks themselves (only `README.md`, say) is not checked.

| Variable | Effect |
|---|---|
| `OURO_HOOK_MUTATION=off` | no mutation tests in the hook |
| `OURO_HOOK_MUTATION_TIME=600` | stop them after that many seconds instead of 300; the packages left over are named |
| `OURO_GO_DIR=/opt/go` | where Go is, if not in `PATH` or `~/.local/go` |

### How long it takes

Measured on 8 cores with a warm Go build cache and nothing else running; expect the order of magnitude, not
the second. With every core busy, `make test` takes 75 to 105 s instead of 34 s.

| Step | Time |
|---|---|
| building the tools, first run only | staticcheck 14 s, gremlins 11 s, plus the download of their sources |
| `make fmt-check` | under 1 s |
| `make lint` | 2 s |
| `make test` | 34 s (1 s when nothing changed since the last run: Go keeps the results) |
| `make cover-check` | 20 s |
| `make mutation`, whole module | 24 min |
| `make mutation PKGS=…`, one package | under 1 min for the small ones; `internal/config` 75 s, `internal/discovery` 90 s, `internal/a2a` 3.5 min; `internal/tui` more than 3 min |
| `make check` | 25 min |
| the hook, without the mutation tests | 1.5 min |
| the hook, with them | 5 s more for a small package, never more than 5 min more |

The hook takes longer than `make test` and `make cover-check` together because it runs them on a copy of the
staged files, where no earlier test result can be reused.

Mutation tests are slow by nature: every mutant is compiled and tested on its own, about 1000 of them. That
is why the hook only mutates the packages a commit changes and stops after 5 minutes, and why the whole
module is left to `make mutation` and `make check`. A mutant that makes its tests hang costs up to 90 s.

### Troubleshooting

| Problem | What to do |
|---|---|
| `go not found` | Go is neither in `PATH` nor in `~/.local/go/bin` nor in `/usr/local/go/bin`: run `./install.sh` once (it puts Go into `~/.local/go`), or set `OURO_GO_DIR` to where it is |
| `make check` works, plain `go test` says `command not found` | `make` and the hook find a Go outside `PATH` themselves, your shell does not: `export PATH="$HOME/.local/go/bin:$PATH"` |
| `could not build staticcheck` / `gremlins` | the first run downloads them: check the network, then `make tools` |
| a tool behaves oddly after an update of Go | `rm -rf .tools && make tools` |
| the hook does not run | `make hooks` in this clone; `git config core.hooksPath` has to be empty |
| `make hooks` refuses | a hook setup is already there; the message names it and the two ways to keep both |
| the hook passes or fails, `make check` does the opposite | the hook checks the staged files, `make check` the working tree: `git status` shows the difference |
| a path in the hook's output starts with `/tmp/ouro-pre-commit.` | that is the copy of the staged files; the same path is in your checkout |
| the end-to-end test fails with ports or a node already running | it starts a real node on free ports; run it alone with `go test ./cmd/ouro` |
| the end-to-end test fails at `/dev/ptmx` or `/dev/pts` | it runs `ouro` at a pseudo-terminal; a container without `devpts` has none |
| a test fails with `node did not start` | should not happen any more: a node built with the race detector needs 0.7 s to come up and several seconds on a busy machine, so starting waits up to a minute, and ends at once when the process dies. If it does, the log named in the message says why the node ended |
| a test fails only while the machine is busy | a wait in that test is too short; say which one. The checks were run with every core busy without a failure, but only a few times |
| mutants are all `TIMED OUT` | the machine is busy: run them again when it is not. A mutant gets 90 s, or three times what the unchanged tests take (`mutant_time` in `scripts/mutation.sh`) |
| the mutation tests take too long before a commit | `OURO_HOOK_MUTATION=off git commit …`, then `make mutation PKGS=…` later |
| it has to go in now | `git commit --no-verify`, and run `make check` afterwards |

## License

Ouroboros is licensed under the [Apache License 2.0](LICENSE) (`Apache-2.0`); the copyright notice is in
[NOTICE](NOTICE). The binary links third-party modules under the MIT and BSD-3-Clause licenses, listed with
their license texts in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES). Pass all three files on with the binary.

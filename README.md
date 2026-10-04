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
| A2A | `GET /.well-known/agent-card.json`, JSON-RPC `message/send` on `POST /` (TCP 41337, falls back to a free port) |
| Discovery | UDP probe to multicast `239.255.41.38`, the broadcast address of every network, localhost and the peers of the tailnet on port 41338; nodes answer the sender and the group, so only port 41338 needs to be open |
| Agents | two per node, built from one blueprint: yours (`ouro`, `ouro chat`) runs in the UI process, the one answering peers in the node. Which tools each may use is set in `config.json`, see [Tools](#tools); at most 8 LLM calls per answer |
| Memory | last 40 messages per `contextId`, up to 256 conversations |
| Card | the description in the agent card is `Ouroboros instance named <name>` for every node; `list_peers` shows your agent the cards of connected peers |
| Trust | signed requests (Ed25519); unknown peers become connection requests a human approves |
| Files | `~/.config/ouroboros/` – `config.json` (0600), `contacts.json` (0600), `ouro.pid`, `ouro.log`, `update/`. `config.json` and `contacts.json` are replaced in one step (temporary file, then rename), so a crash never leaves half a file; changes are serialized by a lock on `config.json.lock` / `contacts.json.lock` |
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
| `a2a` (agent to agent) | a connected peer asks your agent | no tool |

`"*"` stands for every tool, `[]` for none. A name that is not a tool is an error: the node does not start,
the chat does not answer. Restart the node after changing `a2a`.

With `ask_peer` in `a2a`, a peer you connected to can make your agent ask the peers you are connected to,
in your name. Two rules keep that from running in circles: the agent never asks the peer it is answering,
and a message is passed on by at most 3 agents.

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

## Code

```
cmd/ouro             entry point
internal/cli         the plugins that make up ouro: commands, setup dialog, wiring
internal/kernel      plugin kernel: services, contributions, start order
internal/tui         terminal UI (Bubble Tea)
internal/node        the local node: Server runs it, Local controls the background one
internal/assistant   blueprint of the agents (owner and peers) and the tools list_peers, ask_peer
internal/peers       the other nodes: who is online, who is connected, how to talk to them
internal/home        the node's directory: config, identity, contacts, PID and log file
internal/a2a         A2A protocol: types, HTTP handler, client
internal/agent       conversation memory and tool loop on top of an LLM
internal/openai      minimal OpenAI-compatible client (+ openaitest fake)
internal/discovery   LAN discovery, plus sources of hosts to probe directly (Tailscale)
internal/daemon      background process + PID file
internal/contacts    trusted peers and connection requests
internal/identity    Ed25519 key pair of a node
internal/config      persisted configuration
internal/update      self-update from the newest main
```

### Extending

ouro is a small kernel (`internal/kernel`) and a list of plugins (`plugins()` in `internal/cli/cli.go`).
A plugin is a named `Start` function. In it, it gets the services it needs, offers its own and contributes
items; the kernel starts every plugin after those it depends on.

```go
func shoutPlugin() kernel.Plugin {
	return kernel.Plugin{Name: "shout", Start: func(ctx *kernel.Context) error {
		term, err := kernel.Use[*console](ctx) // a service of another plugin
		if err != nil {
			return err
		}
		kernel.Add(ctx, command{"shout", "TEXT", "print TEXT in capitals", func(_ context.Context, args []string) error {
			fmt.Fprintln(term.out, strings.ToUpper(strings.Join(args, " ")))
			return nil
		}})
		return nil
	}}
}
```

| To add | Contribute with `kernel.Add` | Example |
|---|---|---|
| a command | a `command` | `talkPlugin` in `internal/cli/talk.go` |
| a tool for the agents | an `agent.Tool` | `peersPlugin` in `internal/cli/peers.go` |
| an A2A method for connected peers | an `a2a.Method` | `Method` in `internal/a2a/server.go` |
| a way to find nodes | a `discovery.PeerSource` | `tailscalePlugin` in `internal/cli/discover.go` |
| another front end | a command that runs it | `tuiPlugin` in `internal/cli/tui.go` |
| another LLM API | provide an `llm` instead of `openaiPlugin` | `internal/cli/chat.go` |

Not plugins, and not replaceable by one: identity, request signatures, trust and contacts, the built-in A2A
methods, the wire format, update and start/stop. A service can be provided once; a second plugin that
provides the same type stops ouro from starting.

Plugins are compiled in. There is no loading at runtime.

```sh
make test    # vet + tests with race detector
make cover   # coverage including the end-to-end test
```

## License

Ouroboros is licensed under the [Apache License 2.0](LICENSE) (`Apache-2.0`); the copyright notice is in
[NOTICE](NOTICE). The binary links third-party modules under the MIT and BSD-3-Clause licenses, listed with
their license texts in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES). Pass all three files on with the binary.

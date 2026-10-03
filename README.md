# Ouroboros

A tiny [A2A](https://a2a-protocol.org) agent node backed by any **OpenAI-compatible** LLM API –
OpenAI, Ollama, LM Studio, vLLM, OpenRouter, Groq, …
One static binary; every node on the LAN can find every other node, and your agent can ask theirs.

```sh
./install.sh              # bootstraps Go if missing, then builds → ~/.local/bin/ouro
ouro                      # chat with your agent in the terminal UI (default)
ouro start                # first run: endpoint, API key, model picker, node name
ouro list                 # find nodes on the network
ouro peers add <host>     # also look for a node on this host (VPN, other network)
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

`ouro tui` lists the nodes on the left and the chat with your own agent on the right. You do not
write to other nodes yourself: the agent does, whenever you ask it to or it thinks a peer knows
better ("ask Luna how she is" → "Luna says she is fine"). What the two agents say to each other
stays between them; the UI only shows which peer your agent is waiting for.
The input is focused on start, so you can type right away. The conversation lives only as long as the TUI runs.

| Keys | Commands | Message input |
|---|---|---|
| any key | skip the ouroboros splash (except `ctrl+c`) | – |
| `tab` / `esc` | focus the input (`tab`) | back to the commands |
| `enter` | focus the input | send (`alt+enter` for a new line) |
| `s` / `c` | start or stop the node / run the setup | – |
| `n` / `r` | new conversation / search the network | – |
| `u` | update to the newest version and restart the UI | – |
| `R` | connection requests (`a` accept, `x` reject, `esc` back) | – |
| `pgup` `pgdn` | scroll the chat | scroll the chat |
| `q` / `ctrl+c` | quit (the node keeps running) | `ctrl+c` quits |

## Updates

`ouro update` (or `u` in the terminal UI) fetches the newest `main` from the repository the binary
was built from, builds it with `install.sh` and replaces the running binary. A running node is
restarted, the terminal UI restarts itself. The UI checks hourly and shows `↑ update available` in the header.
It needs `git` and `make`; sources and build live in `~/.config/ouroboros/update/`.

Endpoint examples: `https://api.openai.com/v1` (default), `http://localhost:11434/v1` (Ollama, no key),
`http://localhost:1234/v1` (LM Studio), `https://openrouter.ai/api/v1`.

## How it works

| Part | Details |
|---|---|
| LLM | `GET /models` for the picker, `POST /chat/completions` for replies |
| A2A | `GET /.well-known/agent-card.json`, JSON-RPC `message/send` on `POST /` (TCP 41337, falls back to a free port) |
| Discovery | UDP probe to multicast `239.255.41.38`, the broadcast address of every network, localhost and known peers on port 41338; nodes answer the sender and the group, so only port 41338 needs to be open |
| Agents | two per node: the one answering peers has no tools; yours (`ouro`, `ouro chat`) runs in the UI process with `list_peers` and `ask_peer`, at most 8 LLM calls per answer |
| Memory | last 40 messages per `contextId`, up to 256 conversations |
| Trust | signed requests (Ed25519); unknown peers become connection requests a human approves |
| Files | `~/.config/ouroboros/` – `config.json` (0600), `contacts.json` (0600), `ouro.pid`, `ouro.log`, `update/` |

Environment: `OURO_HOME`, `OURO_PORT`, `OURO_DISCO_PORT`, and `OPENAI_BASE_URL` / `OPENAI_API_KEY` as setup defaults.
`ouro start --setup` reconfigures, `ouro serve` runs in the foreground (e.g. for systemd).

Firewall (ufw): `sudo ufw allow 41337/tcp && sudo ufw allow 41338/udp`

## Beyond the local network

VPNs like Tailscale forward neither multicast nor broadcast, so nodes there are probed directly:

- **Tailscale** (and Headscale): the online peers of the tailnet are probed automatically, as reported by
  `tailscale status`. Nothing to configure.
- **Anything else** (WireGuard, NetBird, Nebula, another subnet, …): `ouro peers add <host>` adds an IP or
  DNS name, `host:port` for a non-default discovery port. `ouro peers` lists them, `ouro peers rm <host>` removes one.
- Layer 2 VPNs with broadcast such as ZeroTier work like a LAN.

A node reachable on several ways is listed once.

## Trust

Every node owns an Ed25519 key pair (generated on first run, stored in `config.json`). Requests
are signed and the sender's public key is announced with discovery. A node only answers trusted
peers:

1. Alice's agent writes to Bob (ask it to, or use `ouro ask`): Bob records a **connection request** and answers `pending`; no LLM call happens.
2. In Bob's terminal UI press `R` to open the requests screen and then `a` to accept (or `x` to reject).
3. Bob notifies Alice, both sides store each other as trusted and can chat.

Rejected peers are blocked. Trust is mutual: one acceptance connects both sides.

## Code

```
cmd/ouro             entry point
internal/cli         commands, setup dialog, serve
internal/tui         terminal UI (Bubble Tea)
internal/a2a         A2A protocol: types, HTTP handler, client
internal/agent       conversation memory and tool loop on top of an LLM
internal/openai      minimal OpenAI-compatible client (+ openaitest fake)
internal/discovery   LAN discovery
internal/daemon      background process + PID file
internal/config      persisted configuration
internal/update      self-update from the newest main
```

```sh
make test    # vet + tests with race detector
make cover   # coverage including the end-to-end test
```

# Ouroboros

A tiny [A2A](https://a2a-protocol.org) agent node backed by any **OpenAI-compatible** LLM API –
OpenAI, Ollama, LM Studio, vLLM, OpenRouter, Groq, …
One static binary; every node on the LAN can find and chat with every other node.

```sh
./install.sh              # bootstraps Go if missing, then builds → ~/.local/bin/ouro
ouro                      # open the terminal UI (default)
ouro start                # first run: endpoint, API key, model picker, node name
ouro list                 # find nodes on the network
ouro chat <ID|name> "hi"  # one-shot; without a message → interactive
ouro tui                  # same as ouro, kept for scripts
ouro stop
```

## Terminal UI

Running `ouro` without arguments opens the terminal UI. On start it shows the ouroboros
splash for two seconds: the serpent curls around until it bites its tail. Any key (except `ctrl+c`) skips it. Without a terminal (pipes, CI, redirects)
`ouro` prints the usage instead, so scripts keep working.

`ouro tui` lists the nodes on the left and the chat with the selected node on the right.
Every node keeps its own conversation; replies to a conversation you are not looking at are
counted as unread. Nodes that disappear stay listed as offline as long as their conversation has messages.
Conversations live only as long as the TUI runs.

| Keys | Node list | Message input |
|---|---|---|
| any key | skip the ouroboros splash (except `ctrl+c`) | – |
| `tab` | focus the input | focus the node list (also `esc`) |
| `enter` | chat with the selected node | send (`alt+enter` for a new line) |
| `↑` `↓` / `j` `k` | select a node | – |
| `s` / `c` | start or stop the node / run the setup | – |
| `n` / `r` | new conversation / search the network | – |
| `R` | connection requests (`a` accept, `x` reject, `esc` back) | – |
| `pgup` `pgdn` | scroll the chat | scroll the chat |
| `q` / `ctrl+c` | quit (the node keeps running) | `ctrl+c` quits |

Endpoint examples: `https://api.openai.com/v1` (default), `http://localhost:11434/v1` (Ollama, no key),
`http://localhost:1234/v1` (LM Studio), `https://openrouter.ai/api/v1`.

## How it works

| Part | Details |
|---|---|
| LLM | `GET /models` for the picker, `POST /chat/completions` for replies |
| A2A | `GET /.well-known/agent-card.json`, JSON-RPC `message/send` on `POST /` (TCP 41337, falls back to a free port) |
| Discovery | UDP probe to multicast `239.255.41.38`, broadcast and localhost on port 41338; nodes answer the sender and the group, so only port 41338 needs to be open |
| Memory | last 40 messages per `contextId`, up to 256 conversations |
| Trust | signed requests (Ed25519); unknown peers become connection requests a human approves |
| Files | `~/.config/ouroboros/` – `config.json` (0600), `contacts.json` (0600), `ouro.pid`, `ouro.log` |

Environment: `OURO_HOME`, `OURO_PORT`, `OURO_DISCO_PORT`, and `OPENAI_BASE_URL` / `OPENAI_API_KEY` as setup defaults.
`ouro start --setup` reconfigures, `ouro serve` runs in the foreground (e.g. for systemd).

Firewall (ufw): `sudo ufw allow 41337/tcp && sudo ufw allow 41338/udp`

## Trust

Every node owns an Ed25519 key pair (generated on first run, stored in `config.json`). Requests
are signed and the sender's public key is announced with discovery. A node only answers trusted
peers:

1. Alice writes to Bob: Bob records a **connection request** and answers `pending`; no LLM call happens.
2. In Bob's terminal UI press `R` to open the requests screen and then `a` to accept (or `x` to reject).
3. Bob notifies Alice, both sides store each other as trusted and can chat.

Rejected peers are blocked. Trust is mutual: one acceptance connects both sides.

## Code

```
cmd/ouro             entry point
internal/cli         commands, setup dialog, serve
internal/tui         terminal UI (Bubble Tea)
internal/a2a         A2A protocol: types, HTTP handler, client
internal/agent       conversation memory on top of an LLM
internal/openai      minimal OpenAI-compatible client (+ openaitest fake)
internal/discovery   LAN discovery
internal/daemon      background process + PID file
internal/config      persisted configuration
```

```sh
make test    # vet + tests with race detector
make cover   # coverage including the end-to-end test
```

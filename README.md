# Ouroboros

A tiny [A2A](https://a2a-protocol.org) agent node backed by OpenAI.
One static binary; every node on the LAN can find and chat with every other node.

```sh
make install              # → ~/.local/bin/ouro
ouro start                # first run: API key, model picker, node name
ouro list                 # find nodes on the network
ouro chat <ID|name> "hi"  # one-shot; without a message → interactive
ouro stop
```

## How it works

| Part | Details |
|---|---|
| A2A | `GET /.well-known/agent-card.json`, JSON-RPC `message/send` on `POST /` (TCP 41337, falls back to a free port) |
| Discovery | UDP probe to multicast `239.255.41.38`, broadcast and localhost on port 41338 |
| Memory | last 40 messages per `contextId`, up to 256 conversations |
| Files | `~/.config/ouroboros/` – `config.json` (0600), `ouro.pid`, `ouro.log` |

Environment: `OURO_HOME`, `OURO_PORT`, `OURO_DISCO_PORT`, `OPENAI_BASE_URL`, `OPENAI_API_KEY`.
`ouro start --setup` reconfigures, `ouro serve` runs in the foreground (e.g. for systemd).

Firewall (ufw): `sudo ufw allow 41337/tcp && sudo ufw allow 41338/udp`

No authentication yet – anyone on the network can chat with any node.

## Code

```
cmd/ouro             entry point
internal/cli         commands, setup dialog, serve
internal/a2a         A2A protocol: types, HTTP handler, client
internal/agent       conversation memory on top of an LLM
internal/openai      minimal OpenAI client (+ openaitest fake)
internal/discovery   LAN discovery
internal/daemon      background process + PID file
internal/config      persisted configuration
```

```sh
make test    # vet + tests with race detector
make cover   # coverage including the end-to-end test
```

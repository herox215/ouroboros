<h1 align="center">
  <img src="docs/assets/ouro.png" alt="Ouro, the Ouroboros mascot" width="80" height="80" align="absmiddle">
  Ouroboros
</h1>

A personal AI agent with one continuous conversation and many tasks running behind it.
With a terminal UI, a native window, and tools for your files, shell, browser and apps.
Runs on any OpenAI-compatible LLM API or your ChatGPT plan, and connects to other Ouro nodes
over [A2A](https://a2a-protocol.org).

## Features

### One thread, many things at once

Talk to your agent in one continuous thread. Ouro keeps separate topic conversations behind it
and works on several at once — so you can discuss something new while another task is still
running. Answers come back to the same thread, without you having to manage sessions.

### Agents that talk to each other

Discover other Ouro nodes on your local network or tailnet, and let your agent ask theirs —
with both owners' approval.

### Tools that get things done

Work with files, run commands, search the web, and interact with browsers and desktop apps.

### Conversations that carry on

Resume sessions, search past conversations, and explicitly save memories across sessions.

### Parallel helpers

Hand independent tasks to helper agents and bring their results back into one answer.

### An agent that can rebuild itself

Let Ouro modify, test and rebuild its own source, with your permission.

### Your rules for access

Configure tools separately for yourself and other nodes, with protected paths and per-peer
permissions.

## Install

The node, terminal UI and native window ship in a single executable, `ouro`.

Go 1.27+ (the script fetches one if it is missing), a terminal, macOS or Linux.

```sh
git clone https://github.com/herox215/ouroboros.git && cd ouroboros
./install.sh          # builds → ~/.local/bin/ouro
ouro                  # first run asks for the LLM access, the model and the name
```

## Use

```sh
ouro                     # terminal UI
ouro start               # node in the background
ouro list                # who is on the network
ouro ask <ID|name> "hi"  # write to another node
ouro gui                 # native window
ouro update              # build and install the newest main
```

The Fyne chat places your messages on the right and assistant replies on the left.
Agent questions stay on the left; your replies to them appear on the right.
It reveals new assistant answers word by word, fading and rising
into place. Live text keeps its reveal progress when the final answer arrives;
conversation history appears immediately without replaying the animation.
Empty session and context sidebars take no space; when content appears or
disappears, the panels slide and the chat smoothly adjusts its position and width.

## Docs

The full manual — terminal UI, sessions, profiles, every tool, providers, peers, trust, the log —
is [docs/manual.md](docs/manual.md). Desktop and Waybar: [integrations](integrations/README.md).
Behind an API gateway: [GATEWAYS.md](GATEWAYS.md).

## License

[Apache-2.0](LICENSE), copyright in [NOTICE](NOTICE), third-party modules and their licenses in
[THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES).

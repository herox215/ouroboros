# Ouroboros

A tiny [A2A](https://a2a-protocol.org) agent node backed by any **OpenAI-compatible** LLM API –
OpenAI, Ollama, LM Studio, vLLM, OpenRouter, Groq, … – or by your **ChatGPT plan**, signed in with ChatGPT.
A node can have several of them side by side, see [Providers and accesses](#providers-and-accesses).
One static binary; every node on the LAN can find every other node, and your agent can ask theirs.

```sh
./install.sh              # bootstraps Go if missing, then builds → ~/.local/bin/ouro
ouro                      # chat with your agent in the terminal UI (default); the first run sets the node up
ouro start                # the same setup on the terminal on the first run, then the node in the background
ouro list                 # find nodes on the network
ouro chat "ask beta …"    # one answer from your agent, for scripts; without a message → the terminal UI
ouro ask <ID|name> "hi"   # write to a node directly; without a message → interactive
ouro tui                  # same as ouro, kept for scripts
ouro stop
ouro update               # build and install the newest main
```

## Terminal UI

Running `ouro` without arguments opens the terminal UI. On start it shows the ouroboros
splash for about three seconds: stars drift by, the serpent curls around in a gradient until it bites its
tail with a burst of sparks, a glow runs along its body, and the name decodes itself letter by letter. On a light
background it takes darker colors. Any key (except `ctrl+c`) skips it. Without a terminal (pipes, CI, redirects)
`ouro` prints the usage instead, so scripts keep working.

On the first run there is no node to chat with yet, and the UI opens with the setup instead: the access to
the LLM, the model, the name of the node and what your agent may do. For the access you pick a provider, and
the provider asks what it needs: the one for OpenAI-compatible APIs the endpoint and the API key, the one for
ChatGPT opens the page to sign in on. Behind the dialog
the snake lies uncurled on the starry sky, and with every page it curls up a little more. Once the setup is
saved, its tail reaches its mouth, it turns once and shrinks into the logo, and the splash takes over where
the jaws snap shut. `esc` goes back a page, and on the first page it quits; any key skips the end. The key is
never shown, and a key that is saved is only offered for the endpoint it was entered for. `/setup` opens the same
setup later with what the node has: it starts with the accesses of the node, to pick one, add one, sign in to
one again, rename or remove one. There `esc` on the first page goes back to the chat. The node keeps
running while you set it up and is restarted with what the setup saved.

The UI is the chat with your own agent. You do not
write to other nodes yourself: the agent does, whenever you ask it to or it thinks a peer knows
better ("ask Luna how she is" → "Luna says she is fine"). What the two agents say to each other
stays between them; the UI only shows which peer your agent is waiting for.
The whole **loop** of an answer is in the history, not only its end: what the agent says along with
the tools it asks for, and below those words one muted line per tool call (`· asking beta ✓ 1.2s`,
`· writing notes.md ✓ 3ms`, a failed one with `✗` and its reason). A call the agent said nothing of
is only that line. Lines of calls whose tool is still running are marked with `…`. Once the answer is in,
the calls of all its rounds are summed up in one line below the last words of the agent:
`↳ 4 tool calls, 1 failed: reading a.go, asking beta ×2, writing c.go`.
What the agent says is markdown, as the models write it, and the chat shows it formatted: headings, bold,
italic, code in a line and in blocks, lists, quotes, tables and links, in the colors of the terminal theme and
without the marks. What you write is shown as you wrote it.
The UI takes the mouse for the wheel, so the terminal does not select text itself. The chat does: press on the
history and drag, and once you release, the text selected is on the clipboard, without its colors. A press
alone marks nothing, so a click leaves no mark. It is put
there through the terminal (OSC 52, which also works over SSH) and on the clipboard of the system. A key drops
the selection.
Next to the name of your agent, the chat shows how long the context of the conversation is:
`lucagent  · context 45.2 KiB ≈ 11.6k tokens`. That is what the last call of the LLM sent, the prompt, the
tools and the conversation so far as JSON; the tokens are an estimate at 4 bytes a token. It grows with
every step of an answer, since each sends the whole conversation again, and is updated whenever the loop
shows something new. A new conversation (`/new`) has none until its first call, and neither has one that
goes on after a restart. Once the API tells how many of the tokens it was sent it had cached, the chat shows
that too: `· cached 92%, session 84%`, of the last call and of all calls in the session since the node started.
A call that goes on from the one before should find most of its tokens cached; a low rate costs time and money.
`/context` shows what the context is: the system prompt, every tool with its schema
and the conversation so far, message by message with its size.
The input is focused on start, so you can type right away. The conversations are **sessions of the node**, not
of the UI: the node runs their answers and keeps them, also when it starts anew, see [Sessions](#sessions). What your
agent did for them is recorded in [`ouro.log`](#the-log), and the conversation itself in the
[history](#history), where your agent can look it up again.

The UI is a plugin like any other (`internal/plugins/tui`), and it is the front end of the standard ouro: the
plugin says so itself, nothing else names it. An ouro built without it prints the usage when it is started
without a command, and `ouro chat` talks line by line. If the list of plugins holds more than one front end,
ouro does not pick one: `ouro` and `ouro chat` fail and name the commands that open them (`ouro tui`, …),
which keep working.

Other plugins add to the UI, see [Extending](#extending):

- a **screen** next to the chat, opened with its command (`/greetings`) and left with `esc`. It may offer
  actions below its text: `←` `→` select one, `enter` runs it.
- a **command** (`/greet`), which runs something and reports in the footer. `/help` lists what the plugins added.
- what is shown of a **tool call** in the loop of an answer. `asking beta` is what the plugin of
  `ask_peer` shows; a tool without such a line shows as nothing, and the agent as thinking. With
  `Detail` the plugin can add what became of the call, which the UI puts after that line once the
  call ended (`assistant.HowItEnded` gives `✓ 1.2s`, or `✗` with the reason).

A command has one meaning. The UI keeps its own commands (the table below), and of two plugins that claim the
same name the one that was started first gets it, a screen before a plain command. What loses its name is left out, and the UI says so in red in its
footer when it opens, until the first key is pressed: `/stop is taken by the terminal UI: the screen "Stats"
is left out`. The UI opens all the same, and so does everything else of that plugin.

While the UI is open your node is online: opening it starts the node, and closing it stops the node
again unless it was already running before (`ouro start`). An answer that is on its way when you close the UI
is given all the same: the node stops once no answer is on its way any more.

Everything the UI does is a **command**: you type it into the message input with a slash in front and send
it with `enter`. A command runs in the UI and never goes to your agent, so only you run commands; a command
the UI does not know stays in the input, to be corrected. While you type one, a list above the input offers those that fit:
`↑` `↓` select one, `tab` completes the input to it and `enter` runs it.
A message that starts with a slash takes two: `//etc` sends `/etc`.

| Command | What it does |
|---|---|
| `/new` | new conversation; the one shown goes on in the background |
| `/session` | the conversations of the node, the latest first, the one shown marked: `↑` `↓` select, `enter` shows the one selected, `d` twice deletes it, `tab` switches to the conversations of peers, `esc` back; see [Sessions](#sessions) |
| `/cancel` | stop the answer that is on its way in the conversation shown |
| `/start` / `/stop` | start or stop the node (`/start` runs the setup of a node that has none) |
| `/setup` | run the setup; for a node that is set up it starts with its accesses: `enter` uses the one selected, `l` signs in to it again, `r` renames it, `d` removes it, the last row adds another one, see [Providers and accesses](#providers-and-accesses) |
| `/context` | the next context, or the last actual prepared request with extensions, message by message: `↑` `↓` `pgup` `pgdn` `home` `end` scroll, `esc` back |
| `/settings` | the configuration in one place, by categories: the node, the agents, the plugins, the tools of your agent and of peers, the protected paths. `↑` `↓` select, `enter` opens a category, `←` `→` what to do with a line of it, `enter` does it, `esc` back; see [Settings](#settings) |
| `/model` | the models of every access of the node, grouped by access, the one in use marked: `↑` `↓` select, `enter` saves it (the agents answer with it from the next message on, also when it is a model of another provider), `esc` back |
| `/pending` | what waits for your decision: connection requests of peers, connections your agent proposes, and [changes of the configuration](#configuration) it proposes. `↑` `↓` select an entry, `←` `→` accept or reject, `enter` decides, `esc` back |
| `/help` | list the commands, those of the plugins too |
| `/quit` | quit |
| commands of plugins | open a screen (`esc` back) or run what the plugin added |

Keys only move around:

| Key | What it does |
|---|---|
| any key | skip the ouroboros splash (except `ctrl+c`) |
| `enter` / `alt+enter` | send / a new line |
| `ctrl+s` | the conversations, like `/session`, but what is typed stays with the conversation shown |
| `pgup` `pgdn` | scroll the chat |
| `esc` | back from a screen to the chat |
| `ctrl+c` | quit, everywhere |

### Sessions

A conversation with your agent is a **session**, and the sessions belong to the node: the node runs the answers
and keeps what happens in each of them, the messages, what the agent says on the way, every call of a tool and
the answer. The UI and `ouro chat` are front ends of it. They talk to the node over a socket of the file
system, `ouro.sock` in the directory of the node, and show what the node tells them. Nothing of it leaves the
host: there is no port for it, and only you can open the socket (it is `0600`, its directory `0700`). Where the
path of the directory is too long for a socket (more than about 100 characters), the socket is in a directory
of yours in the temporary directory of the system, `ouro-UID/`, named after the directory of the node; the node
refuses one that is not yours. A session of the socket is always yours: no peer reaches it, and the tools only
you may use, like `rebuild`, work there.

So an answer does not depend on the UI:

- **Closing the UI** while your agent works does not stop it. The answer is given in the node, and it is in the
  log and in the history. Open the UI again and the conversation is there, the answer too. A node the UI
  started stops once no answer is on its way.
- **Several conversations at once.** `/new` starts a new conversation and leaves the one before going on.
  `/session` (or `ctrl+s`) lists the conversations of the node: each with its title and when something last
  happened in it, and behind those whose answer is on its way a small animation. The
  one shown is marked. `enter` shows the one selected, with an answer on its way as usual. When an answer
  comes in in a conversation that is not shown, the footer says so (`answered in “…” · /session`). What you
  typed stays with the conversation you typed it in; `ctrl+s` takes you to the list without clearing it.
- **Titles.** A conversation is named for its topic. With each of your first three messages, while the
  answer runs, the model of the node is asked for a title of a few words from what you and the agent said
  so far, without the calls of tools. A greeting alone has no topic yet: then it is asked again with the
  next message. Until there is a title, the start of your first message names the conversation. The title
  is shown at once and kept in the archive; the call is part of no conversation and not in the log. An
  answer that is in before the title waits for it, 20 seconds at most.
- **Two front ends** see the same: the UI lists the conversations `ouro chat` had too.
- `/cancel` stops the answer on its way in the conversation shown. Stopping the node stops every answer.

The node **keeps the sessions** in its [archive](#history), so that they outlive it: after `ouro stop`, a
crash, a reboot, `ouro update` or a build of `rebuild`, `/session` lists them as before, and you go on with
one where you left it.

- **One place.** Every answer is archived once it ended, answered, failed or stopped, in
  `history/owner/<session>.jsonl`. The node does so with or without the `history` plugin, which only gives
  your agent the tools to search it. A crash costs at most the answer on its way.
- **Loaded when needed.** A session from before the node started is loaded once it is shown, asked something
  or its context looked at: what happened in it, and what your agent remembers of it, cut like any
  conversation (see Memory in [How it works](#how-it-works)), with an answer that failed or was stopped
  closed as it was. The list reads only its first message and when it was last used. What only the UI was
  told is not archived: a session loaded shows a script as one call of `run`, not the calls within it, and
  an answer that failed without why.
- **What the agent is told.** With the first message after the node started anew, the agent is told so, and
  how long ago the last answer was: what tools gave back before may be outdated, and the variables of its
  [scripts](#scripts) are gone. You do not see that note.
- **Nothing is deleted by itself.** `d` pressed twice on `/session` deletes the session selected, unless an
  answer is on its way there: from the archive, and what your agent remembers of it.
- **The conversations of peers.** `tab` on `/session` lists how peers talked with your agent, each with the
  name of the peer (as your contacts name it) and its first message. `enter` shows one, only to read: you
  cannot write there. `d` pressed twice deletes it, and your agent forgets it. They start anew for the peer
  after a restart; only yours go on.

## Updates

`ouro update` fetches the newest `main` from the repository the binary
was built from, builds it with `install.sh` and replaces the running binary. A running node is
restarted: an answer on its way is stopped; the [sessions](#sessions) are kept.
It needs `git` and `make`; sources and build live in `~/.config/ouroboros/update/`.

Your agent has the tool `rebuild` for the other direction: it builds the checkout the binary was built from
(`make install` records it) with its files as they are now, committed or not, and runs `make test` there. Only
when the build and every test pass, the binary is replaced; otherwise nothing is installed and the agent gets
the end of the output. That is how a change your agent made to the plugins of ouro
with its tools for files takes effect. The tool runs in the node, which cannot stop itself while the answer is
on its way. So the node starts anew with the new build once the agent has answered and no other answer is on
its way; it takes no new message until then. It hands over to the new build in the same process: the lock on
`ouro.pid` and the socket stay open across it, so no other node starts in between, and a front end that
connects meanwhile waits until the new node answers. The new node finds the [sessions](#sessions) in the archive,
and what the agent remembers of them. The terminal UI starts anew as
well, on the same terminal and without the splash, and shows the same conversation. The tool is for you only:
a peer is refused even if `a2a` lists it. Take
`rebuild` out of `h2a` if your agent should not build what it wrote. A binary installed by `ouro update` keeps
the checkout of the one it replaced.

**`rebuild` builds in a sandbox.** What the agent wrote runs when the tests run, so the build has two steps:

1. **Download, with network, by the frame.** `go mod download` fetches exactly the modules `go.mod` and
   `go.sum` name, and Go checks every one against `go.sum`. Nothing of the checkout runs in this step.
2. **Build and test, without network, in the sandbox.** `make install` and `make test` run there with the
   modules from step 1. They may listen and connect on the loopback interface (the tests start nodes there)
   and nothing beyond it, they cannot connect to a socket of the file system (such as that of Docker), and they
   write only to a temporary directory below `update/`, to `update/stage` and to the build cache of Go. The
   binary is built in the temporary directory, not in the checkout. Everything they start is in the same
   sandbox.

Both steps run the Go that `rebuild` finds first in `PATH`, in `OURO_GO_DIR` or `~/.local/go`, or in
`/usr/local/go`, in an environment the frame fixes: `GOFLAGS=-mod=readonly`, `GOENV=off` (no file of
`go env`), `GOWORK=off` (no `go.work`, also not above the checkout), `GOTOOLCHAIN=local` (no other
toolchain; the Go found has to be as new as `go.mod` asks), `GOVCS=*:off` (modules only from the proxy, no
`git`) and no `GOROOT`; in step 2 also `GOPROXY=off`. `GOPROXY`, `GOPRIVATE` and the like of the environment
ouro was started in hold for step 1: they are yours.

| System | Sandbox |
|---|---|
| macOS | `sandbox-exec` with a profile that refuses the network beyond localhost, sockets of the file system and writing elsewhere |
| Linux | [bubblewrap](https://github.com/containers/bubblewrap) (`bwrap` in `/usr/bin` or `/bin`, the package `bubblewrap`): the file system read only but for the three directories, a `/dev` of its own, a network namespace of its own with the loopback interface alone, and a filter of seccomp that refuses sockets of the family `AF_UNIX` and `io_uring` |

On Linux, bubblewrap is the one way that works where unprivileged user namespaces are confined, as on Ubuntu
24.04 and later, where its profile of AppArmor allows them; Debian, Fedora and Arch allow them anyway.
Landlock (Linux 5.13 and later) is no sandbox for this on its own: it limits TCP by port, not by address, so
the loopback interface cannot be told from the network, and it does not limit connecting to a socket of the
file system.

**Without a sandbox** (bubblewrap is missing or cannot run), `rebuild` builds nothing and tells the agent
why. You can let it build without a sandbox, with network, with `"rebuild_without_sandbox": true`; this
holds wherever there is a sandbox too. With `"rebuild_without_tests": true` it installs a build once it
builds, without running `make test`. Both are switches of the agents in `/settings`, and the output of a
build starts with a warning for each that is on. Your agent sees them with `get_config` and may propose
them (say "let rebuild run without the tests and without a sandbox"), but a proposal changes nothing until
you accept it in `/pending`. The tools for files never write `config.json`.

When something fails in the sandbox and its output says that something was not permitted, that a file system
is read only or that the network is unreachable, the agent is told that the sandbox refused it, and that
trying again does not help.

Sandboxes do not nest: in a process that runs in one, `rebuild` finds none. The tests of ouro that need one
are skipped there, so `make test` passes in the sandbox too.

Endpoint examples: `https://api.openai.com/v1` (default), `http://localhost:11434/v1` (Ollama, no key),
`http://localhost:1234/v1` (LM Studio), `https://openrouter.ai/api/v1`.

## How it works

| Part | Details |
|---|---|
| LLM | an OpenAI-compatible API: `GET /models` for the picker, `POST /chat/completions` for replies. The ChatGPT plan: `GET /v1/models` and `POST /v1/responses` at `api.openai.com`, with the token of the login. See [Providers and accesses](#providers-and-accesses) |
| A2A | `GET /.well-known/agent-card.json`, JSON-RPC `message/send` on `POST /`, over HTTPS only (TLS 1.3 on TCP 41337, falls back to a free port) |
| Channels | JSON-RPC `channel/open` on `POST /` with `Upgrade: ouro-channel/1`: the node answers `101 Switching Protocols` and the TLS connection of the request carries messages in both directions until one side closes it, see [Channels](#channels). Not part of A2A |
| Discovery | UDP probe to multicast `239.255.41.38`, the broadcast address of every network, localhost and the peers of the tailnet on port 41338; nodes answer the sender and the group, so only port 41338 needs to be open. Where no interface carries multicast (a network namespace with the loopback interface alone), a node hears the probes sent to its host; a datagram the system does not hand over, as a sandbox does with one of another host, is passed over |
| Agents | two per node, built from one blueprint, both in the node: yours answers the [sessions](#sessions) of `ouro` and `ouro chat`, the other one peers. Which tools each may use is set in `config.json`, see [Tools](#tools); at most 100 LLM calls per answer, from the 81st on with a note to the LLM of how many it has left; the LLM has one tool, `run`, and uses the others in a [script](#scripts) |
| Memory | a conversation is cut in steps: beyond 80 messages or 256 KiB it is cut to the last 40 messages and 128 KiB, at a message of the owner, so that its start stays the same between the cuts and the provider can cache it; always the whole of the last answer, however many tool calls it took; up to 256 conversations. A result of a tool leaves out runs of lines (1 KiB or more) that an earlier result of the same answer has, with a line that says so. Once the results of an answer have more than 64 KiB, the LLM is told so with every call, in a message of its own after the results, which stay as they are and as the provider cached them, and a script hands on at most 4 KiB of what it prints and of what it returns. From the 16th call of the LLM in an answer on, it is told with every call to stop searching and ask the user what exactly is meant, unless it has found what it looks for; the same call of a tool asked for a third time in a row is not run, and the LLM is told so. What the LLM thought is sent back within an answer; once it is at least half of what came after its start, it is left out when the answer ends, and always at a cut. ouro asks the API for the tokens of every call (`stream_options`) and gives every API the conversation as `prompt_cache_key`, so that a gateway sends its calls to the same cache. A Claude model (its name has `claude`) caches only up to `cache_control` breakpoints: ouro sets them at the system prompt and at the last 3 messages with content. An API that refuses one of these parameters (HTTP 400 that names it) is asked again without it, and not sent it again. An answer that fails or is stopped is kept as far as it got, so that asked again the agent goes on from there; why it failed is not kept; a conversation of a peer is its `contextId` together with its key, so no peer continues or reads the conversation of another. What is cut off is not lost: the [history](#history) keeps every conversation in full |
| Card | the description in the agent card is what you wrote in `/settings` (`description` in `config.json`, one line of at most 300 characters), or `Ouroboros instance named <name>` without one; anyone can read it, so nothing private belongs there. `list_peers` shows your agent the cards of connected peers. `capabilities.streaming` is `false`: in A2A it stands for `message/stream` (the answer to a message as server-sent events), which a node does not have. That peers can open channels is listed under `capabilities.extensions`; which channels is not, as anyone can read the card |
| Trust | nodes talk over TLS 1.3 with a self-signed certificate made from the node's Ed25519 key; the connection is pinned to the key the node is known by, and requests are signed for that key; a message only goes to a peer both owners agreed to connect to, and a node asks another to connect with `contact/request`, which carries no message |
| LLM | answers are asked for as a stream (`"stream": true`), so a gateway that gives up on a request without data for a while (HTTP 504) keeps waiting while the model thinks; an API that does not stream answers in one piece, which is taken as well. A completion is given up after 5 minutes without data, a list of the models after 5 minutes |
| Limits | at most 64 connection requests wait for a decision: when the list is full, the one that waited longest makes room for a new one (trusted peers are never dropped). What your agent proposes waits in the same list but never takes the place of a request: with 64 waiting it is not recorded. At most 16 changes of the configuration your agent proposed wait for a decision. The tools for files and a script have limits of their own, see [Files](#files) and [Scripts](#scripts). One address gets 10 messages per minute from nodes that are not connected; further ones are refused with error `-32005` and leave no request (at most 1024 addresses are counted per minute, unknown nodes from any further address are refused). Against replays the node remembers every nonce for at least 4 minutes, at most 16384 per 4 minutes; beyond that it refuses requests rather than forget a nonce early. A connected peer has at most 8 channels open to a node at a time, a further one is refused with error `-32006`; a channel without a message in either direction for 5 minutes is closed; a message of a channel has at most 1 MiB; the channels of a peer that was removed from `contacts.json` or refused are closed within a second, and all channels when the node stops |
| Errors | a peer whose message your agent failed to answer is only told `the agent could not answer (error <ID>)`; the cause is in `ouro.log` under that ID, and only you (`ouro ask <your node>`) get to read it directly. The peer can ask your agent about the ID later, see [When a peer could not answer](#when-a-peer-could-not-answer) |
| Files | `~/.config/ouroboros/` – `config.json` (0600), `credentials.json` (0600, what the node signs in to its LLMs with, see [Providers and accesses](#providers-and-accesses)), `models.json` (0600, the models the accesses last listed), `host-id` (0600, an opaque ID of this installation, made on first use), `contacts.json` (0600), `proposals.json` (0600), `failures.json` (0600), `ouro.pid`, `ouro.sock` (0600, see [Sessions](#sessions)), `ouro.log` (0600) with `ouro.log.1` (0600) and `ouro.log.lock`, `update/`, and `history/` (0700, see [History](#history) and [Sessions](#sessions)). `config.json`, `credentials.json`, `contacts.json` and `proposals.json` are replaced in one step (temporary file, then rename), so a crash never leaves half a file; changes are serialized by a lock on `config.json.lock` / `credentials.json.lock` / `contacts.json.lock` / `proposals.json.lock`. `ouro.log` holds what the node does and [what your agent does for you](#the-log); at 10 MiB it moves to `ouro.log.1`, see there |
| Process | the node holds a lock on `ouro.pid` (`PID PORT`) while it runs. Only a locked file counts as a running node, so one left behind by a crash or reboot is ignored and never gets a foreign process signalled |

Environment: `OURO_HOME`, `OURO_PORT`, `OURO_DISCO_PORT`, and `OPENAI_BASE_URL` / `OPENAI_API_KEY` as what the
provider for OpenAI-compatible APIs offers in the setup.
`ouro start --setup` reconfigures, `ouro serve` runs in the foreground (e.g. for systemd).

Firewall (ufw): `sudo ufw allow 41337/tcp && sudo ufw allow 41338/udp`

## Tools

The agent has tools for peers (`list_peers`, `ask_peer`), for files (`read`, `write`, `edit`, `list`,
`delete`, `search`), for the [history](#history) of its conversations (`history_search`, `history_read`,
`history_sessions`) and, for you alone, three for the configuration (`get_config`, `propose_config`,
`withdraw_proposal`), one that runs commands ([`bash`](#commands)) and one that builds and installs its own
source ([`rebuild`](#updates)). It uses them in **scripts**: the LLM is
offered one tool, `run`, and every tool the agent may use is a function in it, see [Scripts](#scripts).
Which of them it may use depends on who it is talking to, and is set in `config.json`. This is what the
setup writes:

```json
"tools": {
  "h2a": ["*"],
  "a2a": [],
  "protected": [
    "~/.ssh", "~/.gnupg", "~/.aws", "~/.kube", "~/.config/gcloud",
    "~/.docker/config.json", "~/.netrc", "~/.password-store", "~/.local/share/keyrings"
  ]
}
```

| List | Applies when | Without it |
|---|---|---|
| `h2a` (human to agent) | you ask your agent | every tool |
| `a2a` (agent to agent) | a connected peer asks your agent | no tool, and no scripts (see [Scripts](#scripts)) |
| `protected` | a tool for files is used, by anybody | the list above; `[]` protects nothing |

An entry of `h2a` and `a2a` is the name of a tool, or an object with the name as `"tool"` and **settings** for
that tool next to it. `"*"` stands for every tool, `[]` for none; an object next to `"*"` narrows that one tool.
A name that is not a tool, a tool that is listed twice, a setting the tool does not have and a setting of the
wrong type are errors: the node does not start, the chat does not answer. A change holds at once: your agent
and the node read `config.json` with every message and build their agents anew when it changed, without a
restart. A node that runs keeps answering peers as before while the file cannot be used, and says why in
`ouro.log`.

### Files

| Tool | Does | Limits |
|---|---|---|
| `read(path, offset?, limit?)` | returns the text of a file; the whole file comes as it is, a part with a note that says which lines of how many it is and, when there is more, at which line to continue | 2000 lines or 256 KiB per call, a line has less than 256 KiB; what is not text (UTF-8 without NUL) is refused |
| `write(path, content)` | creates the file or replaces all of it, in one step, and creates missing directories | 1 MiB |
| `edit(path, old, new)` | replaces `old` by `new`; `old` has to match exactly, and exactly once; returns the changed lines with 3 lines around them, at most 50 | the file has at most 1 MiB, before and after |
| `list(path)` | the entries of a directory: directories end in `/`, files come with their size | 1000 entries; what the tool may not read is not listed |
| `delete(path, recursive?)` | removes a file or an empty directory; one with content only with `recursive: true` | stops, with nothing deleted, before anything it may not delete |
| `search(path, pattern, glob?)` | the lines that match a regular expression ([RE2](https://github.com/google/re2/wiki/Syntax), `(?i)` ignores case) as `path:line: text`, in a file or in every file below a directory; `glob` keeps to file names like `*.go` | 200 lines of at most 300 bytes, 10000 files, 100000 files and directories looked at (also those the glob leaves out); hidden directories below the path, files of more than 1 MiB, what is not text and what the tool may not read are left out |

A path is absolute or starts with `~`, your home directory. Each of the six tools takes the settings `allow`
and `deny`: paths the tool may go to, and paths within those it may not. A path is a file, or a directory with
everything below it; there are no patterns. A shared directory for peers, and your own agent held back:

```json
"tools": {
  "h2a": [
    "*",
    {"tool": "read",   "deny":  ["~/Documents/tax"]},
    {"tool": "delete", "allow": ["~/Projects/scratch"]}
  ],
  "a2a": [
    "list_peers",
    {"tool": "read",  "allow": ["~/share"]},
    {"tool": "list",  "allow": ["~/share"]},
    {"tool": "write", "allow": ["~/share/inbox"], "deny": ["~/share/inbox/keep"]}
  ],
  "protected": ["~/.ssh", "~/.gnupg"]
}
```

Three things limit a tool for files, and the first that speaks wins:

1. **The core**, fixed in the code (`internal/guard`) and not configurable:
   - `config.json`, `credentials.json` and their lock files are neither read nor changed, under any name
     (also not through a hard link): they hold the key of your node and what it signs in to its LLMs with,
     the keys of the APIs and the tokens of a login, which would end up with the LLM.
   - Everything else in the directory of the node (`contacts.json`, `proposals.json`, `failures.json`,
     `ouro.pid`, `ouro.sock`, `ouro.log`, `ouro.log.1`, `update/`, `history/`) is read and
     never changed.
   - The source of the core is read and never changed: in every directory whose `go.mod` names the module
     `ouroboros`, the packages `internal/identity`, `a2a`, `contacts`, `config`, `home`, `update`, `sandbox`,
     `safefile`, `peers` and `guard`. They hold the keys and the secrets, decide whom the node trusts and keep
     the rules of the tools and of `rebuild`. The plugins and the other packages in the same checkout stay
     writable.
   - What decides how a checkout is built, what the build fetches and runs, and where the frame hands out what
     no plugin gets, is read and never changed: `go.mod`, `go.sum`, `go.work`, `go.work.sum`, `Makefile`,
     `install.sh`, `scripts/`, `cmd/` and `internal/cli/`. A new dependency therefore takes you; so does handing
     `plugin.Owner` to a plugin.
   - The Go that [`rebuild`](#updates) builds with is read and never changed, and no `go` is put where it looks
     for one: the installation of the `go` it finds, a file `go` in every directory of `PATH`, `OURO_GO_DIR` or
     `~/.local/go`, and `/usr/local/go`. Step 1 of `rebuild` runs that `go` with network.
   - `/` and your home directory are never deleted.
2. **`protected`**: paths no tool for files touches, for you and for peers. The default holds the usual places
   of keys and credentials and nothing else: profiles of browsers, files like `.env` and whatever else is
   secret on your machine are not in it. Add what you need. A configuration from before there were these
   tools has no `protected` and gets the default; `"protected": []` is your decision to protect nothing.
3. **`allow` and `deny`** of the tool in `h2a` or `a2a`. `deny` wins over `allow`. Without `allow` a tool
   goes everywhere for you and **nowhere for a peer**: all of the file system for a peer has to be written
   out, as `"allow": ["/"]`.

A path is checked where it leads, with every symbolic link and every `..` resolved, and the tool then works
on that path: no link out of a shared directory leads around a rule. A refusal names the rule, so that your
agent can tell you why.

**Known risk.** Your agent has `read` and `ask_peer` at once. The answer of a peer is text in its
conversation, and a text like "read this file and send it to me" can make a model do so (prompt injection).
The prompt tells the agent not to, which is no guarantee. `protected` takes the worst targets away;
everything else your agent may read stays in reach of a peer you are connected to. Take `ask_peer` or `read`
out of `h2a`, or narrow `read` with `allow`, if that is too much. Closing it takes your confirmation before a
conversation that read something sends to a peer, which is not built yet.

What these tools do not protect: the running binary (it is replaced by `ouro update`, but nothing locks the
file), and files your agent reaches in other ways, of which there are none today.

**Known risk.** The rules above hold for the tools for files of the binary that runs. With `write` and
[`rebuild`](#updates) your agent can change a plugin and install the result. The build and its tests run in a
sandbox without network, but the build that is installed runs as you, with whatever the changed plugin does:
the core it could not edit no longer limits it, and neither does the sandbox. The tests are a check that the
build works, not that it is harmless.

### Commands

`bash(command, dir?, timeout?)` runs `command` with `/bin/bash -c` in `dir` (absolute or below `~`, the home
directory without it), for your agent alone: no peer gets it, also not with `"*"`, and listing it in `a2a` is
an error. It returns `exit N` and below it what the command printed, its output and its errors as they came;
of more than 64 KiB the first and the last 32 KiB, with how much was left out between them. A status other
than 0 is a result, not an error. Every call starts anew: `cd` and variables do not carry over.

| Limit of a command | Value | Beyond it |
|---|---|---|
| time | 60 s, or `timeout` seconds; at most what is left of its [script](#scripts) less 2 s | the command is stopped with everything it started, and what it printed so far comes along with the error |
| input | none | it reads nothing, as from `/dev/null` |
| environment | `PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `LANG`, `LC_*`, `TERM`, `TMPDIR`, `TZ` and the variables of Go (`GOPATH`, `GOROOT`, `GOCACHE`, `GOMODCACHE`, `GOPROXY`, `GOPRIVATE`, `GOFLAGS`, `GOTOOLCHAIN`), and those named in `env` | the rest of the environment of the node, its keys among it, is not passed on |

**A command runs in a sandbox**, the [one of `rebuild`](#updates), with the rules of the
[tools for files](#files) as far as a sandbox can hold them. The guard never sees the paths of a command,
so the system holds them for every process it starts:

- The secrets of the node, what is `protected` and what `deny` of `bash` names are neither read nor written.
  On Linux a file of them is covered by an empty one, a directory by an empty one in memory.
- The directory of the node, the Go of `rebuild` and, in the checkout ouro was built from, the source of the
  core and what decides how it is built are read and never written, and what of them is missing is not
  created. A symbolic link among them counts as itself and as where it leads.
- The rest is writable, or with `allow` only what it names. Reading is not limited beyond the first point.
- No other host, unless `network` is set: the loopback interface alone, as for `rebuild`. With network,
  still no socket of the file system, such as that of Docker; on macOS but the one that resolves names.

What the sandbox refused does not pass when tried again; the result says so when the output says that
something was not permitted, on a read-only file system or without network.

```json
"h2a": ["*", {"tool": "bash", "allow": ["~/projects", "~/Library/Caches/go-build", "~/go"], "network": true, "env": ["GITHUB_TOKEN"]}]
```

| Setting | Means | Without it |
|---|---|---|
| `allow` | the paths a command may write, each with everything below it; one that does not exist is not writable | everywhere the rules above leave writable |
| `deny` | paths within those that a command neither reads nor writes | none |
| `network` | `true` lets a command reach other hosts | `false` |
| `env` | the names of further variables of the environment it gets | none |

The sandbox holds less than the guard of the tools for files:

- **A checkout is the one ouro was built from**, not every directory whose `go.mod` names the module. In
  another one a command may change the core; `rebuild` builds only the first.
- **`/` and your home directory** are not kept from being deleted, only what the rules keep in them.
- **On Linux a path that is missing, or a link**, cannot be covered: the directory it is in is mounted read
  only, and what is in it writable again, so nothing new is created there. In your home directory that is
  whenever a protected path such as `~/.aws` does not exist.
- A process that leaves the group of the command (`setsid`, a daemon) is not stopped with it.

**Without a sandbox.** Where there is none (bubblewrap is missing or cannot run, or ouro runs in a sandbox
already), `bash` runs nothing and tells the agent why. You, and only you, can let it run its commands without
one, also where there is one, with `"bash_without_sandbox": true` in `config.json` or in `/settings`. A
command then runs as you: with network, the secrets of the node and the source of the core in reach, and
none of the rules of the tools for files. The agent sees the setting with `get_config` and cannot propose it.

### Scripts

`run(script)` is the only tool the LLM is offered. Everything the agent does, it does in a script: it
writes JavaScript, and inside it every tool it may use is a function of its name. An answer has at most 100
calls of the LLM, every call sends the whole conversation again, and every result stays in it: a script
does in one call what would take many, and hands on only what it prints. The description of `run` lists the
functions with their parameters, and the prompt says that they are functions of `run`.

```js
// "which of my peers knows the way to the station?", in one call of the LLM
const connected = list_peers().split("\n")
  .filter(line => line.includes(", connected)"))
  .map(line => line.split(" ")[0])
for (const peer of connected) {
  try { print(peer + ": " + ask_peer({peer: peer, message: "Do you know the way to the station?"})) }
  catch (e) { print(peer + " could not answer: " + e.message) }
}
```

- What a script gives back goes to the LLM: what it printed with `print` (or `console.log`), one line per call,
  and after that the value of its last statement, a string as it is, everything else as JSON. A statement
  without a value, like `var a = 1`, gives back nothing. Of what is printed, 16 KiB are handed on and the rest
  is left out, and the LLM is told how much. All that was printed (up to 4 MiB) is then kept in the variable
  `printed`, so that the next script can look into it instead of reading again. Of a value beyond 16 KiB its
  start is handed on, and the value itself is kept in `returned`. A script that fails
  gives back its error and what it printed before.
- What a script gives back ends with a line of what the interpreter keeps for the next script, like
  `[kept for the next script: files (array of 312), walk (function), notes (string of 1532 bytes)]`: every
  name on `globalThis` that is not a standard object or a tool, at most 30 of them. A getter is not called
  for it.
- **Everything a script gives back stays in the conversation** and is sent again with every later call of
  the LLM. The description of `run` says so: keep what the tools return in variables, print counts, names
  and excerpts instead of whole files, and look at what is kept before calling a tool again.
- **The scripts of a conversation share one interpreter.** What a script declares at its top level (`let`,
  `const`, `var`, `function`, `class`) or puts on `globalThis` is there for the scripts after it, as in a REPL
  or a notebook: the agent can search, look at what it found, and go on with it in the next script instead of
  calling the same tools again. A `let` or `const` at the top level is a `var`, so a script may declare a
  name again that one before it declared.
- A script that uses `return` at its top level is the body of a function instead: what it returns is its
  value, and what it declares ends with it. What it puts on `globalThis` stays.
- A tool takes one object with its parameters and returns the text the LLM would get. A tool that fails
  throws an `Error` with the text of its error: not caught, it ends the script, and the LLM gets the error
  with the line of the script; with `try`/`catch` the script goes on.
- The functions are also in the object `tools`. A tool whose name is a word of JavaScript has to be called
  there: `tools.delete({path: "…"})`. `delete({…})` would be the operator, which does nothing, so a script
  that says so is not run and the LLM is told how to write it.
- **A script can do nothing the lists do not allow.** It sees exactly the tools this agent may use, with
  their settings: those of `h2a` for you, those of `a2a` for a peer (and `explain_error`, see
  [below](#when-a-peer-could-not-answer)). `run` is no tool to list: it is always there, and naming it in
  `h2a` or `a2a` is an error. A tool runs under the conversation the script was started in: for the same
  peer, as many hops away, so every rule a tool enforces holds inside a script. In the terminal UI and in
  the log the calls of a script show one by one (`asking beta…`, `writing notes.md…`).
- **A peer without a tool runs no script.** As long as `a2a` lists no tool, the agent that answers peers
  has no tool at all, not even `run`: a script may keep your node busy, see below. With one tool in `a2a`
  peers run scripts with it.
- Your agent always has `run`, also with an empty `h2a`: its scripts can then only compute.
- Every script gets the tools, `tools`, `print` and `console` anew, whatever the script before made of them.
  What else a script changes, like `JSON.stringify`, stays changed for the scripts of its conversation after
  it, not for the tools, which do not use what a script can reach.
- The environment is the language (ES2015 and most of what came after, interpreted by
  [goja](https://github.com/dop251/goja), which is part of the binary) with its standard objects (`JSON`,
  `Math`, `String`, `Array`, …), `print`, `console` and the tools. No network, no files, no processes, no
  `require` or `import`, no timers.
- **What is kept, and for how long.** Each conversation has an interpreter of its own: your conversations
  with your agent, and every conversation of every peer, which never sees one of another. The scripts of one
  conversation run one after the other. 16 conversations keep theirs; beyond that the one that ran a script
  the longest ago loses all its scripts kept. A script that was stopped for its memory, or that left a tool
  behind that did not end, takes its interpreter with it, and the LLM is told so. Nothing of it is written
  to disk: when the node or the terminal UI stops (also for a [restart after a rebuild](#updates)), it is gone,
  while the conversation goes on.

**Only scripts needs a model that writes JavaScript.** On the same questions with deepseek-v4.1-flash,
answers with scripts were as good as with single calls of the tools, sent about a third less to the LLM in
all and took about a fifth less time; they did best where a script cuts down what it finds (counting,
searching, the same call for many files), and worse where the agent has to read and understand text. A small
model that cannot write JavaScript can do nothing: try yours before you rely on it. The last line of every
answer in [`ouro.log`](#the-log) says what it cost.

| Limit of a script | Value | Beyond it |
|---|---|---|
| time | 600 s | the script is stopped, and so are the tools it is waiting for; a tool that does not end is waited for 2 s longer and then left behind |
| calls of tools | 64 | the script is stopped at the next call |
| size of the script | 64 KiB | it is not run |
| size of the result | 16 KiB, 4 KiB once the results of the answer have more than 64 KiB | the start is handed on, and the value is kept in `returned` |
| what it prints | 16 KiB, 4 KiB once the results of the answer have more than 64 KiB | the rest is left out, the LLM is told how much, and all of it is kept in `printed` |
| calls of functions within each other | 1024 | the script is stopped |
| memory | 256 MiB, see below | the script is stopped |

A script that is stopped cannot catch that. The limits are fixed in the code (`internal/script`), the same
for you and for peers. They hold for each script, not for all scripts of a conversation.

**The limit of memory is approximate.** The interpreter has none of its own. While a script runs, the node
looks at the heap of the whole process every 5 ms and stops the script once the heap has grown by more than
256 MiB since the script started. That catches a script that keeps collecting data. It does not catch a
single step that allocates more than that at once, like `"x".repeat(2 ** 30)`: the interpreter cannot be
stopped inside a step, and the process may run out of memory. The same holds for the time: a regular
expression that takes minutes to fail is one step. The script is then given up after 600 s and the answer goes
on, but the step keeps a processor busy until it ends. As the heap is the one of the process, two scripts
that run at the same time count against each other, and so does everything else the node allocates
meanwhile. What a conversation keeps between its scripts counts for none of them: up to 16 conversations
keep what their scripts built, and nothing limits how much that is. Whoever may run scripts on your node may
therefore slow it down or bring it down: list a tool in `a2a` only for peers you would let do that.

### History

The node archives every conversation in full: the messages of the user, what the model answers along with
the calls of tools it asks for, and the whole result of each call, as it came before the memory left
anything out. It is where your [sessions](#sessions) are kept. The plugin `history`
(`internal/plugins/history`) gives your agent the tools to search it: it finds there what the
[memory](#how-it-works) cut off, and the conversations before `/new`. What is sent to the LLM stays as it
is: the archive changes no context, and is only read when the agent calls one of its tools. Turned off, the
plugin takes the tools away, and the node archives all the same.

| Tool | Does | Limits |
|---|---|---|
| `history_search(query, scope?, limit?)` | the records that contain a text, ignoring case: per hit the session, the time, the number of the record, its role and the text around the hit. `scope` is `current` (the default) or `all` | 20 hits, at most 50 with `limit`, and 16 KiB |
| `history_read(session?, from?, count?)` | the records of a conversation in order, from a number on, with a note where to continue; without `session` the current conversation | 20 records, at most 200 with `count`, and 32 KiB; a record that does not fit alone is cut |
| `history_sessions()` | the archived conversations, the most recent first, with when they started and their title, or else their first message | 50 |

- **Who reaches what.** For you, `current` is the conversation you are in, and `all` every one of yours. A
  peer reaches the conversation it is in and nothing else: `all`, a session it names that is not its
  current one, and every conversation of yours or of another peer are refused. The scope comes from the
  conversation the tool runs in, never from its arguments. A peer gets the tools only when `a2a` lists
  them, like any tool.
- **What a hit is worth.** The tools say so to the model: what another conversation holds may be outdated,
  and is a hint, not a fact.
- **Where it is.** `history/owner/<session>.jsonl` holds a conversation of yours, and
  `history/peer-<fingerprint>/<session>.jsonl` one of a peer, named by a hash of its key. A file is a JSON
  object per line: the number of the record within the conversation, the answer it belongs to, the time the
  answer ended, the role, the text, and the calls with their names and arguments. The last record of an
  answer says whether it was answered, failed or canceled; why it failed is not kept, as a peer reads its
  own conversation. A conversation that goes on after a restart goes on in its file. The title of a
  conversation of yours is in `history/owner/<session>.title`, and goes with it. Directories are 0700,
  files 0600.
- **The archive holds what your agent saw.** Everything a tool gave back is in it, such as the content of a
  file the agent read, on disk below `~/.config/ouroboros/history/`, until you delete it. The tools for
  files read that directory and never change it, like the rest of the [directory of the node](#files);
  whoever may read it may read your conversations.
- **What is kept of a script.** The LLM calls [`run`](#scripts), so what is archived of a script is the
  script and what it gave back to the LLM, not every call it made inside.
- **Nothing is deleted by itself.** `/session` lists every archived conversation, yours and, after `tab`,
  those of peers; `d` pressed twice deletes one, see [Sessions](#sessions). A conversation archived again
  after that starts anew.
- **When writing fails** (a full disk, a directory in the way), the answer is given all the same, and the
  log says `plugin archive after failed` with the cause.
- **The log** records every call of the three tools: the tool, for whom, the scope and the number of hits,
  never what was searched or found.

Before the archive, nothing was archived: conversations from before are not in it.

### Settings

`/settings` in the terminal UI shows the configuration of the node in one place, and changes it. It is the
owner's alone, like every command of the UI: no agent and no peer reaches it.

| Category | Shows | Changes |
|---|---|---|
| The node | name, the description of the agent card, the access in use (what it is called and what tells it apart, like its endpoint or its account; never what it signs in with), model, the ID of the node | the name and the description (empty is the default); the accesses in `/setup`, the model in `/model`, both opened from here |
| The agents | the context provider and those this build has; whether `rebuild` may build without a sandbox, and whether there is one; whether `bash` runs its commands without one | all three |
| Plugins | every plugin of this build: on or off, whether it runs now, and why one cannot be turned off | turn a plugin on or off |
| Tools of your agent | `h2a`: whether all tools are allowed (`*`), and for each tool whether it is allowed, by its name or by `*`, with its settings | all tools, each tool, its settings |
| Tools for peers | `a2a`, the same; a tool for your agent alone is shown as such and cannot be allowed | the same |
| Protected paths | `protected`, the paths no tool for files touches, or that the default holds | remove a path, add one, go back to the default |

It opens with the list of the categories: `↑` `↓` select one, `enter` opens it, `esc` goes back to the chat.
In a category, `↑` `↓` select a line, `←` `→` what to do with it, `enter` does it, and `esc` goes back to the
categories. A text, like the name, a path or the settings of a tool (a JSON object like
`{"allow": ["~/share"]}`), is typed in a line below the list: `enter` saves it, `esc` keeps what was.

- **Every change is checked before it is written**, the way the node checks its configuration when it starts:
  an unknown tool, a setting a tool does not take, a protected path that is none, a context provider this
  build does not have, a plugin turned off that another one needs, a description of more than one line or
  300 characters. What fails changes nothing, and the footer says why.
- **The footer says from when a change holds.** The name, the tools, the paths and the context provider hold
  from the next message, for you and for peers. The description holds for the next one who reads the card. Whether `rebuild` may build without a sandbox holds for the
  next rebuild, whether `bash` runs without one for its next command. A plugin turned on or off holds once ouro starts anew: quit and start `ouro`, and `/stop` and
  `/start` the node. Until then its line says that it runs, or that it does not.
- **All tools.** Turning a tool off while all tools are allowed lists every other tool by its name instead
  of `*`, so that nothing else changes; from then on a tool of a plugin turned on later is not allowed by
  itself. Turning all tools on again drops the names, but keeps the settings of the tools that have some.
  For peers, `*` and every plugin turned on give them its tools: the line says so.
- **Secrets.** The key of the node and what the accesses sign in with are not shown, and not changed here:
  the setup changes the accesses. The log records that a setting was changed, and which, never a value.
- `/setup` and `/model` work as before; `/settings` opens them for the access and the model.
- **A provider stays on while it is used.** A provider is the plugin of its name (`openai`, `chatgpt`): it
  cannot be turned off while an access of the node signs in with it. Remove the access in `/setup` first.

#### Plugins on and off

The plugins are compiled into ouro, and `Plugins` in `internal/cli/cli.go` lists them: turning one on or off
chooses among those, and loads nothing. The configuration keeps the plugins that are off:

```json
"plugins": {"off": ["tailscale", "history"]}
```

- **What cannot be turned off**: `base`, `node`, `assistant`, `peers` and `tui`, which are
  ouro, every plugin another one that is on needs: `discovery` while `peers` is on, and a provider (`openai`,
  `chatgpt`) while an access of the node signs in with it. Their line says why. A provider no access uses can
  go: with `chatgpt` off, the setup has one provider and does not ask which.
- **When it holds**: the list is read when ouro starts. The node and the terminal UI are each started anew
  for it.
- **A plugin that is gone**: a name in `off` that this build does not have, as after a rebuild without that
  plugin, is passed over, and `/settings` lists it as not part of this build, to forget.
- **A list that cannot be**: if the plugins left on cannot start together (`config.json` was changed by
  hand), ouro starts all of them, and `/settings` says why.
- **Tools and peers**: turning a plugin on never changes `a2a`. With `*` in `a2a` its tools are for peers
  as well, which `/settings` says.

### Configuration

You can see and change the configuration by talking to your agent: "which tools do peers have?", "let beta
read `~/share`". The agent has two tools for that, and neither changes anything.

`/settings` in the terminal UI shows all of it in one place and changes it, see [Settings](#settings).

You can change the model yourself, without the setup and without a proposal: `/model` in the terminal UI
lists what every access of the node offers, and the one you pick holds from the next message on, also when
it is a model of another provider, see [Providers and accesses](#providers-and-accesses).

| Tool | Does |
|---|---|
| `get_config()` | returns the configuration as JSON: ID, name, description, model, `provider` (what the access in use is called) and `tools`. The key of the node and what the accesses sign in with are not in it, and neither is the ID of the access. What is not set shows with the default that holds (`description`, `h2a`, `a2a`, `protected`), and so do the switches of `rebuild` and `bash` |
| `propose_config(path, value, reason)` | proposes one field to become `value`: `name`, `description`, `model`, `tools.h2a`, `tools.a2a`, `tools.protected`, `rebuild_without_sandbox` or `rebuild_without_tests`, with a sentence on why. The proposal waits for you, and the agent is told its id |
| `withdraw_proposal(id?)` | takes back one of the agent's own proposals before you decided on it: with `id` the one it names, without an id it lists what waits, with their ids. Nothing is changed by it, and what you decided on is out of reach |

**Trust comes only from your decision.** What an agent may do and what a peer may reach is trust, so a
proposal changes nothing: it waits among the requests in the terminal UI (`/pending`), where you see the field, what
it is **now** and what it would be **then**, and accept or reject it. Now and then come from the
node: now is read from `config.json` when the list is shown, then is the value the node checked and would
write. Only the line "your agent says" is the text of the agent.

- The value is checked the way the node checks its configuration when it starts: an unknown tool, a tool that
  is listed twice, a setting a tool does not have, a value of the wrong type, a protected path that is none
  or a description of more than one line or 300 characters is an error the agent gets at once, and no
  proposal.
- The key of the node and the access to the LLM cannot be proposed: not which access the node answers
  through, and nothing of what an access holds, which is not in the configuration at all. A proposal that a
  peer talked your agent into must not send your conversations to another server. You change them alone, in
  the setup. The model can be proposed: it is one of the access in use.
- A proposal is made for the configuration as it was. If the field was changed since (by hand, or by another
  proposal you accepted), the list says so, and accepting applies nothing and drops the proposal.
- At most 16 proposals wait; a further one is not recorded, and the agent is told. The same change is
  recorded once.
- Proposals are decided in the terminal UI only. `ouro chat "…"` opens none: the agent says that the proposal
  waits there. `ouro ask` writes to another node without your agent, so no proposal comes from it.
- An accepted change is in `config.json` and holds at once, see above; `ouro.log` records it.
- **Both tools are for you alone.** For a conversation of a peer they refuse, `"*"` in `a2a` does not include
  them, and their names in `a2a` are an error: the node does not start and says that these tools are not for
  peers. A peer that makes your agent propose something for it (it can, with a persuasive answer to
  `ask_peer`) gets a line in your list, which says what would change, and nothing more.

The file tools cannot do any of this: `config.json` is locked for them, see [Files](#files).

### Providers and accesses

A **provider** is a kind of LLM a node can talk to. This ouro has two:

| Provider | Signs in with | Answers through |
|---|---|---|
| `openai` – OpenAI-compatible API | an endpoint and an API key, which may be none (a local Ollama) | `POST /chat/completions` at the endpoint |
| `chatgpt` – ChatGPT plan | your ChatGPT account, in the browser ("Sign in with ChatGPT") | `POST /v1/responses` at `api.openai.com`, within the limits of your plan |

An **access** is one way the node signs in: an endpoint with its key, or a ChatGPT account. A node can have
several, also several of the same provider – a key of your own and one of work, or two Ollama servers. Each
has an ID that never changes, a name you may give it, and what tells it apart: the endpoint or the account.

```
/model

OpenCode Go · https://go.opencode.example/v1
  model-a
  model-b
Ollama local · http://localhost:11434/v1
▸ model-c  in use
  model-a
ChatGPT · you@example.com
  model-d
```

The names here are examples: which models an access has is what its provider lists.

- **`/model` lists the models of all accesses**, each below what its access is called, which is the name you
  gave it or else what its service is commonly called. Pick a model and the agents answer with it from the next
  message on: a model of another access switches the provider with it. No setup and no restart is needed.
  The same model name in two accesses is two entries, and the one you pick is the one of its access.
- **An access that does not answer does not keep the list back.** The accesses are asked at the same time,
  each for at most 15 seconds. One that fails shows with the reason, below its name, and with the models it
  listed the last time (`models.json`), which you can still pick.
- **A conversation goes on with the new provider.** The messages and the results of the tools stay. What
  the model of the provider before thought does not go along: every provider keeps that in a form of its
  own, and none is sent what another one thought. The first time the agents answer through another access,
  the UI says once that the prompt cache starts anew there: the first requests may cost more or take longer.
- **Nothing is switched for you.** An access that fails, or a plan whose limit is reached, stops the answer
  with what went wrong. The node never moves to another access on its own.
- **Manage the accesses in the setup**: `/setup` in the terminal UI and `ouro start --setup` on the terminal
  both start with the accesses of the node. There you add one, sign in to one again (a new key, a login that
  ran out), rename one and remove one. Renaming changes no ID, so nothing that refers to the access breaks.
  The last access cannot be removed. Removing the access in use hands over to the first of the others, with
  the model that one offers by default, and says so; if that one does not list its models, nothing is
  removed. A ChatGPT access is signed out at OpenAI before it is removed.
- **Peers answer with the same access and model as you do.**

```
ouro start --setup

Accesses:

    1) Ollama local · http://localhost:11434/v1  (in use)
    2) ChatGPT · you@example.com

  a = add · l N = sign in again · r N NAME = rename · d N = remove

Access [number, Enter = 1]:
```

#### Where the secrets are

`config.json` names the access in use and holds nothing of it:

```json
{
  "access": "3f9a1c2e",
  "model": "model-c",
  "name": "alpha"
}
```

What the accesses sign in with is in `credentials.json` next to it, readable by you alone (0600) and
replaced in one step under a lock of its own:

```json
{
  "accesses": [
    {
      "id": "3f9a1c2e",
      "provider": "openai",
      "name": "Ollama local",
      "rev": "5d02b7aa",
      "title": "localhost",
      "about": "http://localhost:11434/v1",
      "data": { "base_url": "http://localhost:11434/v1" },
      "secret": { "api_key": "…" }
    }
  ]
}
```

- **Everything under `secret` is a secret of the node**, whatever a provider keeps there: it is never in the
  log, no tool reads or changes `credentials.json`, no proposal changes an access, and `get_config` shows the
  agent only what the access in use is called. `title` and `about` are what the UI shows.
- **A provider reaches its own access and nothing else.** It is handed that one access, to read and to renew,
  never the file, the configuration or the access of another provider.
- **A token that is renewed builds no agent anew.** `rev` changes when you sign in to an access again, and
  then the agents are built anew. What a provider renews on its own, like a token that ran out, is written
  back without that.
- **An older `config.json` is still read.** One with `base_url` and `api_key` is moved the first time ouro
  reads it: the two become the access `default` of the provider `openai` in `credentials.json`, the
  configuration gets `"access": "default"`, and the key is gone from it. The access is written first, so a
  crash in between leaves a configuration that is moved again the next time, to the same access. If the
  access cannot be written, the configuration stays as it is and ouro says why.

#### Sign in with ChatGPT

With the provider `chatgpt` the agents answer within your ChatGPT plan, without an API key. ouro uses the way
OpenAI documents for open-source apps that run where their user is
([ChatGPT plan usage](https://developers.openai.com/siwc/token-sharing-open-source)), and nothing else: no
client ID of another app, no file of the Codex CLI (`~/.codex/auth.json` is neither read nor written) and no
endpoint of ChatGPT itself. The API key stays available, as the other provider.

1. Pick "ChatGPT plan" in the setup. ouro opens the page of OpenAI in your browser (and puts its link on the
   clipboard in the terminal UI). You sign in there and allow the app to use your plan. The first time this
   registers a client of its own for your account, named "Ouroboros"; you can change the name on that page.
2. The browser comes back to ouro on `http://127.0.0.1:1455/auth/callback`. If that port is taken, ouro uses
   another free one.
3. ouro checks what came back – that it belongs to this attempt, that OpenAI signed the ID token for this
   client, and that using the plan was allowed – and keeps the tokens in `credentials.json`.

- **Without a browser on the machine** (a node on a server, over SSH): open the link on the machine you sit
  at and sign in. The browser then fails to load a page at `http://127.0.0.1…`, since that is the server's
  own address: copy that address from the address bar and paste it where ouro waits. It holds the same code.
  OpenAI offers no sign-in by a code alone (device login) for this way, so ouro has none.
- **Limits and cost.** The agents use your plan within its limits, and so do the answers to peers: a peer
  you allow tools spends your plan, not its own. For ChatGPT Plus the five-hour limit is shared with every
  other app that uses the plan. What an app may use of it, and whether it may use credits beyond the plan,
  you set in [ChatGPT Settings → Usage](https://chatgpt.com/settings/usage): ouro turns nothing on there.
  OpenAI's pages name no fee for registering the app; that is no promise that using it never costs.
- **When the limit is reached** the answer stops and says so, with the link to that page. ouro tells when it
  is over only if the API says so, and never switches to another access.
- **The login is renewed while the node runs**: an access token holds for an hour and is renewed a few
  minutes before it runs out, or when the API refuses it. The refresh token is replaced with every renewal
  and saved at once, in one step with the access token. Answers that find the token run out at the same time
  renew it once.
- **A login that ran out or was revoked** – after 30 days without use, or when you disconnect the app in the
  settings of ChatGPT – stops the answer with "the ChatGPT login is no longer valid: sign in again in
  /setup". There, `l` on the access signs in to it again, as the same account.
- **Removing the access** revokes its login at OpenAI and deletes its tokens here. The client that was
  registered for your account stays; you remove it in the settings of ChatGPT.
- **The prompt cache.** Every request names its conversation, as `prompt_cache_key` and in the header
  `session_id`. The header is what keeps the calls of a conversation where OpenAI cached it: measured against
  the API, calls that went on from the one before found nothing cached without it, and 50 to 67% of their
  tokens with it. OpenAI's pages for this way do not describe the header, so this may change; in the debug
  mode the log tells for every call what was sent and what was cached.
- **What the API takes here** is narrower than elsewhere, by OpenAI's rules for this way: requests are
  streamed and not stored, and the model list is the one of your account. A paid or remotely hosted service
  built on ouro needs another agreement with OpenAI and is not this case.

### Peers

With `ask_peer` in `a2a`, a peer you connected to can make your agent ask the peers you are connected to,
in your name, and nobody else: for a peer the agent neither writes to a node you are not connected to nor
proposes a connection. Two rules keep that from running in circles: the agent never asks the peer it is
answering, and a message is passed on by at most 3 agents.

`ask_peer` takes the name or ID of a node that is online, as `list_peers` shows it, and no address: an
agent cannot be sent to a `host:port` somebody put into its head. Only you address a node that way, with
`ouro ask`.

`list_peers` tells for each node how far the connection has come: `connected`, `connection requested`,
`not connected`, or `trusted, but not reachable right now`. The last is a peer you connected to whose node
did not prove to hold its key within 2 seconds: it is gone, slow, or someone else answers at its address.
It stays your peer and needs no confirmation; the agent tries it again later.

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
- The agent that answers peers looks an ID up with the tool `explain_error`, a function of its scripts. It
  has it whatever else `a2a` lists, as long as it lists one tool: a peer without a tool runs no script, and so
  cannot be told what went wrong. It is not a tool you can list. It finds only failures of the peer that is
  asking: with the ID of somebody else's error a peer gets the same "nothing on record" as with a made-up one.
- The explanation comes from the peer's agent, so it is there once that agent can answer again. While its LLM
  is still down, asking about the ID fails like any other message, with a new ID. Your agent knows this and
  does not promise you an explanation it does not have.

## The log

`ouro.log` is where the node writes what it does, and where the chat with your agent is recorded: the node
runs the answers of the terminal UI and of `ouro chat` (see [Sessions](#sessions)) and writes them to the same
file. After a conversation that went wrong, the log says what the agent did and where the time went:

```
2026/10/04 14:02:11 chat 3f6c…: message from the owner: "ask beta for the time"
2026/10/04 14:02:12 chat 3f6c…: llm call 1 took 1.204s: tool calls: 1, said "I will ask beta." [1.3k tokens, 0 cached (0%)]
2026/10/04 14:02:15 chat 3f6c…: tool ask_peer {"message":"What time is it?","peer":"beta"} took 2.871s: ok (24 bytes)
2026/10/04 14:02:16 chat 3f6c…: llm call 2 took 0.912s: text [1.3k tokens, 1.3k cached (100%)]
2026/10/04 14:02:16 chat 3f6c…: answered after 4.991s (llm calls: 2, tool calls: 1, sent 5.3 KiB, 2.6k tokens, 1.3k cached (50%)): "Beta says it is noon."
```

- **A line per step**: the message of the owner with its conversation, every call of the LLM when it has
  ended (with `text`, `tool calls: N` or `failed` and the error), every tool call when it has ended (the tool,
  its arguments, `ok` or `error` and the error), and the end of the answer: `answered`, `failed` with the error
  or `canceled`, each with the time it took and what it cost: the number of calls of the LLM, the number of
  calls of tools that ended (those in a script and `run` itself) and how much was sent to the LLM in all,
  the messages and the tools of every call as JSON. That grows with every call, as each sends the whole
  conversation again. Where the API says what a call cost, its line and the last line also have the tokens
  sent and how many of them the provider had cached: a call that goes on from the one before should find
  most of its tokens cached. What the LLM says along with
  calls of tools follows them as `said "…"`; a call that ends in `text` has no text in its line, because that
  text is the answer and is in the last line. The calls of a [script](#scripts) are recorded like the others,
  before the line of `run`.
- **Canceled**: when you type `/cancel` in the UI, interrupt `ouro chat` with Ctrl-C or stop the node, the call
  the agent was waiting for is recorded as `canceled` with the time it had run, and then the answer. Ctrl-C
  while `ouro chat` waits for an answer stops that answer; in the chat line by line you can then go on. The node
  writes these lines before it is gone, so the last line of a conversation says what the agent was waiting
  for. Quitting the UI or `/new` stops nothing: the answer goes on in the node.
- **How much text**: the message, what the LLM says and the answer are cut after 300 bytes, at the start of a
  character, with `… (+N bytes)` for the rest; a line break is written as `\n`, so every entry is one line.
  The arguments of a tool are cut the same way, but the content of `write` and `old` and `new` of `edit` are
  only given by their size, as in `{"content":"(1532 bytes)","path":"~/notes.md"}`. Of the result of a tool
  the log has only its size, never its text.
- **No secrets**: the key of the API and the key of the node are replaced by `[redacted]` wherever they would
  appear, in a message, an argument, an error, what the LLM says or an answer, also in the other usual
  encodings of the key of the node (base64 with or without padding, URL-safe, hex, also of its seed). What the
  agent read from a file or heard from a peer is not in the log, except what the LLM quotes of it in what it
  says, cut as above.
- **Peers**: what a peer writes to your agent and what it answers is not recorded, as before: the log has the
  request (`POST /`) and, for an answer that failed, the error with its ID, but no text of a peer.
- **Size**: `ouro.log` is kept below 10 MiB. Before a line is written to a log that reached that size, its
  content is moved to `ouro.log.1`, which is replaced, and `ouro.log` starts empty. One older generation is
  kept, at most 20 MiB in all. The node and the processes that log beside it take turns by a lock on
  `ouro.log.lock`, so no line of theirs is lost in a rotation. What reaches the log of a node in the background
  without passing through the node, such as what the runtime prints when it crashes, can be lost if it is
  written in the moment the log is rotated; it never overwrites a line.

Both files are 0600, and the tools for files read them and never change them, like everything in the
directory of the node (see [Files](#files)). A line is shortened, but what the agent was told to do is in it:
whoever may read `ouro.log` may read your conversations with your agent.

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
2. In Alice's terminal UI `/pending` shows `bob · your agent wants to connect`. With accept she agrees, and only
   then Bob gets a **connection request** (`contact/request`). It carries no message; Bob records it and
   answers `pending`, no LLM call happens. With reject the proposal is dropped and Bob never hears of it.
3. In Bob's terminal UI `/pending` shows the request; he selects accept (or reject) and presses `enter`.
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
internal/tui         terminal UI (Bubble Tea), with the splash and the setup
internal/setup       the setup of the node: its accesses, model, name; the terminal and the UI both use it
internal/llm         what a provider of an LLM is: how it signs in, lists its models and answers, and the one access it reaches
internal/credentials the accesses of the node, in credentials.json: what it signs in to its LLMs with
internal/chatgpt     the provider that signs in with ChatGPT: the login, its tokens, the Responses API (+ chatgpttest fake)
internal/node        the local node: Server runs it, Local controls the background one
internal/session     the sessions of the owner: the Service the node runs them in, and the client of its socket
internal/assistant   blueprint of the agents (owner and peers): run, the one tool of the LLM, and the tools list_peers, ask_peer, explain_error, get_config, propose_config, withdraw_proposal as functions of its scripts; what the owner's agent does goes to the log
internal/guard       which files a tool may touch: the core that is fixed, protected, allow and deny
internal/script      runs a script of the LLM (JavaScript, goja) with the tools of its agent as functions
internal/peers       the other nodes: who is online, who is connected, how to talk to them
internal/home        the node's directory: config, credentials, identity, contacts, failed answers, PID and log file, which it rotates
internal/a2a         A2A protocol: types, HTTP handler, client; channels between nodes
internal/agent       conversation memory and tool loop on top of an LLM
internal/openai      minimal OpenAI-compatible client (+ openaitest fake)
internal/discovery   LAN discovery, plus sources of hosts to probe directly (Tailscale)
internal/daemon      background process + PID file
internal/contacts    trusted peers and connection requests
internal/proposal    changes of the configuration the agent proposed, waiting for the owner
internal/diagnosis   why answers to peers failed, by category, for the agent to explain
internal/history     the archive of every conversation, and how the agent searches and reads it
internal/identity    Ed25519 key pair of a node
internal/config      persisted configuration
internal/update      self-update: from the newest main, or from the checkout as it is, in the sandbox
internal/sandbox     runs a command writing only where it may, without network unless allowed: sandbox-exec, bubblewrap
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

		// For the terminal UI: /greetings opens the screen, on which an action
		// greets everyone.
		plugin.Add(ctx, plugin.Screen{Command: "greetings", Title: "Greetings", Body: g.screen,
			Actions: []plugin.Action{{Name: "greet all", Run: g.greetAll}}})
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
| a tool with settings in `config.json` | wrap the `plugin.Tool` in `plugin.Configurable`, with a struct that has a JSON field per setting | `internal/plugins/files` |
| what the terminal UI shows of a tool call | `plugin.Add` a `plugin.ToolView` | `plugin/example`, `internal/plugins/peers` |
| a screen of the terminal UI | `plugin.Add` a `plugin.Screen` | `plugin/example` |
| a command of the terminal UI | `plugin.Add` a `plugin.Slash`, or list a `plugin.Action` in `Screen.Actions` | `plugin/example` |
| an A2A method for connected peers | `plugin.Add` a `plugin.Method`; peers run it with `Network.Call` | `plugin/example` |
| a channel to connected peers, for what flows | `plugin.Add` a `plugin.ChannelHandler`; peers open it with `Network.Open` | `plugin/example` |
| a way to find nodes | `plugin.Add` a `plugin.PeerSource` | `internal/plugins/tailscale` |
| another front end | `plugin.Add` a `plugin.FrontEnd` and the command that opens it, instead of the tui plugin | `internal/plugins/tui` |
| another LLM API | `plugin.Add` a `plugin.Provider` in a plugin of the provider's name: `Login` asks the owner through a `plugin.Dialog` and returns the `plugin.Credentials` to keep, `Models` lists the models of a `plugin.Access`, `Model` returns what the agents answer with, translating to and from the OpenAI message types. It is one more provider next to the others: the owner picks it in the setup, and its models show in `/model` | `internal/plugins/openai`, `internal/plugins/chatgpt` |
| a service for other plugins | return it from `plugin.NewService`; they name its type in their needs | `internal/plugins/discovery` |

What a plugin hands to the terminal UI is text and functions, nothing of how the UI is drawn. A screen is a
command, a title, `Body`, which returns the text to show, and the actions it offers; a command (`plugin.Slash`)
and an action are `Run`, which returns the line for the footer. They run off the UI, so they may ask the
network, and with a context that ends when the user quits. `Body` is called when the screen opens, after one
of its actions ran and every ten seconds while it is open. A command is named by one word without the slash:
`greetings` for `/greetings`. See [Terminal UI](#terminal-ui) for what happens when two plugins want the same
name.

A front end is what opens when `ouro` runs without a command, and what `ouro chat` opens without a message.
It talks to the agent of the owner through the service `plugin.Sessions`: the sessions of the node, which it
reaches over the socket of the node (see [Sessions](#sessions)). `Ask` starts an answer and returns at once,
`Watch` tells what happened in a session and what happens from then on, as `plugin.SessionEvent`s of plain
data, `Cancel` stops an answer, `List` lists the sessions, also those the node kept from before it started,
and `Delete` deletes one; `Peers` lists the conversations of peers, which `Read` gives to read and `Delete`
deletes too. `plugin.Await` asks and waits for one answer. A
front end shows a call of a tool as `plugin.ToolLine` says: the node sends the call, not its text. ouro opens a front end only if the list of plugins holds exactly one: with none it
prints the usage, with several it fails and names them.

The terminal UI is also where the owner starts and stops the node and accepts or rejects connection
requests. Those are operations of the frame, so no service offers them. They are in `plugin.Owner`, and that
is all that is in it: the status of the node, start, stop, the setup, the requests that are waiting, accept
and reject. The contacts, the key and the configuration cannot be had from it. The setup (`Owner.Setup`)
saves the endpoint, the key, the model and the name, and keeps the key to itself: it tells the front end
whether there is one, never which. A front end takes the `Owner`
as an argument of its `Plugin` function and gets it in `Plugins`, where the tui plugin gets it today. A plugin
cannot ask the kernel for it: `plugin.Owner` is sealed like the contacts, so that a plugin nobody put into
that list by hand, such as one an agent wrote, never decides whom the node trusts.

A provider signs in with a dialog of its own: it asks as many questions as it needs, one after the other
(`Dialog.Ask`), tells the owner a line (`Tell`), says that an answer was not taken (`Reject`) and has a page
opened in the browser (`Open`). The terminal asks them line by line, the terminal UI on pages of the setup;
the provider does not know which. What it returns is kept as the access: `Credentials.Title` and `About` are
shown, `Data` is what it needs that is no secret, and every text under `Secret` is kept out of the log. It
gets back a `plugin.Access`, which reads those credentials and renews them (`Renew`, under the lock of
`credentials.json`, so that a rotating token is not used twice) and reaches nothing else: not the store, not
the configuration, not the access of another provider.

How a provider uses the cache of its API is its own business, since every API wants it told another way.
ouro keeps the start of a conversation the same from call to call, and gives the provider the rest:
`plugin.ContextID(ctx)` in `Complete` names the conversation, to send the way the API wants a conversation
named (the openai provider as `prompt_cache_key`, the chatgpt provider also as the header `session_id`);
`ChatMessage.Usage` of the answer says how many tokens were sent and how many the API had cached, which the
UI and the log show; and `plugin.Trace(ctx)` takes, in the debug mode of the node, what the call sent and
got – with `plugin.Requests` how far the request begins like the one before, with `plugin.TraceResponse`
the headers of the answer. Those lines are what tells why a call was not cached.

Not plugins, and out of their reach: the identity of the node, request signatures, trust and contacts, the
accesses of the node as a whole (`credentials.json`), the
built-in A2A methods, opening a channel and its limits, the wire format, update and start/stop. They are the
fixed frame, wired up in `Plugins` and never handed to the kernel (a front end starts and stops the node
through the `plugin.Owner` it is given there): a plugin that needs or provides one of their types, or a type
through which
one can be had, stops ouro from starting, as does a second plugin that provides a service that exists. A
method added under a built-in name is never called, and `Network.Call` does not run the built-in ones.

Plugins are compiled in. There is no loading at runtime. The owner turns the plugins of the build on and off in
[`/settings`](#plugins-on-and-off), which holds once ouro starts anew.

#### Context providers and answer hooks

Plugins can contribute `plugin.ContextProvider` and `plugin.AnswerHook` with `plugin.Add`.
Set `"context_provider": "name"` in `config.json` to select a provider for both owner
and peer agents. Registration alone never selects one. Missing selected names,
duplicate registrations of the selected name, or a missing `Prepare` callback are
configuration errors; peers also refuse this invalid selection instead of using an
older agent. An empty selection preserves the existing context policy.

```go
plugin.Add(ctx, plugin.ContextProvider{
    Name: "example-context",
    Prepare: func(c context.Context, p plugin.Preparation) ([]plugin.ChatMessage, error) {
        // A provider decides how to incorporate ephemeral hook context.
        messages := append([]plugin.ChatMessage{p.Messages[0]}, p.Context...)
        return append(messages, p.Messages[1:]...), nil
    },
})
plugin.Add(ctx, plugin.AnswerHook{
    Name: "example-lifecycle",
    Before: func(c context.Context, p plugin.Preparation) ([]plugin.ChatMessage, error) {
        return []plugin.ChatMessage{{Role: "system", Content: "Answer concisely."}}, nil
    },
    After: func(c context.Context, outcome plugin.AnswerOutcome) error {
        // Inspect outcome.Reply and outcome.Err; persist plugin state if needed.
        return nil
    },
})
```

The shared agent loop runs this flow for CLI, TUI and peers. `Before` callbacks run
once in contribution registration order. Later callbacks see earlier contributions
in `Preparation.Context`. Contributions allow system/user text only, are scoped to
this answer, and never enter canonical history. With no selected provider they are
inserted after the system prompt; a selected provider explicitly incorporates them.
`Prepare` runs before **every** model call, including calls following tools. It
receives an owned snapshot including the system prompt and current bounded history.
Returned messages must preserve the first system message exactly and contain
adjacent complete tool call/result groups; invalid output or preparation failure
aborts the answer with the provider name and cause, without fallback.

Optional `Observe` receives each new user message, model reply and raw tool result
before core history trimming or repeated-result reduction. It runs synchronously;
failure aborts the answer. This is the persistence boundary: providers must retain
what they need themselves. The bounded core transcript remains the default source
snapshot, not a complete archive. Existing/restored history is available through
`Preparation.Messages`; it is not replayed as new events. Core does not store
plugin archives, summaries or retention policies; `internal/plugins/history` is a plugin that archives
with an `After` hook alone, and names the conversation of a tool with `plugin.ContextID`. Plugin cleanup belongs to
`ctx.OnStop`, including closing plugin storage. Switching providers does not replay
already discarded history.

Every admitted answer runs all `After` callbacks once, in registration order, on
success, failure or cancellation (including failure of `Before`). They receive
owned raw events for the current answer and the original terminal outcome. Before
failure stops further before hooks and model work. After failure allows remaining
after hooks to run, preserves a successful reply and its nil answer error, and
never obscures an original answer error. Failures remain in `Inspection.Diagnostics`
with hook name, phase and cause, and are logged by owner and peer assistants.
`Assistant.Inspection` exposes these records to plugins/front ends. There is no
automatic repair loop.

Callbacks and direct model processing share the answer's cancellation context;
after hooks are still invoked with that canceled context, so they should record
outcomes without requiring uncanceled model work. A canceled wait for another
answer does not start a lifecycle. Answers sharing a memory and conversation ID
are serialized through after processing; different conversations may run
concurrently, so plugin state must be synchronized. A callback must not reenter
the same conversation. `Processing.Model.Complete` accesses the configured model
with `Processing.ModelName` directly, bypassing providers, hooks and tool execution.
It is synchronous work owned by the callback; plugins must not leave detached work
running after cancellation. JavaScript sessions remain separate from this API.

`Processing.Conversation` contains the local ID and trusted `Peer`/`Origin` supplied
by the network. Owner and peer agents have separate memories; peer IDs are scoped
by authenticated identity. Plugin storage keys must include the caller identity,
and providers must not add owner-only data to peer preparation. Plugins are trusted
in-process code; this API does not sandbox their storage or callbacks.

With extensions active, `/context` and `Assistant.Contents` show the **last actual
prepared request**, including hook contributions, and any answer/preparation error.
They never invoke preparation speculatively. Before the first request there is no
prepared snapshot. `Assistant.Context` measures the actual sent request and tools;
a failed preparation sends no model request. `Inspection` includes owned copies of
messages, the tool definitions actually sent, and diagnostics for the last answer, retained with bounded conversation
memory. Without extensions, the existing next-context view remains unchanged. Only a
selected provider or a hook with `Before` counts as active here: a hook with `After` alone
does not change what is sent, so `/context` keeps the next-context view, followed by the
diagnostics of the last answer.


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
  its reason. Today these are `internal/openai/openaitest`, `internal/chatgpt/chatgpttest`,
  `internal/llm/llmtest`, `internal/update/updatetest` and `internal/cli/clitest`: the fakes and the helpers
  that the tests of several packages share (600 statements),
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
- gremlins only tries a mutant in code that coverage gives a block of its own. A constant or a `case`
  expression has none, so its mutants are always *not covered*, whatever the tests do. Such a mutant is
  applied by hand to a copy of the package, a test that kills it is made sure of, and it is listed in
  `mutation-uncoverable.txt` as `file:line:column MUTATION  # reason`: it is then reported as accepted.
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
| `Not covered` | no test of the package itself runs this line | add a test in the package; a constant or a `case` expression goes into `mutation-uncoverable.txt` (see above) |
| `Timed out` | the tests did not finish with the change | nothing: it was noticed |
| `Accepted as equivalent` | survivors listed in `mutation-equivalents.txt` | nothing; a line that no longer matches is named |
| `Accepted as uncoverable` | mutants not covered and listed in `mutation-uncoverable.txt` | nothing; a line that no longer matches is named |
| `Not mutated` | the packages left out because they signal processes | review their tests by hand |

Every survivor and every mutant not covered is either killed by a test or listed in one of the two files.
The counts change with every commit and are not kept here: the first line of the report has them.

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
[NOTICE](NOTICE). The binary links third-party modules under the MIT, BSD-2-Clause, BSD-3-Clause and Apache-2.0 licenses, listed with
their license texts in [THIRD_PARTY_NOTICES](THIRD_PARTY_NOTICES). Pass all three files on with the binary.

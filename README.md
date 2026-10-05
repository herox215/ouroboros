# Ouroboros

A tiny [A2A](https://a2a-protocol.org) agent node backed by any **OpenAI-compatible** LLM API –
OpenAI, Ollama, LM Studio, vLLM, OpenRouter, Groq, …
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

On the first run there is no node to chat with yet, and the UI opens with the setup instead: five pages for
the LLM endpoint, the API key, the model, the name of the node and what your agent may do. Behind the dialog
the snake lies uncurled on the starry sky, and with every page it curls up a little more. Once the setup is
saved, its tail reaches its mouth, it turns once and shrinks into the logo, and the splash takes over where
the jaws snap shut. `esc` goes back a page, and on the first page it quits; any key skips the end. The key is
never shown, and a key that is saved is only offered for the endpoint it was entered for. `/setup` opens the same
setup later with what the node has; there `esc` on the first page goes back to the chat. The node keeps
running while you set it up and is restarted with what the setup saved.

The UI is the chat with your own agent. You do not
write to other nodes yourself: the agent does, whenever you ask it to or it thinks a peer knows
better ("ask Luna how she is" → "Luna says she is fine"). What the two agents say to each other
stays between them; the UI only shows which peer your agent is waiting for.
The input is focused on start, so you can type right away. The conversation lives only as long as the TUI runs;
what your agent did for it is recorded in [`ouro.log`](#the-log).

The UI is a plugin like any other (`internal/plugins/tui`), and it is the front end of the standard ouro: the
plugin says so itself, nothing else names it. An ouro built without it prints the usage when it is started
without a command, and `ouro chat` talks line by line. If the list of plugins holds more than one front end,
ouro does not pick one: `ouro` and `ouro chat` fail and name the commands that open them (`ouro tui`, …),
which keep working.

Other plugins add to the UI, see [Extending](#extending):

- a **screen** next to the chat, opened with its command (`/greetings`) and left with `esc`. It may offer
  actions below its text: `←` `→` select one, `enter` runs it.
- a **command** (`/greet`), which runs something and reports in the footer. `/help` lists what the plugins added.
- what is shown of a **tool call** while the agent waits for it. `asking beta…` is what the plugin of
  `ask_peer` shows; a tool without such a line shows as the agent thinking.

A command has one meaning. The UI keeps its own commands (the table below), and of two plugins that claim the
same name the one that was started first gets it, a screen before a plain command. What loses its name is left out, and the UI says so in red in its
footer when it opens, until the first key is pressed: `/stop is taken by the terminal UI: the screen "Stats"
is left out`. The UI opens all the same, and so does everything else of that plugin.

While the UI is open your node is online: opening it starts the node, and closing it stops the node
again unless it was already running before (`ouro start`).

Everything the UI does is a **command**: you type it into the message input with a slash in front and send
it with `enter`. A command runs in the UI and never goes to your agent, so only you run commands; a command
the UI does not know stays in the input, to be corrected. While you type one, the footer lists those that fit.
A message that starts with a slash takes two: `//etc` sends `/etc`.

| Command | What it does |
|---|---|
| `/new` | new conversation |
| `/start` / `/stop` | start or stop the node (`/start` runs the setup of a node that has none) |
| `/setup` | run the setup |
| `/requests` | what waits for your decision: connection requests of peers, connections your agent proposes, and [changes of the configuration](#configuration) it proposes. `↑` `↓` select an entry, `←` `→` accept or reject, `enter` decides, `esc` back |
| `/help` | list the commands, those of the plugins too |
| `/quit` | quit |
| commands of plugins | open a screen (`esc` back) or run what the plugin added |

Keys only move around:

| Key | What it does |
|---|---|
| any key | skip the ouroboros splash (except `ctrl+c`) |
| `enter` / `alt+enter` | send / a new line |
| `pgup` `pgdn` | scroll the chat |
| `esc` | back from a screen to the chat |
| `ctrl+c` | quit, everywhere |

## Updates

`ouro update` fetches the newest `main` from the repository the binary
was built from, builds it with `install.sh` and replaces the running binary. A running node is
restarted.
It needs `git` and `make`; sources and build live in `~/.config/ouroboros/update/`.

Your agent has the tool `rebuild` for the other direction: it builds the checkout the binary was built from
(`make install` records it) with its files as they are now, committed or not, and runs `make test` there. Only
when the build and every test pass, the binary is replaced and a running node restarted; otherwise nothing is
installed and the agent gets the end of the output. That is how a change your agent made to the plugins of ouro
with its tools for files takes effect. The tool is for you only: a peer is refused even if `a2a` lists it. Take
`rebuild` out of `h2a` if your agent should not build what it wrote. A binary installed by `ouro update` keeps
the checkout of the one it replaced.

Endpoint examples: `https://api.openai.com/v1` (default), `http://localhost:11434/v1` (Ollama, no key),
`http://localhost:1234/v1` (LM Studio), `https://openrouter.ai/api/v1`.

## How it works

| Part | Details |
|---|---|
| LLM | `GET /models` for the picker, `POST /chat/completions` for replies |
| A2A | `GET /.well-known/agent-card.json`, JSON-RPC `message/send` on `POST /`, over HTTPS only (TLS 1.3 on TCP 41337, falls back to a free port) |
| Channels | JSON-RPC `channel/open` on `POST /` with `Upgrade: ouro-channel/1`: the node answers `101 Switching Protocols` and the TLS connection of the request carries messages in both directions until one side closes it, see [Channels](#channels). Not part of A2A |
| Discovery | UDP probe to multicast `239.255.41.38`, the broadcast address of every network, localhost and the peers of the tailnet on port 41338; nodes answer the sender and the group, so only port 41338 needs to be open |
| Agents | two per node, built from one blueprint: yours (`ouro`, `ouro chat`) runs in the UI process, the one answering peers in the node. Which tools each may use is set in `config.json`, see [Tools](#tools); at most 50 LLM calls per answer, of which one can run a [script](#scripts) that calls many tools |
| Memory | last 40 messages per conversation, but always the whole of the last answer, however many tool calls it took; up to 256 conversations. An answer that fails or is stopped is kept as far as it got, so that asked again the agent goes on from there; why it failed is not kept; a conversation of a peer is its `contextId` together with its key, so no peer continues or reads the conversation of another |
| Card | the description in the agent card is `Ouroboros instance named <name>` for every node; `list_peers` shows your agent the cards of connected peers. `capabilities.streaming` is `false`: in A2A it stands for `message/stream` (the answer to a message as server-sent events), which a node does not have. That peers can open channels is listed under `capabilities.extensions`; which channels is not, as anyone can read the card |
| Trust | nodes talk over TLS 1.3 with a self-signed certificate made from the node's Ed25519 key; the connection is pinned to the key the node is known by, and requests are signed for that key; a message only goes to a peer both owners agreed to connect to, and a node asks another to connect with `contact/request`, which carries no message |
| Limits | at most 64 connection requests wait for a decision: when the list is full, the one that waited longest makes room for a new one (trusted peers are never dropped). What your agent proposes waits in the same list but never takes the place of a request: with 64 waiting it is not recorded. At most 16 changes of the configuration your agent proposed wait for a decision. The tools for files and a script have limits of their own, see [Files](#files) and [Scripts](#scripts). One address gets 10 messages per minute from nodes that are not connected; further ones are refused with error `-32005` and leave no request (at most 1024 addresses are counted per minute, unknown nodes from any further address are refused). Against replays the node remembers every nonce for at least 4 minutes, at most 16384 per 4 minutes; beyond that it refuses requests rather than forget a nonce early. A connected peer has at most 8 channels open to a node at a time, a further one is refused with error `-32006`; a channel without a message in either direction for 5 minutes is closed; a message of a channel has at most 1 MiB; the channels of a peer that was removed from `contacts.json` or refused are closed within a second, and all channels when the node stops |
| Errors | a peer whose message your agent failed to answer is only told `the agent could not answer (error <ID>)`; the cause is in `ouro.log` under that ID, and only you (`ouro ask <your node>`) get to read it directly. The peer can ask your agent about the ID later, see [When a peer could not answer](#when-a-peer-could-not-answer) |
| Files | `~/.config/ouroboros/` – `config.json` (0600), `contacts.json` (0600), `proposals.json` (0600), `failures.json` (0600), `ouro.pid`, `ouro.log` (0600) with `ouro.log.1` (0600) and `ouro.log.lock`, `update/`. `config.json`, `contacts.json` and `proposals.json` are replaced in one step (temporary file, then rename), so a crash never leaves half a file; changes are serialized by a lock on `config.json.lock` / `contacts.json.lock` / `proposals.json.lock`. `ouro.log` holds what the node does and [what your agent does for you](#the-log); at 10 MiB it moves to `ouro.log.1`, see there |
| Process | the node holds a lock on `ouro.pid` (`PID PORT`) while it runs. Only a locked file counts as a running node, so one left behind by a crash or reboot is ignored and never gets a foreign process signalled |

Environment: `OURO_HOME`, `OURO_PORT`, `OURO_DISCO_PORT`, and `OPENAI_BASE_URL` / `OPENAI_API_KEY` as setup defaults.
`ouro start --setup` reconfigures, `ouro serve` runs in the foreground (e.g. for systemd).

Firewall (ufw): `sudo ufw allow 41337/tcp && sudo ufw allow 41338/udp`

## Tools

The agent has tools for peers (`list_peers`, `ask_peer`), for files (`read`, `write`, `edit`, `list`,
`delete`), one that runs a script calling the others (`run`) and, for you alone, two for the configuration
(`get_config`, `propose_config`) and one that builds and installs its own source ([`rebuild`](#updates)). Which of them it may use depends on who it is talking to, and is set in `config.json`. This is
what the setup writes:

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
| `a2a` (agent to agent) | a connected peer asks your agent | no tool, except `explain_error` (see [below](#when-a-peer-could-not-answer)) |
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
| `read(path, offset?, limit?)` | returns the text of a file; says when there is more and at which line to continue | 2000 lines or 256 KiB per call, a line has less than 256 KiB; what is not text (UTF-8 without NUL) is refused |
| `write(path, content)` | creates the file or replaces all of it, in one step, and creates missing directories | 1 MiB |
| `edit(path, old, new)` | replaces `old` by `new`; `old` has to match exactly, and exactly once | the file has at most 1 MiB, before and after |
| `list(path)` | the entries of a directory: directories end in `/`, files come with their size | 1000 entries; what the tool may not read is not listed |
| `delete(path, recursive?)` | removes a file or an empty directory; one with content only with `recursive: true` | stops, with nothing deleted, before anything it may not delete |

A path is absolute or starts with `~`, your home directory. Each of the five tools takes the settings `allow`
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
   - `config.json` and its lock file are neither read nor changed, under any name (also not through a hard
     link): they hold the key of your node and the key of the API, which would end up with the LLM.
   - Everything else in the directory of the node (`contacts.json`, `proposals.json`, `failures.json`,
     `ouro.pid`, `ouro.log`, `ouro.log.1`, `update/`) is read and never changed.
   - The source of the core is read and never changed: in every directory whose `go.mod` names the module
     `ouroboros`, the packages `internal/identity`, `a2a`, `contacts`, `config`, `home`, `daemon`, `update`,
     `kernel`, `safefile`, `peers` and `guard`. The plugins in the same checkout stay writable. That `go.mod`
     is not deleted and has to go on naming the module, or the rule would be gone with one edit.
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
[`rebuild`](#updates) your agent can change a plugin and install the result, and that build runs as you, with
whatever the changed plugin does: the core it could not edit no longer limits it. The tests are a check that
the build works, not that it is harmless.

### Scripts

An answer has at most 50 calls of the LLM, every call sends the whole conversation again, and every result of
a tool stays in it. `run(script)` is for what is known in advance: the same call for many peers or files,
results that are filtered or passed on unchanged. The agent writes a script in JavaScript, and inside it
every other tool it has is a function of its name. Every tool stays callable on its own.

```js
// "which of my peers knows the way to the station?", in one call of the LLM
return list_peers().split("\n")
  .filter(line => line.includes(", connected)"))
  .map(line => line.split(" ")[0])
  .map(peer => {
    try { return peer + ": " + ask_peer({peer: peer, message: "Do you know the way to the station?"}) }
    catch (e) { return peer + " could not answer: " + e.message }
  })
```

- The script is the body of a function. What it returns goes to the LLM: a string as it is, everything else
  as JSON.
- A tool takes one object with its parameters and returns the text the LLM would get. A tool that fails
  throws an `Error` with the text of its error: not caught, it ends the script, and the LLM gets the error
  with the line of the script; with `try`/`catch` the script goes on.
- The functions are also in the object `tools`. A tool whose name is a word of JavaScript has to be called
  there: `tools.delete({path: "…"})`. `delete({…})` would be the operator, which does nothing, so a script
  that says so is not run and the LLM is told how to write it.
- **A script can do nothing the agent could not do with single calls.** It sees exactly the tools this
  agent has, with their settings, and `run` itself is not among them. For a peer these are the tools of
  `a2a` (and `explain_error`), and `run` is off for peers unless `a2a` lists it. A tool runs under the
  conversation the script was started in: for the same peer, as many hops away, so every rule a tool
  enforces holds inside a script. In the terminal UI the calls of a script show like single ones
  (`asking beta…`, `writing notes.md…`).
- The environment is the language (ES2015 and most of what came after, interpreted by
  [goja](https://github.com/dop251/goja), which is part of the binary) with its standard objects (`JSON`,
  `Math`, `String`, `Array`, …) and the tools. No network, no files, no processes, no `require` or
  `import`, no timers, no `console`.
- Every script starts in a fresh interpreter. Nothing is kept between two calls; what is to stay is written
  to a file with `write`.

| Limit of a script | Value | Beyond it |
|---|---|---|
| time | 60 s | the script is stopped, and so are the tools it is waiting for; a tool that does not end is waited for 2 s longer and then left behind |
| calls of tools | 64 | the script is stopped at the next call |
| size of the script | 64 KiB | it is not run |
| size of the result | 64 KiB | the LLM gets an error instead |
| calls of functions within each other | 1024 | the script is stopped |
| memory | 256 MiB, see below | the script is stopped |

A script that is stopped cannot catch that. The limits are fixed in the code (`internal/script`), the same
for you and for peers.

**The limit of memory is approximate.** The interpreter has none of its own. While a script runs, the node
looks at the heap of the whole process every 5 ms and stops the script once the heap has grown by more than
256 MiB since the script started. That catches a script that keeps collecting data. It does not catch a
single step that allocates more than that at once, like `"x".repeat(2 ** 30)`: the interpreter cannot be
stopped inside a step, and the process may run out of memory. The same holds for the time: a regular
expression that takes minutes to fail is one step. The script is then given up after 60 s and the answer goes
on, but the step keeps a processor busy until it ends. As the heap is the one of the process, two scripts
that run at the same time count against each other, and so does everything else the node allocates
meanwhile. Whoever may run scripts on your node may therefore slow it down or bring it down: list `run` in
`a2a` only for peers you would let do that.

### Configuration

You can see and change the configuration by talking to your agent: "which tools do peers have?", "let beta
read `~/share`". The agent has two tools for that, and neither changes anything.

| Tool | Does |
|---|---|
| `get_config()` | returns the configuration as JSON: ID, name, model, endpoint and `tools`. The key of the node and the key of the API are not in it. What is not set shows with the default that holds (`h2a`, `a2a`, `protected`) |
| `propose_config(path, value, reason)` | proposes one field to become `value`: `name`, `model`, `tools.h2a`, `tools.a2a` or `tools.protected`, with a sentence on why. The proposal waits for you |

**Trust comes only from your decision.** What an agent may do and what a peer may reach is trust, so a
proposal changes nothing: it waits among the requests in the terminal UI (`/requests`), where you see the field, what
it is **now** and what it would be **then**, and accept or reject it. Now and then come from the
node: now is read from `config.json` when the list is shown, then is the value the node checked and would
write. Only the line "your agent says" is the text of the agent.

- The value is checked the way the node checks its configuration when it starts: an unknown tool, a tool that
  is listed twice, a setting a tool does not have, a value of the wrong type or a protected path that is none
  is an error the agent gets at once, and no proposal.
- The key of the node, the key of the API and the endpoint of the LLM cannot be proposed. A proposal that a
  peer talked your agent into must not send your conversations to another server. You change them alone, with
  `ouro start --setup`.
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

### Peers

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

## The log

`ouro.log` is where the node writes what it does, and where the chat with your agent is recorded: in the
terminal UI and with `ouro chat`, which run your agent in their own process and add to the same file. After
a conversation that went wrong, the log says what the agent did and where the time went:

```
2026/10/04 14:02:11 chat 3f6c…: message from the owner: "ask beta for the time"
2026/10/04 14:02:12 chat 3f6c…: llm call 1 took 1.204s: tool calls: 1, said "I will ask beta."
2026/10/04 14:02:15 chat 3f6c…: tool ask_peer {"message":"What time is it?","peer":"beta"} took 2.871s: ok (24 bytes)
2026/10/04 14:02:16 chat 3f6c…: llm call 2 took 0.912s: text
2026/10/04 14:02:16 chat 3f6c…: answered after 4.991s (llm calls: 2): "Beta says it is noon."
```

- **A line per step**: the message of the owner with its conversation, every call of the LLM when it has
  ended (with `text`, `tool calls: N` or `failed` and the error), every tool call when it has ended (the tool,
  its arguments, `ok` or `error` and the error), and the end of the answer: `answered`, `failed` with the error
  or `canceled`, each with the time it took and the number of calls of the LLM. What the LLM says along with
  calls of tools follows them as `said "…"`; a call that ends in `text` has no text in its line, because that
  text is the answer and is in the last line. The calls of a [script](#scripts) are recorded like the others,
  before the line of `run`.
- **Canceled**: when you quit the UI, type `/new` for a new conversation or interrupt `ouro chat` with Ctrl-C,
  the call the agent was waiting for is recorded as `canceled` with the time it had run, and then the answer.
  Ctrl-C while `ouro chat` waits for an answer stops that answer; in the chat line by line you can then go on.
  The UI writes these lines before it is gone, so the last line of a conversation says what the agent was
  waiting for.
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
2. In Alice's terminal UI `/requests` shows `bob · your agent wants to connect`. With accept she agrees, and only
   then Bob gets a **connection request** (`contact/request`). It carries no message; Bob records it and
   answers `pending`, no LLM call happens. With reject the proposal is dropped and Bob never hears of it.
3. In Bob's terminal UI `/requests` shows the request; he selects accept (or reject) and presses `enter`.
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
internal/setup       the setup of the node: endpoint, key, model, name; the terminal and the UI both use it
internal/node        the local node: Server runs it, Local controls the background one
internal/assistant   blueprint of the agents (owner and peers) and the tools list_peers, ask_peer, explain_error, run, get_config, propose_config; what the owner's agent does goes to the log
internal/guard       which files a tool may touch: the core that is fixed, protected, allow and deny
internal/script      runs a script of the LLM (JavaScript, goja) with the tools of its agent as functions
internal/peers       the other nodes: who is online, who is connected, how to talk to them
internal/home        the node's directory: config, identity, contacts, failed answers, PID and log file, which it rotates
internal/a2a         A2A protocol: types, HTTP handler, client; channels between nodes
internal/agent       conversation memory and tool loop on top of an LLM
internal/openai      minimal OpenAI-compatible client (+ openaitest fake)
internal/discovery   LAN discovery, plus sources of hosts to probe directly (Tailscale)
internal/daemon      background process + PID file
internal/contacts    trusted peers and connection requests
internal/proposal    changes of the configuration the agent proposed, waiting for the owner
internal/diagnosis   why answers to peers failed, by category, for the agent to explain
internal/identity    Ed25519 key pair of a node
internal/config      persisted configuration
internal/update      self-update: from the newest main, or from the checkout as it is
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
| another LLM API | return a `plugin.LLM` from `plugin.NewService`, instead of the openai plugin | `internal/plugins/openai` |
| a service for other plugins | return it from `plugin.NewService`; they name its type in their needs | `internal/plugins/discovery` |

What a plugin hands to the terminal UI is text and functions, nothing of how the UI is drawn. A screen is a
command, a title, `Body`, which returns the text to show, and the actions it offers; a command (`plugin.Slash`)
and an action are `Run`, which returns the line for the footer. They run off the UI, so they may ask the
network, and with a context that ends when the user quits. `Body` is called when the screen opens, after one
of its actions ran and every ten seconds while it is open. A command is named by one word without the slash:
`greetings` for `/greetings`. See [Terminal UI](#terminal-ui) for what happens when two plugins want the same
name.

A front end is what opens when `ouro` runs without a command, and what `ouro chat` opens without a message.
It talks to the agent of the owner through the service `*plugin.Assistant`, and shows a tool call as
`plugin.ToolLine` says. ouro opens a front end only if the list of plugins holds exactly one: with none it
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

Most survivors are in the drawing of `internal/tui` (the arithmetic of the logo), some in the parts of
`internal/discovery` that depend on the network interfaces of the machine, and some in `internal/safefile`,
where what the mutant leaves out only shows after a power failure or a write that fails. The counts change
with every commit and are not kept here: the first line of the report has them.

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

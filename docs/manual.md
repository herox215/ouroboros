# Ouroboros — the manual

The full manual. For the short version, see the [README](../README.md).

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
ouro gui                  # native Wails GUI: chat, create sessions, switch profiles
ouro stop
ouro update               # build and install the newest main
ouro rollback             # the build before the last update or rebuild again
```

## Desktop status / Omarchy

`ouro desktop status` exposes a privacy-safe, platform-independent JSON status;
`ouro desktop waybar` renders it for a Waybar custom module and
`ouro desktop open` opens the GUI. On Linux, the running node also registers a
StatusNotifier tray icon (including on Omarchy/Quickshell); click for a small menu
with running session titles and an explicit action to open the GUI.
Optional Waybar custom-module configuration and
installation instructions are in [integrations](../integrations/README.md). Native
macOS builds with cgo also show a native menu-bar icon with status, running
session titles and “Open Ouroboros”. Windows has no native tray adapter yet;
it can use the same status and open commands. Desktop configuration and auto-start are never changed
automatically.

## Terminal UI

Running `ouro` without arguments opens the terminal UI. New conversations use the current
terminal directory as their project. `ouro /path/to/project` (or `ouro tui /path/to/project`)
selects another start directory. In a conversation, `/cd PATH` changes its project, `/cd`
shows it, and `/cd --workspace` returns to the agent's own profile workspace. The header
shows the selected project; it is saved per conversation and restored when reopened.
Paths with spaces may be quoted. Relative file paths and commands without an explicit
directory use the project, while temporary agent files belong in its separate workspace.
A project change is refused while an answer or delegated task is running; selecting a
project does not change file access permissions.

Running `ouro` without arguments opens the terminal UI. On start it shows the ouroboros
splash for about three seconds: the name decodes itself letter by letter in the middle of the screen,
the tagline is typed below it, and a shine sweeps across the name. A terminal too narrow for the big name
shows it in plain letters.
On a light background the splash takes darker colors. Any key (except `ctrl+c`) skips it. Without a terminal (pipes, CI, redirects)
`ouro` prints the usage instead, so scripts keep working.

On the first run there is no node to chat with yet, and the UI opens with the setup instead: the access to
the LLM, the model, the name of the node and what your agent may do. For the access you pick a provider, and
the provider asks what it needs: the one for OpenAI-compatible APIs the endpoint and the API key, the one for
ChatGPT opens the page to sign in on. Behind the dialog
the snake lies uncurled on the starry sky, and with every page it curls up a little more. Once the setup is
saved, its tail reaches its mouth, it turns once and shrinks to the name of the splash, and the splash takes
over. `esc` goes back a page, and on the first page it quits; any key skips the end. The key is
never shown, and a key that is saved is only offered for the endpoint it was entered for. `/setup` opens the same
setup later with what the node has: it starts with the accesses of the node, to pick one, add one, sign in to
one again, rename or remove one. There `esc` on the first page goes back to the chat. The node keeps
running while you set it up and is restarted with what the setup saved.

The UI is the chat with your own agent. You do not
write to other nodes yourself: the agent does, whenever you ask it to or it thinks a peer knows
better ("ask Luna how she is" → "Luna says she is fine"). What the two agents say to each other
stays between them; the UI only shows which peer your agent is waiting for.
The whole **loop** of an answer is in the history, not only its end: what the agent says, round by
round, and below the last words of the answer one muted line that says how many tools it used, like
`↳ Used 4 tools, 1 failed`. The line counts the calls of all its rounds, and it is there while the
answer is still on its way, as far as it has come; what the agent waits for at that moment is the line
below the input (`asking beta`), once the call has run for 0.3 s: one that ends sooner, as most commands
and reads do, leaves the agent thinking, so the line does not flicker. What each call was, with its
arguments and its result, is on the context screen (`/context`).
An answer that ouro could have done better, by the [signals](#the-log) of the log, is looked back on once it is
given, and what the review would change of ouro follows it as a faint line, like `✦ bash waited 42s for the
build: let it run as a job · /suggestions`. It changes nothing by itself: it waits in `/suggestions`, where accepting it
opens a new conversation that asks your agent to make it, after checking it against the source, as the review read
only the log; the conversation shown goes on. The answer to it is not reviewed again, so that reviews do not
suggest what to make of their own suggestions. Rejecting it drops it. Front ends get it as the session event
`suggestion`; the chat does not keep it with the session, `/suggestions` does, in `suggestions.json` of the node, at
most 16, the same text once.
The review also looks for what to [remember](#memories) of you or your environment, so that you need not say it
twice: a correction of how you want answers, a path, a convention of a project. That it keeps at once, and tells it
with a faint line, like `✦ remembered: build: ouro is built with make build · /undo`; `/undo` takes back the last
of them in the conversation shown. Where the answer took a message [the thread](#the-thread) handed on, the line
is shown in the thread too, so that `/undo` takes it back there. Front ends get it as the session event `remembered`, whose `args` are what
takes it back; the GUI shows the line with a button for it.
What the model thinks before it writes comes in as the stream brings it, and the chat shows the last
lines of it, faint, above the words of its round, under `· thought`; `ctrl+r` hides it again. A
provider that keeps its thinking to itself, as the responses API of OpenAI does, sends none of it,
and then there is nothing to show. Front ends get it as the session event `thought`, a piece each.
The answer itself grows in the chat as the stream brings it, with a mark at its end, and the answer of
the end replaces it with the whole of it; a round of the loop that ends with tools takes back what it had
written meanwhile. Front ends get it as the session event `answering`, a piece of 32 characters each:
those pieces are not recorded in the session, as the Answer holds all of them, and the UI draws them
together 20 times a second rather than one per piece. Of the conversation it keeps what it has drawn of
the entries that cannot change anymore and builds only the answer on its way anew, so that a long one
costs the same per frame as a short one. Its newest word is threaded in: it shows
as glyphs of its own width and settles into its letters over three frames, so that
the answer seems to be written rather than to appear in blocks. While they come, the line below the input says
`<agent> is writing` instead of `is thinking`. What the model wrote before it goes on with a call of a tool is
shown at once, also the last words that did not fill a piece. The call itself shows nothing until it is
written, which for a large tool argument may take a minute: meanwhile the line says how much of it there is,
`<agent> is writing a call · 3.2 KiB`, anew every 512 bytes. Front ends get it as the session event `calling`,
with the bytes in `n`, not recorded either. A provider that brings a call in one piece tells nothing of it.
What the agent says is markdown, as the models write it, and the chat shows it formatted: headings, bold,
italic, code in a line and in blocks, lists, quotes, tables and links, in the colors of the terminal theme and
without the marks. A line break is kept, as models write poems and addresses line by line. What you write is shown as you wrote it.
The wheel scrolls at once, a line a step, under the hand: a touchpad sends many steps for a fast stroke and
few for a slow one, and nothing moves on after the fingers stop. The page keys and, on `/context`, the arrows,
`home` and `end` glide instead: they carry the view to where they mean over a tenth to a quarter of a
second, fast at first and slowly at the end, at 60 frames a second; a line is the smallest step a terminal
has. Pressed again on the way, a key adds to where it goes and the view goes on from where it is; the wheel
ends a glide and takes over. While the chat is at its end, what comes glides into view the same way;
scrolled up, it stays where it is.
The UI takes the mouse for the wheel, so the terminal does not select text itself. The chat does: press on the
history and drag, and once you release, the text selected is on the clipboard, without its colors. A press
alone marks nothing, so a click leaves no mark. It is put
there through the terminal (OSC 52, which also works over SSH) and on the clipboard of the system. A key drops
the selection.
In its footer, the chat shows how long the context of the conversation is:
`context 45.2 KiB ≈ 11.6k tokens`. That is what the last call of the LLM sent, the prompt, the
tools and the conversation so far as JSON; the tokens are an estimate at 4 bytes a token. It grows with
every step of an answer, since each sends the whole conversation again, and is updated whenever the loop
shows something new. A new conversation (`/new`) has none until its first call, and neither has one that
goes on after a restart. Once the API tells how many of the tokens it was sent it had cached, the chat shows
that too: `· cached 92%, session 84%`, of the last call and of all calls in the session since the node started.
A call that goes on from the one before should find most of its tokens cached; a low rate costs time and money.
The footer of the chat and `/context` also show TPS for the last model call: API-reported output tokens divided by
the full request duration, including request latency but excluding tool execution. Missing output usage
is shown as `TPS unknown`. Costs use the API-reported USD `usage.cost`, when available; ChatGPT access
shows `cost included in plan`. Missing costs are unknown, never assumed to be zero. The session total
is accumulated since the node started and marked `(partial)` if any calls have unknown costs. No
model-price estimate is currently applied. On narrow terminals the footer cuts them off; `/context`
provides the detailed metrics.
`/context` shows what the context is: the system prompt, every tool with its schema
and the conversation so far, message by message with its size. It is laid out to be read, not as a chat: every
message has a head with its role and size and hangs on a bar of the color of its role, the functions a tool
offers and their parameters stand out, its schema is indented JSON, and the arguments of a call are taken apart,
a text as the lines it has. What the text says is not changed. Every message has its number in its head.

The input is focused on start, so you can type right away. The conversations are **sessions of the node**, not
of the UI: the node runs their answers and keeps them, also when it starts anew, see [Sessions](#sessions). What your
agent did for them is recorded in [`ouro.log`](#the-log), and the conversation itself in the
[history](#history), where your agent can look it up again.

The UI is a plugin like any other (`internal/plugins/tui`), and it is the front end of the standard ouro: the
plugin says so itself, nothing else names it. An ouro built without it prints the usage when it is started
without a command, and `ouro chat` talks line by line. If the list of plugins holds more than one front end,
ouro does not pick one: `ouro` and `ouro chat` fail and name the commands that open them (`ouro tui`, …),
which keep working.

The optional `gui` plugin contributes only the explicit `ouro gui` command, not a default front end.
It opens a Wails v2 window with the profile's thread, the same
conversation the TUI shows first. Say what is to be done there; the node routes topics,
relays questions and gathers results through its existing thread implementation.
Of the thread the GUI shows the last six hours, as the TUI does and as its agent
is told it; the archive keeps the rest. The GUI adds no routing of its own. Beside the thread there is no navigation of its own,
as in the demo: no profile picker, search, new session or toggle; the profile is chosen at
start (`ouro --profile NAME gui`). **Zum Thread** returns from an individual session.
The status shows work in progress, questions and the number of active sessions the thread
handed work to. **Stoppen** calls the same session cancellation as the TUI, including
stopping the thread's delegated answers. Messages can still be sent while work runs.
`ouro gui [DIRECTORY]` uses the same initial-project handling as `ouro tui [DIRECTORY]`:
the current directory by default. A busy thread keeps its project and shows a notice.
Beside the thread, the sidebar follows the part of it in view, as in the demo: the part
whose messages cross the middle of the history, or the newest one at its end. Each message
of the owner opens a part; the sessions the thread started (`session_new`), handed a message
to (`session_send`), looked at (`session_status`) or stopped (`session_stop`) on it, and those
whose result it reports there, belong to it, as the `handed` events of the thread tell: the thread
hides the calls of its agent, so it tells those as events of their own, also when it is read
back from the archive. Front ends that do not know the kind leave it out. The
sidebar shows the time of the part and its sessions with their state, or **Noch keine**,
refreshed every three seconds without reopening the conversation; a session opened from it
keeps the part beside it. The context rail shows where those sessions stand, as `SessionTrace`
reads them: the phase of the loop to the answer (thinking, calling a tool, writing the answer,
asking the owner), the call on its way and what it does, the call it did last, and what comes
next. Under that stand, a chart draws the calls over the time of the session in lanes of its
most used tools, so that its rhythm and its mix show at a glance rather than every call as a
line. Older messages fade out at the top while older ones are above. Opening a tool result
also shows it in that rail, using the same lazy request and cache. It may stay beside the thread
when returning from its session, labelled with its source; switching profiles clears it. The
rail is a saved result, not a live browser view.
`ouro --profile NAME gui` selects the initial profile. Switching is local to the window and does not
change configuration. Profiles with the GUI plugin disabled are omitted. The window starts the
node through the same sessions API as the TUI when needed, and releases nodes it started on close;
running answers continue in the node.
The gear button opens the settings, including **Darstellung**: **System** (the default),
**Hell** or **Dunkel**. System follows changes to the OS appearance immediately. The choice
is saved locally for the GUI across profiles and restarts.
The frontend is plain HTML/CSS/JavaScript embedded in the executable: no Node, npm,
Wails CLI, separate web server, or frontend build is needed. The Go binding supplies
the profiles and session list (owner and peer conversations, newest first).
The sidebar groups sessions by date. The GUI opens chats through the same session service as the TUI: owner sessions use
`Watch`, peer conversations use `Read`, and live events update the selected chat. Tool results
and arguments are not included in the GUI transcript payload; opening a tool call requests
its saved result separately. Long texts show an 8,000-character preview until expanded.
Create an owner session with **Neue Session**, or select an existing one to continue it.
The message field sends with Enter; Shift+Enter inserts a newline. Sending uses `Ask`, and
`Steer` while an answer is running (including replies to agent questions). Peer conversations
remain read-only. Refresh reloads the session list. Like the TUI, opening an owner session
restores it in the node and watches its events; when a tool asks ouro to start anew from a
new build, the window closes, and the new process opens one again showing the profile and
the session the window before showed. The same happens when the node starts anew without
the watched session asking for it, as after a rebuild in another session, `ouro update` or
a rebuild by a peer: once the live stream ends, the window opens the session again a few
times over about half a minute, and a node that says it runs another image (`GET /instance`
on its socket) makes the window start the binary that node runs. Where the window cannot
close itself, it shows the whole session from the new node instead. A node that does not
come back is reported once; the transcript stays. There are no configuration operations.

Native builds need CGO and the [Wails prerequisites](https://wails.io/docs/gettingstarted/installation/).
On Linux the build uses GTK 3 and **WebKitGTK 4.1** (Debian/Ubuntu:
`build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev`; Arch:
`base-devel pkgconf gtk3 webkit2gtk-4.1`). On macOS install the Xcode command-line tools.
`make build` supplies Wails' `production` tag and the Linux `webkit2_41` tag;
installation, update and rebuild use this same build recipe. When the executable is in the
source checkout, `./ouro gui` checks this recipe through Go’s build cache before opening
the window. Changed build inputs (including uncommitted code and embedded frontend files)
are rebuilt into a staging directory; only a successful, different build replaces the
binary and restarts it with the same arguments and environment. A build failure stops
the launch rather than opening a stale GUI. Installed copies outside the checkout do
not perform this development check. A direct native Go build
must likewise use `go build -tags production,webkit2_41 ./cmd/ouro`.
`CGO_ENABLED=0 make build` still produces a headless binary, whose `gui` command explains
that a native build is needed. Plain `go test` does not require desktop libraries; native
GUI tests can also run with `go test -tags production,webkit2_41 ./internal/plugins/gui`.
Frontend tests run with `node --test internal/plugins/gui/frontend_test.mjs`; Node is needed
only for these tests, not to build or run ouro. `make test-gui` runs both native GUI
and frontend tests.

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
footer when it opens, until the first key is pressed: `/help is taken by the terminal UI: the screen "Stats"
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
| `/new` | a conversation of its own, outside [the thread](#the-thread); the one shown goes on in the background |
| `/session` | the conversations of the node in a dialog over the chat, the latest first, the one shown marked: type to filter the list, `↑` `↓` select, `enter` shows the one selected, `ctrl+d` twice deletes it, `tab` switches to the conversations of peers, `esc` takes back what is typed and then goes back; see [Sessions](#sessions) |
| `/thread` | back to the thread, which the UI opens with: one conversation with your agent about everything, while behind it each topic is a conversation of its own and several work at once; see [The thread](#the-thread) |
| `/cancel` | stop the answer that is on its way in the conversation shown, and the tasks its agent delegated |
| `/jobs` | the commands the agent runs in the background for the answers on their way, in your conversations and in those of peers, each with where it runs and for how long, the list kept up to date: `↑` `↓` select, `enter` stops the one selected, `esc` back; see [Commands](#commands) |
| `/setup` | run the setup; for a node that is set up it starts with its accesses: `enter` uses the one selected, `l` signs in to it again, `r` renames it, `d` removes it, the last row adds another one, see [Providers and accesses](#providers-and-accesses) |
| `/context` | the next context, or the last actual prepared request with extensions, message by message: `↑` `↓` `pgup` `pgdn` `home` `end` scroll, `esc` back. `/` searches it: every match is marked as you type, without regard to case and across the ends of lines; `↑` `↓` go from match to match, `enter` keeps the search, `esc` ends it |
| `/settings` | the configuration in one place, in a dialog over the chat, by categories: the node, the agents, the plugins, the tools of your agent, of peers and of single peers, the protected paths. `↑` `↓` select, `enter` opens a category, `←` `→` what to do with a line of it, `enter` does it, `esc` back; see [Settings](#settings) |
| `/model` | the models of every access of the node, grouped by access, the one in use marked: `↑` `↓` select, `enter` saves it (the agents answer with it from the next message on, also when it is a model of another provider), `p` makes it the model [peers are answered with](#providers-and-accesses), `f` a [fallback](#fallbacks), `esc` back |
| `/undo` | take back the last memory the review of an answer kept by itself in the conversation shown: one it made is forgotten, one it replaced is as it was before. A memory changed since is not taken back; see [Memories](#memories) |
| `/suggestions` | changes of ouro the reviews suggest, in the existing dialog over the chat. `↑` `↓` select, `←` `→` accept or reject, `enter` decides, `a` accepts all in one new conversation, `esc` closes without deciding. Accepting starts an agent task; it does not apply a change immediately |
| `/effort` | how much the model thinks before it answers in the conversation shown, see [Sessions](#sessions) |
| `/debug` | turn the debug mode on or off: the log of the node then tells much more |
| `/help` | list the commands, those of the plugins too |
| `/keys` | list the keys of the chat in a dialog, `esc` back |
| `/quit` | quit |
| commands of plugins | open a screen (`esc` back) or run what the plugin added |

Keys only move around:

| Key | What it does |
|---|---|
| any key | skip the ouroboros splash (except `ctrl+c`) |
| `enter` | send |
| `shift+enter` | a new line |
| `↑` `↓` | bring back what you entered while the UI is open, messages and commands, the latest first. Below the latest is what you had typed, and below that an empty input, from which `↑` brings it back again. A line you change is what you type from then on. In an input of several lines they move between its lines first |
| `↑` in an empty input | take back the last message that is queued, see [Sessions](#sessions); with nothing queued, bring back what you entered |
| `ctrl+v` | paste the clipboard: a picture as the path of its file, see [Pictures](#pictures) |
| `ctrl+s` | the conversations, like `/session`, but what is typed stays with the conversation shown |
| `ctrl+k` | the keys of the chat in a dialog, like `/keys`, but what is typed stays in the input |
| `ctrl+t` | back to the thread, like `/thread`; what is typed stays with its conversation |
| `ctrl+r` | what the model thought above the words of its round, shown or hidden again |
| `ctrl+j` | the jobs of the conversation shown, and the services below them, in a list on the right, and the chat and the input narrower for it; a job too long for a line goes on in the next, and while the list is shown the footer says `ctrl+j hide`. Again: the list is hidden, and the jobs are named in the line of the wait below the chat again |
| `pgup` `pgdn` | scroll the chat |
| `esc` | on a screen: back to the chat. In the chat: close the list of commands with the command typed; without one, stop the answer on its way in the conversation shown and the tasks its agent delegated, like `/cancel` |
| `ctrl+c` | quit, everywhere |

### Pictures

A picture gets into a message as the **path of its file**, in double quotes, which the agent reads with its
tools. Nothing is sent by pasting: the path is typed where the cursor is, and the message is yours to send.
The input and the chat show each picture as a label of its own, `[Image_1]`, `[Image_2]` and on, numbered within
the conversation; what the agent is sent has the paths.

- **A file**, dropped on the terminal or pasted as text: the terminal writes its path, plain, in quotes, with
  its spaces escaped or as a `file://` URI, one or several, and the chat types each as `"/the/path.png"`. In
  the quotes a `"` is `\"` and a `\` is `\\`; a space is a space. Only text that is nothing but the paths of
  files that are there and pictures by their extension (`png` `jpg` `jpeg` `webp` `gif` `bmp` `tif` `tiff`
  `heic` `avif` `svg`) is changed: any other text is pasted as it is.
- **A picture that is no file**, like a screenshot on the clipboard: `ctrl+v` reads it from the clipboard of
  the system and keeps it as a file, whose path it types. A terminal that pastes such a picture as nothing
  does the same. A file copied in a file manager is typed as its own path. Without a picture, `ctrl+v` pastes
  the text of the clipboard.

The pictures that are kept go into `pasted/` in the [workspace](#profiles) of the profile, where its agent
works: every profile has its own. They are named after the time, only you can read them, and they stay until
you or the agent remove them.

What works depends on the desktop, as a program in a terminal is handed text alone:

| Where | A dropped or pasted file | A picture on the clipboard (`ctrl+v`) |
|---|---|---|
| Linux, Wayland | any terminal that pastes the path | with `wl-paste` (wl-clipboard) |
| Linux, X11 | any terminal that pastes the path | with `xclip` |
| macOS | any terminal that pastes the path | with `osascript`, which macOS has; as PNG |
| over SSH, in tmux or a container | if the terminal pastes the path, and the file is on the host of ouro | no: the clipboard is on another host |

Without the tool, `ctrl+v` pastes the text of the clipboard as before.

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
- **Several conversations at once.** The UI opens with [the thread](#the-thread), which keeps your topics apart
  by itself. `/new` starts a conversation of its own, outside the thread, and leaves the one before going on.
  `/session` (or `ctrl+s`) lists the conversations of the node in a dialog over the chat: each with its
  title and when something last happened in it, and behind those whose answer is on its way a small
  animation. The one shown is marked. What you type filters the list as you type it, by title and by the
  name of the peer; `backspace` takes a letter back, `esc` the whole filter and only then closes the dialog.
  `enter` shows the one selected, with an answer on its way as usual. When an answer
  comes in in a conversation that is not shown, the footer says so (`answered in “…” · /session`). What you
  typed stays with the conversation you typed it in; `ctrl+s` takes you to the list without clearing it.
- **Titles.** A conversation is named for its topic. With each of your first three messages, while the
  answer runs, the model of the node is asked for a title of a few words from what you and the agent said
  so far, without the calls of tools. A greeting alone has no topic yet: then it is asked again with the
  next message. Until there is a title, the start of your first message names the conversation. The title
  is shown at once and kept in the archive; the call is part of no conversation and not in the log, and asks the
  model to think little (`low`), whatever the effort of the conversation. An answer that is in before the title
  waits for it, 20 seconds at most.
- **Effort per conversation.** `/effort` sets how much the model thinks before it answers (`auto`, `none`,
  `low`, `medium`, `high`, `max`) for the conversation shown, from its next call of the LLM on, also in the
  answer on its way. Its dialog shows a slider, and above it a net of nerve cells that thinks as hard as the
  effort: it grows when the dialog opens, and grows on or dies back in half a second as you move the slider, with
  the cells that grow anew placed at random. A few cells make a simple tree, more of them a mesh, and signals run
  through it, more, faster and hotter for each effort more. Nothing runs
  through it for `none`, and for `auto` it thinks more and less in turn. Other conversations keep theirs, so two can answer at once with different efforts. What
  you choose is also the effort of the node (`effort` in `config.json`): a new conversation, and one that never
  had its own, starts with it. A conversation keeps its effort in the SQLite session metadata. The header
  shows the effort of the conversation shown. Which efforts a model takes comes from
  [models.dev](https://models.dev): for a model it lists, `/effort` offers those alone, by the names its API
  knows (`low`, `high`, `max` for DeepSeek V4.1 Flash at OpenCode Go, `xhigh` for OpenAI), and only `auto`
  for one it lists without any. ouro fetches the list from `https://models.dev/api.json` at most once a day,
  in the background, and keeps what it needs of it in `catalog.json`; `OURO_MODELS_DEV` names another source,
  `off` fetches nothing. A provider is found there by its ID (OpenCode Go, ChatGPT as OpenAI) or by its
  endpoint. For a model it does not list, `/effort` offers `auto` to `max`, and an API that refuses one is asked
  with the nearest it takes, and at last with none: then the header says what it took at the last call
  (`effort max → high`, `→ not taken`), and so does the line of the call in `ouro.log`. An API that takes an
  effort and does nothing with it cannot be told apart. Peers are answered with the model's own effort.
- **Two front ends** see the same: the UI lists the conversations `ouro chat` had too.
- `/cancel` stops the answer on its way in the conversation shown, and so does `esc` in the chat: the call
  of the LLM and the tools that can be stopped end, the conversation stays as far as it got, and the next
  message goes on from there. What a tool already did is not undone. The [tasks](#tools) the agent
  delegated in it stop too, also once its answer is over, and are not reported. The other conversations go
  on. Stopping the node stops every answer and every task.
- **Steering.** What you send while an answer is on its way is queued, and shown as that below the
  conversation. The agent takes it as soon as the tools it waits for have run, as a message of yours after
  their results, or, if the answer is given before, as your next message. Either way it goes at the end of
  the conversation, so what the provider cached of it stays. `↑` in an empty input takes the last queued
  message back into the input, and it is not queued any more.

The node **keeps the sessions** in its [archive](#history), so that they outlive it: after `ouro stop`, a
crash, a reboot, `ouro update` or a build of `rebuild`, `/session` lists them as before, and you go on with
one where you left it.

- **One place.** Every answer is archived once it ended, answered, failed or stopped, in
  `history/ouroboros.sqlite`. The node does so with or without the `history` plugin, which only gives
  your agent the tools to search it. A crash costs at most the answer on its way.
- **Loaded when needed.** A session from before the node started is loaded once it is shown, asked something
  or its context looked at: what happened in it, and what your agent remembers of it, cut like any
  conversation (see Memory in [How it works](#how-it-works)), with an answer that failed or was stopped
  closed as it was. The list reads only its first message and when it was last used. What only the UI was
  told is not archived; an answer that failed is loaded without why.
- **What the agent is told.** With the first message after the node started anew, the agent is told so, and
  how long ago the last answer was: what tools gave back before may be outdated. You do not see that note.
- **Nothing is deleted by itself.** `ctrl+d` pressed twice on `/session` deletes the session selected,
  unless an answer is on its way there: from the archive, and what your agent remembers of it. Deleting is
  not a letter, since every letter filters the list.
- **The conversations of peers.** `tab` on `/session` lists how peers talked with your agent, each with the
  name of the peer (as your contacts name it) and its first message. `enter` shows one, only to read: you
  cannot write there. `ctrl+d` pressed twice deletes it, and your agent forgets it. They start anew for the peer
  after a restart; only yours go on.

#### The thread

The thread is one conversation with your agent about everything. The terminal UI opens with it, every Matrix
room talks in it, and `/thread` or `ctrl+t` goes back to it from a conversation of its own. The header marks the active thread with a light background. It reads like any chat: you
write, your agent thinks, and it answers. Behind it, each topic is a
conversation of its own, and several of them work at once. While one works on the login, you can talk about the
photos, and the answer about the login comes in when it is done. You are not shown that this happens: the thread
says nothing of the conversations it uses, and while any of them works for it, the line below the chat says that
your agent is thinking.

- **Handing on.** The agent of the thread does none of the work itself. For each of your messages it decides which
  topic it continues, or whether it is a new one. It hands the message to the conversation of that topic with
  `session_send`, or starts one for it with `session_new`. Then its answer ends without a word, as that
  conversation answers. A message about several topics hands each its part. When it cannot tell which topic you
  mean, it asks you, about the topic. It answers itself only what the thread answers already, like what was said
  before. Its words along the way and the calls with which it hands on are not shown; `/context` shows them.
- **Which conversations.** The agent of the thread is told which conversations there are to hand your messages
  to: those in which something happened in the last 24 hours, at most 30, the latest first, each with a handle of
  8 characters of its ID, its title, its project where it is not that of the thread, and whether it is working or
  asks you something. Older ones are left out, and the thread refuses to hand anything to them.
- **Little to read, and cached.** The agent of the thread only hands on, so it is sent little, and thinks little
  (`low`) unless you set its `/effort`. What it was sent stays as it was, and every call begins with all the call
  before sent, as the caches of providers want it: the list of the conversations comes before your message only
  when it changed, or after 6 answers, and stays; of what a conversation answered the thread gets the first and the
  last 300 bytes, as that conversation remembers all of it; your messages come without where you wrote them. What
  is kept, in the archive and in the chat, stays whole: only what is sent is shorter. With DeepSeek V4.1 Flash at
  OpenCode Go, 18 messages about three topics sent 4 to 10k tokens a call, of which 86 to 98% were cached, but
  for the first after a start and the one after a cut of the conversation; `/context` shows what is sent.
- **The last 6 hours.** The terminal thread view also shows only this rolling window; older
  messages stay in the archive, and a thread with no recent messages opens empty.
  The agent of the thread is sent only what happened in it in the last 6 hours, from the
  first message of yours or of ouro that is not older, and the last list of the conversations before that. What is
  older it finds with `search_thread`, which searches the whole thread in the archive, when you refer to it. Each
  time the 6 hours move past an answer, the next call finds nothing in the cache, as it begins anew.
- **What a conversation gets.** A message the thread hands on is a message of yours there: an answer to it starts,
  the answer on its way takes it as it would take what you queued, or it answers the question the conversation
  waits for. It is attributed to where you wrote it in the thread. It is your words as you wrote them: without
  words of its own, `session_send` and `session_new` hand on what you said since the last answer of the thread.
  The agent of the thread writes only the part of a message that belongs to the topic, or what your words mean
  where only the thread explains them, like your answer to a question it asked, in your voice. It never writes
  about you, the thread or other topics, nor sums up what the conversation said itself, which remembers it.
In the terminal UI, new complete answers arriving in the visible thread unfold quickly in small
word-like chunks, rather than appearing all at once. Reopened history is not animated, and answers
already streamed by the model keep their normal streaming effect.

- **What comes back.** The answer of a conversation that took a message the thread handed on is shown in the
  thread as the answer of your agent, and an answer that failed as one that failed. What such an answer asks you
  on its way is asked in the thread, one question at a time, and what you say there next answers it; answered in
  that conversation itself, it is over in the thread too. The agent of the thread is told all of it for its next
  answer, and keeps it as what was shown, so that a restart does not show it anew. What you write in a
  conversation yourself does not come back to the thread, and neither does an answer that was stopped. While another conversation is shown, the footer says that the thread answered
  (`answered in the thread · /thread`).
- **Stopping.** `esc` and `/cancel` in the thread stop its answer and what the conversations work on for it.
- **Where new ones work.** A conversation the thread starts works in the directory the agent names, or else in the
  project of the thread. The first time a UI shows the thread, the thread takes the directory the UI started in as
  its project, unless it answers just then and keeps the one it has, which the footer says; `/cd` there changes
  it, and the header shows it after `· thread`.
- **Gateways.** A gateway talks in the thread too: every Matrix room does, so there is no `/new` there, see
  [GATEWAYS.md](../GATEWAYS.md). What is written in a room and in the terminal UI is one conversation. The terminal
  UI shows what you wrote in a room as your messages; a room is sent the answers and questions of the thread, and
  what you wrote elsewhere, as `You, in the terminal:` and your words, since the bot cannot write as you.
- **It is a session of the node.** The thread has the ID `thread`, is kept in the archive and goes on after a
  restart like any conversation, shown as it was. `/session` does not list it, it is not named for a topic, and its
  agent has the two tools of the thread alone. The conversations it starts are listed there as any other.

### Profiles

A node may hold several agents, its **profiles**: Terra and Luna, say, side by side on one node. Each is an
agent of its own, with its own key and so its own ID, its name, description, model, tools and what they may do,
its contacts, sessions, history, proposals and workspace. What they share is the process of the node, with its
port, its socket and its log, and the [accesses](#providers-and-accesses) to the LLMs: each picks one of them
and a model of its own.

- **Adding one.** `ouro profile add Luna` adds a profile, while the node is stopped: it has a new key and
  nothing of another profile, and it talks through the access and with the model of the first one until you
  pick others. The node you had is the first profile and keeps all it had, where it had it; every further one
  is in `profiles/<ID>/` of the directory of the node. `ouro profile` lists them, with their IDs and models,
  and `ouro profile rename Luna Selene` renames one. Two profiles never have the same name, whatever the case
  of its letters. A profile is not removed by ouro: its directory holds its key and its conversations.
- **All of them run.** The node starts every profile and runs it until it stops, whichever the UI shows. An
  answer of one goes on while you look at another, and keeps the node running; a new build of `rebuild` waits
  until no profile answers, and no profile takes a new message meanwhile. A front end that lets go of the node
  it started stops it once no profile answers.
- **The UI shows them all.** `shift+tab` shows the next profile, with all it showed: its conversation, what
  you typed there, its screen. The name in the header morphs into the new profile's name in a short wave of
  dots; another switch continues from what is on screen. The footer says which profile is shown. `/settings`,
  `/model`, `/suggestions` and the rest are of the profile shown. `ouro` opens on the first profile,
  `ouro --profile Luna` on Luna.
- **The command line names one.** `ouro --profile PROFILE COMMAND` runs a command for the profile that
  `PROFILE` names: its ID, the start of its ID that no other one has, or its name, whatever the case of its
  letters (`--profile=PROFILE` too). While there are several, a command that is about one of them fails
  without it, so a script never talks to one it did not mean: `ouro chat`, `ouro ask`, `ouro status`,
  `ouro start --setup`. What is about the node needs none: `ouro start`, `stop`, `serve`, `update`, `list`,
  `profile` and the UI. Which profile the UI showed last does not count. With one profile nothing changes.
- **Peers see each of them.** Every profile answers discovery as a node of its own, with its ID and name,
  on the port of the node, and has a card of its own there. A peer names the profile it writes to by its key,
  in the name of the server of the TLS handshake (`<ID>.ouro`): the node answers with the certificate of that
  profile, which the peer pins, and hands the request to it, which checks that it is signed for it. A peer
  that names none, as an ouro from before there were profiles does, reaches the first profile; such an ouro
  cannot write to the others. A profile trusts another of its node no more than any peer: they find each
  other, and talk once both of you agreed, as two nodes do. Whom one trusts, another does not.
- **Each works in its own place.** A command of `bash` that names no directory starts in the workspace of
  its profile, `<directory of the node>-workspaces/<ID>/`, next to the directory of the node, whether the
  node has one profile or several. The agent is told so, and that what you work on is elsewhere: speak of
  a project without its path and it looks for it or asks. The tools of one profile neither read nor change
  what another keeps: its directory, or the files of the first one, and its workspace.

## Updates

`ouro update` fetches the newest `main` from the repository the binary
was built from, builds it with `install.sh` and replaces the running binary. A running node is
restarted: an answer on its way is stopped; the [sessions](#sessions) are kept.
It needs `git` and `make`; sources and build live in `~/.config/ouroboros/update/`.

Your agent has the tool `rebuild` for the other direction: it builds the checkout the binary was built from
(`make install` records it) with its files as they are now, committed or not. Only
when the build passes, the binary is replaced; otherwise nothing is installed and the agent gets
the end of the output. It runs no tests unless it is told which: with `test`, like `rebuild({test: ["./internal/tui/"]})`,
it first runs the tests of those packages, whole, as it builds, and in the environment of the build, without the variables of the node. If one fails, nothing is
built. A package is a path in the checkout that starts with `./`, never a flag of `go test`.
That is how a change your agent made to the plugins of ouro
with its tools for files takes effect. The tool runs in the node, which cannot stop itself while the answer is
on its way. So the node starts anew with the new build once the agent has answered and no other answer is on
its way; it takes no new message until then. It hands over to the new build in the same process: the lock on
`ouro.pid` and the socket stay open across it, so no other node starts in between, and a front end that
connects meanwhile waits until the new node answers. The new node finds the [sessions](#sessions) in the archive,
and what the agent remembers of them. The terminal UI starts anew as
well, on the same terminal and without the splash, and shows the same conversation. What the agent built it
can use only there, so with `continue: true` the conversation goes on by itself in the new build: the agent is
told to go on, in place of a message of yours, once. The tool is for you, and
for a peer only where its own list in `tools.peers` names it, see [Peers you trust to build](#peers-you-trust-to-build):
`"*"` never gives it to a peer, and listing it in `a2a` is an error.

**ouro grows with its tasks.** Where a task needs something ouro cannot do, your agent weighs building it
into ouro against a workaround, builds it when that is the better way, also for the tasks to come, and tells
you what it changed. Where ouro held it back otherwise, by a tool that is slow or misleading, it says so at the
end of its answer, with the change it would make. That is `"self_improve": "build"`, the default. With
`"self_improve": "propose"` in `config.json` (or `Changes of ouro` in `/settings`) it proposes every change and
makes it only when you ask for it. No proposal of the agent changes the setting. Take
`rebuild` out of `h2a` if your agent should not build what it wrote. A binary installed by `ouro update` keeps
the checkout of the one it replaced.

**Back to the build before.** `ouro update` and `rebuild` keep the binary they replace next to it, as
`ouro.previous`. `ouro rollback` installs it again and restarts a running node; the one it replaces is kept in
its place, so a second `ouro rollback` goes forward again.

**`rebuild` builds as you**, with `make install`: with network, and without a sandbox. The binary is built
in `update/stage`, not in the checkout. It runs the Go that it finds first in `PATH`, in `OURO_GO_DIR` or
`~/.local/go`, or in `/usr/local/go`, in an environment the frame fixes: `GOFLAGS=-mod=readonly`, `GOENV=off`
(no file of `go env`), `GOWORK=off` (no `go.work`, also not above the checkout), `GOTOOLCHAIN=local` (no other
toolchain; the Go found has to be as new as `go.mod` asks), `GOVCS=*:off` (modules only from the proxy, no
`git`) and no `GOROOT`. `GOPROXY`, `GOPRIVATE` and the like of the environment ouro was started in hold: they
are yours.

Endpoint examples: `https://api.openai.com/v1` (default), `http://localhost:11434/v1` (Ollama, no key),
`http://localhost:1234/v1` (LM Studio), `https://openrouter.ai/api/v1`.

## How it works

| Part | Details |
|---|---|
| LLM | an OpenAI-compatible API: `GET /models` for the picker, `POST /chat/completions` for replies. The ChatGPT plan: `GET /v1/models` and `POST /v1/responses` at `api.openai.com`, with the token of the login. See [Providers and accesses](#providers-and-accesses) |
| A2A | `GET /.well-known/agent-card.json`, JSON-RPC `message/send` on `POST /`, over HTTPS only (TLS 1.3 on TCP 41337, falls back to a free port) |
| Channels | JSON-RPC `channel/open` on `POST /` with `Upgrade: ouro-channel/1`: the node answers `101 Switching Protocols` and the TLS connection of the request carries messages in both directions until one side closes it, see [Channels](#channels). Not part of A2A |
| Discovery | UDP probe to multicast `239.255.41.38`, the broadcast address of every network, localhost and the peers of the tailnet on port 41338; nodes answer the sender and the group, so only port 41338 needs to be open. Where no interface carries multicast (a network namespace with the loopback interface alone), a node hears the probes sent to its host; a datagram the system does not hand over, as a sandbox does with one of another host, is passed over |
| Agents | two per node, built from one blueprint, both in the node: yours answers the [sessions](#sessions) of `ouro` and `ouro chat`, the other one peers. Which tools each may use is set in `config.json`, see [Tools](#tools); at most 100 LLM calls per answer, from the 81st on with a note to the LLM of how many it has left; the LLM calls permitted tools directly with structured arguments, and may request multiple calls in one response |
| Memory | a conversation is cut in steps: beyond 80 messages or 256 KiB it is cut to the last 40 messages and 128 KiB, at a message of the owner, so that its start stays the same between the cuts and the provider can cache it; always the whole of the last answer, however many tool calls it took; up to 256 conversations. A result of a tool leaves out runs of lines (1 KiB or more) that an earlier result of the same answer has, with a line that says so. Once the results of an answer have more than 64 KiB, the LLM is told so with every call, in a message of its own after the results, which stay as they are and as the provider cached them. At the 16th call of the LLM in an answer, it is told once to stop searching and ask the user what exactly is meant, unless it knows what to do, and then to go on until it is done; the same call of a tool asked for a third time in a row is not run, and the LLM is told so. What the LLM thought is sent back within an answer; once it is at least half of what came after its start, it is left out when the answer ends, and always at a cut. ouro asks the API for the tokens of every call (`stream_options`) and gives every API the conversation as `prompt_cache_key`, so that a gateway sends its calls to the same cache. A Claude model (its name has `claude`) caches only up to `cache_control` breakpoints: ouro sets them at the system prompt and at the last 3 messages with content. An API that refuses one of these parameters (HTTP 400 that names it) is asked again without it, and not sent it again. An answer that fails or is stopped is kept as far as it got, so that asked again the agent goes on from there; why it failed is not kept; a conversation of a peer is its `contextId` together with its key, so no peer continues or reads the conversation of another. What is cut off is not lost: the [history](#history) keeps every conversation in full |
| Card | the description in the agent card is what you wrote in `/settings` (`description` in `config.json`, one line of at most 300 characters), or `Ouroboros instance named <name>` without one; anyone can read it, so nothing private belongs there. `list_peers` shows your agent the cards of connected peers. `capabilities.streaming` is `false`: in A2A it stands for `message/stream` (the answer to a message as server-sent events), which a node does not have. That peers can open channels is listed under `capabilities.extensions`; which channels is not, as anyone can read the card |
| Trust | nodes talk over TLS 1.3 with a self-signed certificate made from the node's Ed25519 key; the connection is pinned to the key the node is known by, and requests are signed for that key; a message only goes to a peer both owners agreed to connect to, and a node asks another to connect with `contact/request`, which carries no message |
| LLM | answers are asked for as a stream (`"stream": true`), so a gateway that gives up on a request without data for a while (HTTP 504) keeps waiting while the model thinks; an API that does not stream answers in one piece, which is taken as well. A completion is given up after 5 minutes without data, a list of the models after 5 minutes |
| Limits | at most 64 connection requests wait for a decision: when the list is full, the one that waited longest makes room for a new one (trusted peers are never dropped). What your agent proposes waits in the same list but never takes the place of a request: with 64 waiting it is not recorded. At most 16 changes of the configuration your agent proposed wait for a decision. The tools for files have limits of their own, see [Files](#files). One address gets 10 messages per minute from nodes that are not connected; further ones are refused with error `-32005` and leave no request (at most 1024 addresses are counted per minute, unknown nodes from any further address are refused). Against replays the node remembers every nonce for at least 4 minutes, at most 16384 per 4 minutes; beyond that it refuses requests rather than forget a nonce early. A connected peer has at most 8 channels open to a node at a time, a further one is refused with error `-32006`; a channel without a message in either direction for 5 minutes is closed; a message of a channel has at most 1 MiB; the channels of a peer that was removed from `contacts.json` or refused are closed within a second, and all channels when the node stops |
| Errors | a peer whose message your agent failed to answer is only told `the agent could not answer (error <ID>)`; the cause is in `ouro.log` under that ID, and only you (`ouro ask <your node>`) get to read it directly. The peer can ask your agent about the ID later, see [When a peer could not answer](#when-a-peer-could-not-answer) |
| Files | `~/.config/ouroboros/` – `config.json` (0600), `credentials.json` (0600, what the node signs in to its LLMs with, see [Providers and accesses](#providers-and-accesses)), `models.json` (0600, the models the accesses last listed), `catalog.json` (0600, the efforts of the models as models.dev lists them), `host-id` (0600, an opaque ID of this installation, made on first use), `contacts.json` (0600), `proposals.json` (0600), `failures.json` (0600), `ouro.pid`, `ouro.sock` (0600, see [Sessions](#sessions)), `ouro.log` (0600) with `ouro.log.1` (0600) and `ouro.log.lock`, `update/`, `history/` (0700, see [History](#history) and [Sessions](#sessions)) and `browser/` (0700, the profile of the [browser](#browser)). `config.json`, `credentials.json`, `contacts.json` and `proposals.json` are replaced in one step (temporary file, then rename), so a crash never leaves half a file; changes are serialized by a lock on `config.json.lock` / `credentials.json.lock` / `contacts.json.lock` / `proposals.json.lock`. `ouro.log` holds what the node does and [what your agent does for you](#the-log); at 10 MiB it moves to `ouro.log.1`, see there |
| Process | the node holds a lock on `ouro.pid` (`PID PORT`) while it runs. Only a locked file counts as a running node, so one left behind by a crash or reboot is ignored and never gets a foreign process signalled |

Environment: `OURO_HOME`, `OURO_PORT`, `OURO_DISCO_PORT`, `OURO_MODELS_DEV` (see [Sessions](#sessions)), and `OPENAI_BASE_URL` / `OPENAI_API_KEY` as what the
provider for OpenAI-compatible APIs offers in the setup; `DEEPSEEK_API_KEY` offers the key for DeepSeek.
`ouro start --setup` reconfigures, `ouro serve` runs in the foreground (e.g. for systemd).

Firewall (ufw): `sudo ufw allow 41337/tcp && sudo ufw allow 41338/udp`

## Tools

The agent has tools for peers (`list_peers`, `ask_peer`), for files (`read`, `write`, `edit`, `list`,
`delete`, `search`), for the [history](#history) of its conversations (`history_search`, `history_read`,
`history_sessions`), for [memories](#memories) (`memory_add`, `memory_update`,
`memory_forget`, `memory_search`, `memory_read`), for owner-only [scheduled answers](#schedule)
(`schedule_read`, `schedule_update`), one that loads a [skill](#skills) (`skill`) and, for you alone, three for the configuration (`get_config`, `propose_config`,
`withdraw_proposal`), one that runs commands ([`bash`](#commands)), one that uses the web ([`browser`](#browser)), one that uses the apps of your computer ([`computer`](#computer)), one that builds and installs its own
source ([`rebuild`](#updates)), one that asks you a question (`user_question`, see below) and one that hands tasks to
helpers (`delegate`, see below). It calls them directly with structured arguments, see [Structured tool calls](#structured-tool-calls).
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
| `a2a` (agent to agent) | a connected peer asks your agent | no tool |
| `peers` | the peer whose ID names a list there asks your agent: its list, in place of `a2a` | `a2a` for every peer, see [Peers you trust to build](#peers-you-trust-to-build) |
| `protected` | a tool for files is used, by anybody | the list above; `[]` protects nothing |

An entry of `h2a` and `a2a` is the name of a tool, or an object with the name as `"tool"` and **settings** for
that tool next to it. `"*"` stands for every tool, `[]` for none; an object next to `"*"` narrows that one tool.
A name that is not a tool, a tool that is listed twice, a setting the tool does not have and a setting of the
wrong type are errors: in `a2a` and `peers` the node does not start, in `h2a` your agent does not answer and
says why. A change holds at once: your agent
and the node read `config.json` with every message and build their agents anew when it changed, without a
restart. A node that runs keeps answering peers as before while the file cannot be used, and says why in
`ouro.log`.

### Memories

The optional `memory` plugin gives the owner a persistent memory, separate from the
conversation archive. `memory_add` records a fact or preference with its source conversation;
`memory_update` changes an entry only at its expected version, and `memory_forget` removes it
from the active store and its search index. The agent saves what the owner asks it to remember,
or a lasting fact the owner told it, never what a tool, a web page or a peer said; stored content
is data, never a higher-priority instruction. Peers cannot use these tools.

ouro also learns by itself: the [review](#the-log) of an answer with signals may remember up to 3
facts, of the owner's message or the follow-up, or a way that worked after another had failed. It
does not keep a failure of the environment, that a tool does not work, an attempt that did not work
out, or what concerns only one task. A memory of the same key in its scope is replaced, as what was
said last holds. What it keeps is told in the chat at once, and `/undo` takes it back: see
[Terminal UI](#terminal-ui). The log says `review: remembered "…"`.

Memories are of three scopes, and of three layers. **Core** memories are short facts about who the owner is and how
they want answers: they are in the system prompt of every answer to the owner, the oldest first,
and all of them together have at most 2,000 characters. A write that would make them more fails;
the agent then merges or shortens core memories with `memory_update`, and what the review would
keep there is kept as a global memory instead. **Global** memories are available across projects,
**project** memories with the trusted selected project directory; both are found with
`memory_search`. Other profiles remain separate.

The **core** goes into every prompt. A **topic**, a conversation other than the thread, is given
more when it starts: up to 5 memories of its project at once, the most used first, and a line of the
keys of the others, so that the agent knows what there is to search for. A moment later a small call
of the LLM picks words from the first message, and up to 5 memories that have any of them are given
from the next answer on. From then on the prompt of the topic stays the same, so that the provider
can cache it; what the agent looks up later comes as the result of a tool. The thread is given only
the core. Everything else is the **archive**, which only `memory_search` and `history_search` reach.

The review may remember a state that will change, as what the owner works on now, for some days,
at most 30: then it expires. Each memory counts how often a topic was given it and how often the agent
read it or found it by words. One that was given 10 times and never used is given to no topic anymore;
at 20 it is archived, and so is one that expired. Nothing is deleted: `memory_search` still finds an
archived memory, marked as such, and remembering it again takes it out of the archive. Once the owner
left for 2 hours, the memories are tidied, and at most once a week the LLM looks for memories of one
scope that say the same and merges them into one, of which the newer holds: the others are archived.
The log says `memories: 2 archived, as they expired or were never used` and `memories: 3 merged into "…"`.

`memory_search` retrieves short ranked full-text matches; `memory_read` reads an entry by ID.
The tool description asks the agent to search when a request may depend on earlier preferences,
agreements or project conventions, and to search rather than guess when in doubt. This is a
tool-driven lookup, not a guarantee of automatic recall or a semantic/embedding search.

Sessions, history and memories share `history/ouroboros.sqlite`. Forgetting a memory does not erase
its earlier mentions from conversation history. Deleting a conversation does not silently delete
independent memories derived from it. Legacy JSONL backups may still contain old conversation text.

### Schedule

The optional `schedule` plugin gives you two owner-only tools: `schedule_read` and
`schedule_update`. Peers cannot use them, even through an explicit peer tool list or `*`.
Jobs live in your profile's `schedule.json`, survive rebuilds and restarts, and run while
that profile's node is serving. Each run is a new model answer and has the usual cost;
it is not a free alarm or a shell cron job.

Use `schedule_update` with `action: "create"`, a short `name`, `text` (at most 4 KiB),
and exactly one of:

- `at`: a future RFC3339 timestamp, or `YYYY-MM-DDTHH:MM` in the node's time zone.
  Give an explicit UTC offset for an ambiguous daylight-saving time.
- `cron`: five fields (minute, hour, day of month, month, weekday), with `*`, lists,
  ranges and steps. For example, `0 9 * * 1-5` runs at 09:00 on weekdays.

The job saves the current conversation's project directory and the machine's IANA
time zone. Recurring jobs keep their local wall-clock time across daylight-saving
changes. There may be at most 32 jobs; recurring runs must be at least 15 minutes apart.
The response includes the ID and up to three upcoming runs, with weekday and zone.
`schedule_read` lists jobs; with `id`, it reads one job and its last ten run records.
`schedule_update` with `action: "update"`, `id` and changed fields edits a job; `at`
and `cron` replace each other. Set `paused: true` to pause, or `false` to resume.
`action: "remove"` takes only `id` and removes the job; it does not cancel an already
running answer. The saved directory and zone cannot be supplied through these tools.

Each run has its own session, titled with the job's name, in its saved project.
Its input is generated scheduled work, not a fresh message from you. The session
routes its result into your thread, including connected front ends such as Matrix.
`ask: "thread"` is the default: questions and permission requests are relayed to the
thread, and the run waits for your answer. With `ask: "never"`, questions receive an
unattended answer and tools requiring permission are refused, not run silently.
A run is limited to one hour. If a job is due again while its previous run is still
active, that occurrence is recorded as `skipped` rather than overlapping.

When the node starts after downtime, a job up to 24 hours late runs once with its
planned time in the generated request; a recurring job then resumes its regular
schedule rather than replaying every missed occurrence. Jobs more than 24 hours late
are recorded as `missed`, logged and reported as generated notifications in your thread,
without executing the stored job text. The thread may use a model answer to explain
the notification; no job session is started. Notification acceptance is best-effort:
failures are logged, and a restart after the durable claim can lose an undelivered
notification. A claim is persisted before dispatch,
so a restart does not repeat it; unfinished claims are marked `interrupted` on the
next process start. Pausing preserves the planned time, so resuming can also produce
a late or missed run.

### Files

| Tool | Does | Limits |
|---|---|---|
| `read(path, offset?, limit?, column?)` | returns the text of a file; the whole file comes as it is, a page with a continuation note; follow its `offset` and, within a long line, its `column` (bytes to skip, default 0) | 2000 lines or 24 KiB of text per page, plus the continuation note; a line has less than 256 KiB, longer ones are not read; what is not text (UTF-8 without NUL) is refused |
| `write(path, content)` | creates the file or replaces all of it, in one step, and creates missing directories | 1 MiB |
| `edit(path, old, new, all?)`, `edit(path, from, to, new)` | replaces `old` by `new`; `old` has to match exactly, and exactly once, or with `all: true` every time it occurs, which returns the lines it changed; or the lines `from` to `to` by `new`, with no text to match; returns the changed lines with 3 lines around them, at most 50. An `old` that does not occur is answered with where it parts from the file: the line of the file and the line of `old`. In a file whose lines end in `\r\n`, an `old` with `\n` matches, as `read` shows the lines, and `new` gets their ends | the file has at most 1 MiB, before and after |
| `list(path)` | the entries of a directory: directories end in `/`, files come with their size | 1000 entries; what the tool may not read is not listed |
| `delete(path, recursive?)` | removes a file or an empty directory; one with content only with `recursive: true` | stops, with nothing deleted, before anything it may not delete |
| `search(path, pattern, glob?, context?)` | the lines that match a regular expression ([RE2](https://github.com/google/re2/wiki/Syntax), `(?i)` ignores case) as `path:line: text`, in a file or in every file below a directory; `glob` keeps to file names like `*.go`, and with `!` in front leaves them out, like `!*_test.go` | 200 lines of at most 300 bytes, 10000 files, 100000 files and directories looked at (also those the glob leaves out); hidden directories below the path, files of more than 1 MiB, what is not text and what the tool may not read are left out; with `context` the lines around each match too, as `grep -C`, at most 30 places of at most 61 lines each: matches that go on beyond start the next place. Beyond 12 KiB it says only how many matches are in which file. A file named itself is searched up to 64 MiB, like a log, and one that is not searched says why. A line that ends in `\r\n` matches without the `\r` |

A path is absolute or starts with `~`, your home directory. For your agent it may also be relative: it then
starts in the workspace of the [profile](#profiles), where its commands start (`.` is the workspace). A peer
names every path in full. Each of the six tools takes the settings `allow`
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
   - The source of ouro is not kept: your agent may change all of it, the core and how it is built too
     (`go.mod`, `Makefile`, `internal/cli/` and the rest), and [`rebuild`](#updates) builds what it changed.
   - The Go that [`rebuild`](#updates) builds with is read and never changed, and no `go` is put where it looks
     for one: the installation of the `go` it finds, a file `go` in every directory of `PATH`, `OURO_GO_DIR` or
     `~/.local/go`, and `/usr/local/go`. `rebuild` runs that `go` with network.
   - `/` and your home directory are never deleted.
2. **`protected`**: paths no tool for files touches, for you and for peers. The default holds the usual places
   of keys and credentials and nothing else: profiles of browsers, files like `.env` and whatever else is
   secret on your machine are not in it. Add what you need. A hard link to a file that is protected, or denied, is
   refused as that file is; one to a file in a protected directory is not known as such, and neither is one to
   a `config.json` the node has written anew since, which keeps what the file held then. A configuration from before there were these
   tools has no `protected` and gets the default; `"protected": []` is your decision to protect nothing.
3. **`allow` and `deny`** of the tool in `h2a` or `a2a`. `deny` wins over `allow`. They hold for the tool they are set for alone: a file that `read`
denies is still found by `search`, listed by `list` and quoted by a failed `edit`, unless those deny it too, or it is `protected`. Without `allow` a tool
   goes everywhere for you and **nowhere for a peer**: all of the file system for a peer has to be written
   out, as `"allow": ["/"]`.

A path is checked where it leads, with every symbolic link and every `..` resolved, and with every name as its
directory has it, and the tool then works on that path: no link out of a shared directory leads around a rule, and
on a file system that does not tell names apart by their case, as that of macOS, `~/.SSH` is `~/.ssh` for the rules
too. A refusal names the rule, so that your
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
[`rebuild`](#updates) your agent can change any part of ouro, the rules above among them, and install the
result. The build runs as you, with network, and so does the build that is installed. Nothing checks that a
build is harmless, or that it works.

### Commands

`bash(command, dir?, timeout?)` runs `command` with `/bin/bash -c` in `dir` (absolute, below `~` or relative to the
workspace; the workspace of the [profile](#profiles) without it), for your agent, and for a peer only where its own
list in `tools.peers` names it: `"*"` never gives it to a peer, and listing it in `a2a` is
an error. It returns `exit N` and below it what the command printed, its output and its errors as they came;
of more than 64 KiB the first and the last 32 KiB, with how much was left out between them. A status other
than 0 is a result, not an error. Every call starts anew: `cd` and variables do not carry over.
A command runs with `pipefail`: a pipe fails when any of its commands does, so that `go test ./... | tail` is
`exit 1` when a test failed. Below the output bash adds a note where the status misleads: `exit 141` for a
command a pipe ended early, as `yes | head` does, and, for `exit 0` with output that says tests failed (of
`go test`, pytest or jest), that what came after the tests hid it.

| Limit of a command | Value | Beyond it |
|---|---|---|
| time | 60 s, or `timeout` seconds | the command is stopped with everything it started, and what it printed so far comes along with the error |
| input | none | it reads nothing, as from `/dev/null` |
| environment | `PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `LANG`, `LC_*`, `TERM`, `TMPDIR`, `TZ`, those of the desktop (`DISPLAY`, `WAYLAND_DISPLAY`, `XDG_RUNTIME_DIR`) and the variables of Go (`GOPATH`, `GOROOT`, `GOCACHE`, `GOMODCACHE`, `GOPROXY`, `GOPRIVATE`, `GOFLAGS`, `GOTOOLCHAIN`), and those named in `env` | the rest of the environment of the node, its keys among it, is not passed on |

**A command runs as you, in no sandbox**: with network, every file you reach, the secrets of the node and the
source of the core among them, and your desktop, so that a program with windows, like a game started with
`godot --path .`, opens on the screen. The guard of the [tools for files](#files) checks only the directory a
command starts in. `bash` and `service` run nothing for a peer: there is no sandbox to hold its commands.

**What a command leaves running is not stopped.** A server started with `&` or `nohup` runs on once the command
ended, unseen and with nobody to stop it; when its time is up or it is stopped, its group of processes is
killed.

**What has to run on runs as a job.** A server the agent tests against, a watcher, a long build: `bash` with
`"background": true` starts the command as a job of the answer and returns its number at once. The agent
goes on, and looks at the job when it needs to:

| Call | Does |
|---|---|
| `{"command": "npm run dev", "background": true}` | starts job 1; with `timeout`, it runs that many seconds at most |
| `{"job": 1}` | whether it runs, and what it printed since the agent last looked (the start and the end of it, as of every command) |
| `{"job": 1, "timeout": 30}` | the same, once it ended or 30 seconds passed |
| `{"job": 1, "stop": true}` | stops it, with everything it started |

A job ends with the answer at the latest: given, failed or stopped with `/cancel`. At most 8 run at once. While
an answer is on its way, the chat shows its jobs after what the agent waits for, as
`alpha is thinking · job 1 · npm run dev`, until they end; front ends get them as the session event `jobs`.
The tasks the agent [delegated](#tools) come as the event `tasks`, and the report of one that ended as `task`.
`/jobs` lists the jobs of every answer on its way, and stops
the one you select: the agent learns so once it looks at it. A front end of its own asks the socket of the
node: `GET /jobs`, and `POST /stopjob` with `{"id": …, "n": …}`.

**What is to run past the answer is a service.** The tool `service` keeps a command running after the answer
that started it, like a dev server, a watcher or a mock API you use later; bash stays for what ends, and for a
server the agent tests against while it answers.

| Call | Does |
|---|---|
| `{"action": "start", "name": "web", "command": "npm run dev", "port": 5173}` | starts it, and returns once its port answers, with what it printed |
| `{"action": "status", "name": "web"}` | whether it runs, until when, and its last lines |
| `{"action": "logs", "name": "web", "lines": 100}` | more of its last lines, kept up to 64 KiB |
| `{"action": "stop", "name": "web"}` | stops it, with everything it started |
| `{"action": "list"}` | the services of whom the conversation is for |

A service runs as bash does, in no sandbox, with the settings of `service` in the configuration, which are those
of bash. It ends when its command ends, when it is
stopped, when its time is up (`hours`: 8 by default and 24 at most for yours, 1 and 4 for a peer's), or when the
node stops or is rebuilt: never later. You have at most 4 at once. A service is known by its name, of
small letters, digits and `-`; a peer starts none, as with bash. You see and
stop all of them, below the jobs, in `/jobs` and in the list of `ctrl+j`; a front end of its own asks the socket
of the node: `GET /services`, and `POST /stopservice` with `{"id": …}`.

Nothing a service starts outlives it. It runs as a group of processes of its own, which is asked to end and
killed 3 seconds later; what it starts is followed in the table of the processes every 0.1 seconds, so that a
process that left the group, with `setsid`, is killed too, and on Linux every process of it has its tag in its
environment, by which it is found even if it was never seen. It watches a pipe the node holds: when the node is
killed or crashes, the pipe closes, and the service ends its group itself; what left the group is noted in
`services.json` of the profile, and killed when the node starts again, known by its number and when it started.
On macOS a program that turns itself into a daemon, forking twice within a few milliseconds and leaving the
group, escapes that: it is not seen in time, and macOS shows the environment of no other process.

```json
"h2a": ["*", {"tool": "bash", "env": ["GITHUB_TOKEN"]}]
```

| Setting | Means | Without it |
|---|---|---|
| `env` | the names of further variables of the environment it gets | none |

### Structured tool calls

The model calls each permitted tool directly by name, with a structured JSON object
of arguments matching its schema. It can request multiple tool calls in one model
response; each call has its own result. For example:

```json
{"name": "search", "arguments": {"path": "internal", "pattern": "func main[(][)]"}}
{"name": "read", "arguments": {"path": "README.md", "offset": 1, "limit": 40}}
```

These are separate calls, not a JavaScript program. When a call depends on an earlier
result, the model reads that result before requesting the next call. Tools retain
their own bounded results, continuation notes, access checks and cancellation rules.
The configured `h2a` and `a2a` lists determine which tools each agent can call; a peer
with no permitted tools cannot call tools. Every call and its result are part of
the conversation and available to the archive and log. Keep reads focused, use
small excerpts for research, and follow continuation notes only when more is needed.

### Web search

`web_search(query, limit?, max_bytes?)` returns source URLs and excerpts (at most 24 KiB, five
results by default, at most ten). Set `max_bytes` (128–24576, default 24576) for a smaller
successful response, including provider notices and the truncation note. This counts UTF-8
bytes, not characters; diagnostic errors are outside this budget. The search backend is a
tool setting, not an argument the agent chooses for each query. Change it in `/settings` under the settings of `web_search`, separately for
your own conversations and any peers allowed to use it. Existing configurations keep using
Exa's free, keyless endpoint with DuckDuckGo/Brave fallback.

For a self-hosted SearXNG instance, use these tool settings (or the corresponding entry in
`tools.h2a` in the profile configuration):

```json
{"tool": "web_search", "provider": "searxng", "endpoint": "http://search.lan:8080/search"}
```

Enable JSON output in SearXNG (`search.formats` must include `json`). Queries are sent to
the configured instance. An explicitly configured self-hosted search does not fall back
to a public service when it fails. Search results are untrusted page content, never
instructions; open the source URLs to verify important claims.

For feasibility checks, start with a few results and small text excerpts (about 2 KiB
per source). Request more only where a specific uncertainty remains. Use bounded
results and continuation notes to keep research focused.

### Browser

`browser` uses the web as a person does, for pages that `bash` with `curl` does not get far with: those that are
built by their scripts, that you sign in to, where you click and fill in forms. By default it drives a
local browser of the Chromium kind, the first one it finds of Chromium, Google Chrome, Brave and Microsoft
Edge, and runs without a window unless asked. A hosted backend can be selected in the tool settings;
the profile, window and local-process details below describe the default local backend.
It takes `action` and what that action needs:

| Action | Does |
|---|---|
| `open(url, new_tab?)`, `open(element, new_tab?)` | goes to a page of `http` or `https` (a URL without one gets `https`), or to where link or image `[n]` leads, waits until it loaded, at most 20 s, and returns it as `read` does; with `new_tab` in a new tab, which is the current one then. An image opened so is shown alone, as the browser shows a picture |
| `read(offset?, max_bytes?)` | the page as text, with headings, lists and what is hidden left out; what can be acted on is in it as `[n]` with what it is, like `[4] link "Next" → /page/2`, `[7] input "Email" = "a@b.c"`, `[9] select "Color" = "Red" (options: Red \| Blue)` or `[12] image "A snake" 1200×800 → https://…/snake.jpg`. A picture counts from 24×24 pixels on, also one in a link, which comes right after it; of a smaller one, or one that did not load, only its text is shown, as `[image "Logo"]`, and one that is part of the page has no address: `(in the page)`. 24 KiB of page text per call by default; `max_bytes` (4–24576) lowers this, excluding headers and continuation notes. Also applies to `open` and automatic action snapshots. A note says where to go on with byte `offset` |
| `click(element)` | clicks `[n]` with the mouse, in its middle once it is scrolled into view. One that another element covers, like a banner, is clicked by script, and the answer says so |
| `type(element, text, enter?)` | replaces what `[n]` holds by `text`, and presses Enter after it with `enter`; of a select, `text` chooses the option by its text or value |
| `key(key)` | presses `Enter`, `Tab`, `Escape`, `Backspace`, `Delete`, an arrow key (`ArrowUp` …), `PageUp`, `PageDown`, `Home`, `End` or `Space` |
| `scroll(direction?)` | scrolls most of a screen `down`, or `up` |
| `back` | goes back a page |
| `screenshot` | shows the LLM what is on the screen of the tab, 1280×900, as an image |
| `dialog(accept, text?)` | answers the dialog the page shows: `alert`, `confirm`, `prompt` or the question whether to leave it; `accept: true` is OK, `false` Cancel, and `text` what is entered into a prompt |
| `console` | what the page wrote to its console since the last `console`, its errors with where they were thrown, and the requests of the browser that failed, like `[error] Failed to load resource: … 404 (…/logo.png)`. The browser keeps the last 200 lines of each tab, of at most 1 KiB each; a call returns the last 24 KiB of them and says how many were left out |
| `tabs` | the tabs of the conversation, like `[2] "snake.jpg (1200×800)" https://…/snake.jpg (current)` |
| `switch(tab)` | makes tab `[n]` the current one; with a window the browser shows it |
| `close` | closes the current tab; the one opened before it is the current one then |

`click`, `type`, `key` and `back` wait for a page they start to load and say where the tab is then; the numbers
of `read` hold until the next `read`. After an interaction, the agent makes a separate
`browser` call with `{"action": "read"}` to check what changed.

While a page shows a dialog it does nothing else, and nothing that is asked of it comes back. So the tool
watches for one: the action that a dialog interrupts returns at once with what the dialog says and how to
answer it, like `click: the page shows a confirm "Delete it?" and waits for an answer: …`, and so does every
action after, until `dialog` answered it.

- **Tabs per conversation.** Every conversation, and every helper of [`delegate`](#tools), has tabs of its
  own, so that they do not get in each other's way, and the actions go to its current tab.
- **The profile of the node.** The browser keeps what the agent signs in to, its cookies and what pages store
  in `browser/` of the [profile](#profiles), apart from your own browser: it does not see your sessions, and
  you sign it in where you want it to be signed in. It is one of the secrets of the node: no tool for files
  reads it, but a command of `bash`, which runs as you, does.
- **With a window.** `open` with `visible: true` starts the browser with its window, for you to watch or to
  step in, to sign in or to solve a captcha; `visible: false` without. A browser that runs the other way is
  ended for it and started again: the tabs open again at the pages they were on, without what was typed in
  them and not sent. The setting `visible` says how the browser starts when no call says so:
  `{"tool": "browser", "visible": true}`. A browser with a window runs until the node stops, one without is
  ended after 10 minutes without a call and started again with the next.
- **What pages see.** The browser does not tell them that it is automated (`navigator.webdriver` is `false`),
  and without a window it does not say so in its user agent: it takes the one it has with a window, which it
  learns by starting once more the first time, for each browser and each update of it. It speaks the
  languages of the user, as `LC_ALL`, `LC_MESSAGES` or `LANG` name them, German say, and then English, and
  keeps the time zone of the system. What it does not hide: without a window it draws with a GPU in software,
  and its screen is smaller than its window. A page that asks whether you are a person is best answered by
  you: with `visible: true`, and the profile keeps what that page then knows of the browser.
- **What it does not do.** It opens no files (`file:`), downloads nothing and sees nothing of a frame within a
  page. It reaches what this machine reaches, the local network and `localhost` too.
- **Peers.** `browser` is not in the `a2a` the setup writes. A peer it is given to gets its tabs in a browser
  context of its own for each conversation, which shares nothing with the profile or with other
  conversations and is thrown away with its last tab. Only you decide whether the browser shows its window: a
  peer's `visible` is refused.
- **What it reads is written by others.** A page can say anything, also what looks like an instruction: the
  description of the tool tells the LLM not to follow such.

The default local backend needs no port: ouro talks to the browser through a pipe
(`--remote-debugging-pipe`), which no other program can reach, and the browser ends with the process
of ouro, however that ends.

#### Hosted browser backends

The `browser` tool also accepts `provider` and `endpoint` in its settings. Select these
under `/settings` for `browser`, or use a tool entry like (replace the example hostname
with your server; the service can run on another machine reachable by the Ouro node):

```json
{"tool": "browser", "provider": "camofox", "endpoint": "http://browser.lan:9377"}
```

`camofox` uses the native [Camofox REST API](https://github.com/jo-inc/camofox-browser),
not Chromium DevTools. It supports navigation, snapshots, clicking, typing, keys, scrolling,
screenshots and tab management. The native adapter reports unsupported actions explicitly
(`dialog`, `console`, and opening a link/image by element reference); use `click` or an explicit
URL instead. Authentication/storage for Camofox is isolated per conversation and owned by
the server, rather than the local Chromium profile. Alternatively, attach a remotely hosted
Chromium-compatible browser:

```json
{"tool": "browser", "provider": "cdp", "endpoint": "http://browser.lan:9222"}
```

For CDP, `endpoint` can also be a browser-level `ws://` or `wss://` DevTools URL.
Use `provider: "local"` (or leave the settings out) to keep the existing local Chromium.
Hosted backends are contacted only when used, never merely to validate settings, and errors
do not fall back to a local browser. The browser host controls networking, storage, display
and browser lifetime; the local profile and window settings do not configure that host.
Use only trusted endpoints: page contents, URLs and typed text are sent there. Keep remote
browser APIs behind authentication and a trusted network or TLS, not exposed publicly.
For bearer authentication, set `token_env` to the name of an environment variable available
to the running Ouro node, for example `"token_env": "MY_BROWSER_TOKEN"`. Keep the token
itself out of the tool settings, which are visible in configuration views.

### Computer

`computer` uses the apps of your computer as a person does, for what no other tool reaches: it looks at
their windows, clicks, types and presses keys. It drives them through
[cua-driver](https://github.com/trycua/cua), which reads what a window shows through its accessibility
tree (AT-SPI on Linux, AX on macOS) and sends it input; on Omarchy it is `pacman -S cua-driver-bin`. ouro
starts it as an MCP server of its own (`cua-driver mcp`) with the first call, tells it to send no
telemetry, and ends it after 10 minutes without a call and when the node stops. It takes `action` and what
that action needs:

| Action | Does |
|---|---|
| `windows` | the windows of the apps not blocked, front ones first, like `window 7: firefox "Snakes" 1280×900`, and the names of the apps of the others |
| `open(app)` | launches an app not blocked by the same policy as window actions, by its name alone: no path, no URL, no arguments |
| `look(window, query?)` | what can be acted on in the window, as `[n] role "label" = "value"`, indented as it is nested, and a screenshot for the LLM; with `query` only the elements whose text has it, and what holds them. 24 KiB per call |
| `click(window, element)`, `click(window, x, y)` | clicks `[n]` of the last `look` at the window, or a point of its screenshot; `count: 2` double-clicks, `button` is `left`, `right` or `middle` |
| `type(window, text, element?, enter?)` | types `text` into the window, into `[n]` if given, and presses Enter after it with `enter` |
| `key(window, key)` | presses a key, like `Return`, `Escape` or `Down`, or keys together, like `ctrl+s` |
| `scroll(window, direction?, amount?, element?)` | scrolls `down`, `up`, `left` or `right`, 3 lines unless `amount` says otherwise |

The numbers of `look` hold until the next `look` at the same window, which the agent calls after an action
to see what it did: `computer({action: "click", window: 7, element: 4}); computer({action: "look", window: 7})`.

- **For you alone, except the apps you block.** No peer gets the tool: listing it in `a2a` is an error.
  `blocked_apps` lists the apps it may neither use nor launch, by the names `windows` shows, matched
  case-insensitively: `{"tool": "computer", "blocked_apps": ["firefox", "gimp"]}` blocks those apps.
  With `"blocked_apps": []`, or without the setting, it may use every app except terminals;
  `"blocked_apps": ["*"]` blocks every app. `open` follows the same policy as window actions.
  The former `apps` allow list is no longer a valid setting: remove it and explicitly list apps you
  want to block under `blocked_apps`; do not copy allowed apps into the deny list.
- **Never a terminal.** Whatever `blocked_apps` says, it does not use or launch a terminal, as Alacritty, kitty, foot, Ghostty,
  GNOME Console or iTerm: there it could do anything, also what the guard of the tools for files keeps it
  from. An app not blocked can still start others or reach far, a file manager say: block what you would
  not let it use while you watch.
- **In front of you.** It brings the window it acts on to the front, where the system lets it, so that you see what it does. With
  `"background": true` it acts on windows where they are, behind others or on another workspace, as far as
  cua-driver can on the system: on Wayland input goes to the window that has the focus.
- **What it reads is written by others.** A window shows anything, also what looks like an instruction: the
  description of the tool tells the LLM not to follow such, not to type passwords and to leave dialogs about
  permissions and payments to you.
- **What it needs.** cua-driver from `PATH`, or where `driver` in the settings says. On Linux it needs the
  session of your desktop: the node has to run with its `WAYLAND_DISPLAY` or `DISPLAY` and the bus of
  AT-SPI, as it does when you start it from a terminal of the desktop. On macOS cua-driver asks once for
  Accessibility and Screen Recording (`cua-driver permissions grant`), for its app CuaDriver, which then runs
  on as a daemon: not for the terminal ouro runs in, which would hand them to every command run there. On
  Linux the driver runs in the process ouro starts and ends with it. Whether a window shows a screenshot
  depends on the system: `look` says when it has none, and why.
- **Hyprland.** cua-driver counts its support for Hyprland as experimental; ouro switches it on
  (`CUA_DRIVER_RS_ENABLE_WAYLAND=1`). With cua-driver 0.35.4 `windows` and `look` work there, with a
  screenshot also of a window on another workspace, but input does not reliably reach a native Wayland
  window: a click may end with an outcome that is unknown, and bringing a window to the front needs a plugin
  of cua-driver for Hyprland that its installer does not install.

### History

The node archives every conversation in full: the messages of the user, what the model answers along with
the calls of tools it asks for, and the whole result of each call, as it came before the memory left
anything out. It is where your [sessions](#sessions) are kept. The plugin `history`
(`internal/plugins/history`) gives your agent the tools to search it: it finds there what the
[memory](#how-it-works) cut off, and the conversations before `/new`. What is sent to the LLM stays as it
is: the archive changes no context, and is only read when the agent calls one of its tools. Turned off, the
plugin takes the tools away, and the node archives all the same.

When you refer back to an earlier agreement, including UI or dialog design, the search tool instructs
the agent to search the history and read the surrounding records before confirming specific details
as agreed. For your earlier conversations it searches across sessions. A proposal is not an agreement;
if the evidence is ambiguous, unavailable or inconclusive, the agent should say what remains unverified
and ask a focused question only when needed, rather than giving blanket agreement.

| Tool | Does | Limits |
|---|---|---|
| `history_search(query, scope?, limit?)` | the records that contain a text, ignoring case: per hit the session, the time, the number of the record, its role and the text around the hit. `scope` is `current` (the default) or `all` | 20 hits, at most 50 with `limit`, and 16 KiB |
| `history_read(session?, from?, count?, offset?)` | the records of a conversation in order, from a number on, with a note where to continue; without `session` the current conversation. A long record comes in pieces: continue with `from` and the byte `offset` in the note (0 if left out) | 20 records, at most 200 with `count`, and 16 KiB including the note; no record content is discarded |
| `history_sessions()` | the archived conversations, the most recent first, with when they started and their title, or else their first message | 50 |

- **Who reaches what.** For you, `current` is the conversation you are in, and `all` every one of yours. A
  peer reaches the conversation it is in and nothing else: `all`, a session it names that is not its
  current one, and every conversation of yours or of another peer are refused. The scope comes from the
  conversation the tool runs in, never from its arguments. A peer gets the tools only when `a2a` lists
  them, like any tool.
- **What a hit is worth.** The tools say so to the model: what another conversation holds may be outdated,
  and is a hint, not a fact.
- **Where it is.** `history/ouroboros.sqlite` is the per-profile SQLite database shared by sessions,
  history and memories. Conversations retain their owner/peer boundary. Records keep their
  sequence, answer, timestamp, role, content and tool calls; the last record says whether the answer
  finished, failed or was canceled, without exposing failure diagnostics to peers. Titles, effort,
  project directory and restart continuation markers are session metadata. Full-text indexes are kept
  transactionally with the data. The database uses a pure-Go driver; no database service is needed.
  Existing JSONL archives and metadata sidecars are imported without deleting the original files.
  Those legacy files remain backup copies, not the active archive; keep them private as well.
- **The archive holds what your agent saw.** Everything a tool gave back is in it, such as the content of a
  file the agent read, on disk below `~/.config/ouroboros/history/`, until you delete it. The tools for
  files read that directory and never change it, like the rest of the [directory of the node](#files);
  whoever may read it may read your conversations.
- **Nothing is deleted by itself.** `/session` lists every archived conversation, yours and, after `tab`,
  those of peers; `ctrl+d` pressed twice deletes one, see [Sessions](#sessions). A conversation archived again
  after that starts anew.
- **When writing fails** (a full disk, a directory in the way), the answer is given all the same, and the
  log says `plugin archive after failed` with the cause.
- **The log** records every call of the three tools: the tool, for whom, the scope and the number of hits,
  never what was searched or found.

Before the archive, nothing was archived: conversations from before are not in it.

### Skills

A **skill** is what you tell your agent once about a kind of task: how to write a review, what to look at in
a ticket, how to release a version. The plugin `skills` (`internal/plugins/skills`) gives your agent the tool
`skill`, which loads one. A skill is a directory in `skills/` in the directory of the node, with a file
`SKILL.md` in the format of [Agent Skills](https://agentskills.io):

```markdown
---
name: review
description: How to review a change. Use when the owner asks for a review.
---

Read the whole diff before you comment. …
```

- **What the LLM sees.** The description of `skill` lists the name and the description of every skill the
  agent may load, one line each. What a skill says comes only when the agent loads it: `skill({name:
  "review"})`. A skill may have further files next to `SKILL.md`; `skill({name: "review", file:
  "checklist.md"})` reads one, also for an agent without the [tools for files](#files). No path and no link
  leads out of the directory of the skill; a file has at most 256 KiB of text.
- **The name** is that of the directory: lowercase letters, digits and single hyphens. `SKILL.md` starts
  with a frontmatter between two lines `---` with `name` and `description`, each on a line of its own;
  further keys are ignored. A `SKILL.md` that is not so is an error, like a tool that is not there: the
  node does not start, the chat does not answer, and the error names the skill.
- **Which skills an agent may load** is a setting of `skill` in `h2a` and `a2a`, `allow`, with their names.
  Without it your agent loads every skill and the agent that answers peers none:

  ```json
  "tools": {
    "h2a": ["*", {"tool": "skill", "allow": ["review", "release"]}],
    "a2a": [{"tool": "skill", "allow": ["faq"]}]
  }
  ```

  A name in `allow` that is no skill is an error.
- **When a skill is seen.** The skills are read when the agent is built: a new skill is offered once the
  configuration changed or the node started again. What a skill says is read at every call, so a change
  to `SKILL.md` holds at once.
- **The agent cannot write a skill.** The tools for files never change the directory of the node, so no
  skill comes from the agent, nor from a peer that talked it into something. A command of `bash`, which runs
  [in no sandbox](#commands), is not held by that.
- **The log** records every call: the skill, the file and for whom, never what the skill says.

### Settings

`/settings` in the terminal UI shows the configuration of the node in one place, in a dialog over the chat
like `/model` and `/effort`, and changes it. It is the owner's alone, like every command of the UI: no agent
and no peer reaches it.

| Category | Shows | Changes |
|---|---|---|
| The node | name, the description of the agent card, the access in use (what it is called and what tells it apart, like its endpoint or its account; never what it signs in with), model, the ID of the node, the model for peers, the fallbacks, the effort, the debug mode | the name and the description (empty is the default); the debug mode; the accesses in `/setup`, the models and fallbacks in `/model`, the effort in `/effort`, each opened from here |
| The agents | the context provider and those this build has; whether your agent builds into ouro what a task needs (`self_improve`) or proposes it | both |
| Plugins | every plugin of this build: on or off, whether it runs now, and why one cannot be turned off | turn a plugin on or off |
| Tools of your agent | `h2a`: whether all tools are allowed (`*`), and for each tool whether it is allowed, by its name or by `*`, with each of its settings below it and what holds where one is not set | all tools, each tool, each of its settings |
| Tools for peers | `a2a`, the same; a tool for your agent alone, and one for a peer with a list of its own alone, is shown as such and cannot be allowed | the same |
| Tools of single peers | `tools.peers`: every peer you trust, and every list there, with whether it has a list of its own | give a peer a list of its own (it starts as the one for peers) or take it away, add one by the ID of its node; its list opens like the tools for peers, where a trusted tool like `bash` is allowed by its name |
| Protected paths | `protected`, the paths no tool for files touches, or that the default holds | remove a path, add one, go back to the default |

It opens with the list of the categories: `↑` `↓` select one, `enter` opens it, `esc` goes back to the chat.
In a category, `↑` `↓` select a line, `←` `→` what to do with it, `enter` does it, and `esc` goes back one
level. `settings` on a tool opens its settings: each on a line, with what it does and what holds while it
is not set. A switch is turned on or off, a list gets an entry or loses one, `reset` goes back to what holds
unset, and `As JSON` changes all of them at once as the configuration keeps them (a JSON object like
`{"allow": ["~/share"]}`). A text, like the name, a path or an entry of a list, is typed in a line below the
list: `enter` saves it, `esc` keeps what was.

- **Every change is checked before it is written**, the way the node checks its configuration when it starts:
  an unknown tool, a setting a tool does not take, a protected path that is none, a context provider this
  build does not have, a plugin turned off that another one needs, a description of more than one line or
  300 characters. What fails changes nothing, and the footer says why.
- **The footer says from when a change holds.** The name, the tools, the paths and the context provider hold
  from the next message, for you and for peers. The description holds for the next one who reads the card. A plugin turned on or off holds once ouro starts anew: quit and start `ouro` again. Node lifecycle commands remain available only in the CLI as `ouro start` and `ouro stop`. Until then its line says that it runs, or that it does not.
- **All tools.** Turning a tool off while all tools are allowed lists every other tool by its name instead
  of `*`, so that nothing else changes; from then on a tool of a plugin turned on later is not allowed by
  itself. Turning all tools on again drops the names, but keeps the settings of the tools that have some.
  For peers, `*` and every plugin turned on give them its tools: the line says so.
- **Secrets.** The key of the node and what the accesses sign in with are not shown, and not changed here:
  the setup changes the accesses. The log records that a setting was changed, and which, never a value.
- `/setup`, `/model` and `/effort` work as before; `/settings` opens them for the access, the models and the
  effort.
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
| `get_config()` | returns the configuration as JSON: ID, name, description, model, `provider` (what the access in use is called) and `tools`. The key of the node and what the accesses sign in with are not in it, and neither is the ID of the access. What is not set shows with the default that holds (`description`, `h2a`, `a2a`, `protected`), and so does the switch of `bash` |
| `propose_config(path, value, reason)` | proposes one field to become `value`: `name`, `description`, `model`, `tools.h2a`, `tools.a2a` or `tools.protected`, with a sentence on why. The tool asks directly through the user-question interface, shows old and new values, and waits; only “Apply change” applies it |
| `withdraw_proposal(id?)` | lists or removes legacy stored proposals only; new changes ask directly and are not queued |
| `user_question(question, options?, recommended?)` | asks you `question` and waits for your answer, which it returns: with 2 to 6 `options` to choose from, one of them `recommended` if the agent has a preference, or without any, for you to write it |
| `delegate(tasks)` | starts 1 to 4 tasks in the background, each done by a helper, and returns at once with their numbers; the report of each comes as a message once it ended. A task is `{task, tools?}`: `tools` narrows what the helper may use |

**The agent can ask you on the way.** When it cannot go on without a decision of yours, it asks with
`user_question` instead of guessing or ending its answer with the question, as its prompt tells it: the
question shows in the conversation, and where the input is you
choose an answer with the arrow keys and enter, the one the agent recommends selected. A question
without answers to choose from shows the input alone. The last row,
"Chat about it", is the input: select it, or just start typing, and what you send is the answer in your own
words. The agent waits for your reply; esc stops the answer as ever. A
question in a conversation that is not shown is told in the footer. Where nobody is shown the question,
as with `ouro chat "msg"`, the agent is told so and goes on with what it thinks best. Take `user_question`
out of `tools.h2a` if you do not want to be asked.

**The agent can hand work to helpers.** With `delegate` it starts tasks in the background, each done by a
helper of its own: a split piece of work, or a long search it keeps out of the conversation. The call returns at
once, and you can go on writing meanwhile. At most 4 tasks run at once in a conversation, and the line below the
chat says how many run (`2 tasks run · esc stops`); `ctrl+j` lists them next to the jobs. Once a task ended, its
report is a line in the chat (`◆ task 2 · find the callers ✓ 2m40s`, or `✗` and why it failed) and the agent
takes it as it takes what you write: an answer on its way goes on with it after its current round, and
otherwise an answer starts for it. What a helper reports is told to the agent as its report, not as what you
said. A helper is an agent of yours, not a peer: it answers with the model, the access and the effort of the
conversation, with your tools as `tools.h2a` sets them up, under the same rules for paths and commands, and
starts in the same workspace. It gets neither the tools to ask you, to reach peers, to build ouro or to change
the configuration, nor `delegate`, so it hands nothing on. It sees only its task, nothing of the conversation,
and forgets it once it answered; what it said along the way is neither shown nor archived, only its report.
A task keeps the node running as an answer does, and a new build waits for it; `esc` and `/cancel` stop the
tasks of the conversation along with its answer, and a stopped task is not reported. A conversation in which a
task runs is not deleted. Where nothing runs past the answer, `delegate` waits for its tasks and returns what
each ended with. Take `delegate` out of `tools.h2a` if your agent is not to use helpers.

**Trust comes only from your decision.** Configuration changes proposed by your agent ask directly in
its conversation through the same question interface as `user_question`. The question shows the field,
its current and proposed values, and the reason. Only **Apply change** authorizes the write; rejection,
a different answer, cancellation, or a missing question interface changes nothing. There is no separate
pending/approval screen and no new persisted configuration proposal.

- Values are validated before the question and again before writing. Unknown tools, duplicate tools,
  wrong types and invalid protected paths are rejected.
- The node key, provider endpoints and credentials cannot be proposed; those remain in setup.
- If the target field changed while the question was open, approval does not overwrite it. Changes to
  unrelated fields are preserved under the configuration lock.
- These configuration tools are owner-only, never available to peers.
- `withdraw_proposal` remains for listing or removing legacy proposals; it does not approve them.

The file tools cannot do any of this: `config.json` is locked for them, see [Files](#files).

### Providers and accesses

A **provider** is a kind of LLM a node can talk to. This ouro has five, which the setup offers by these
names:

| Provider | Signs in with | Answers through |
|---|---|---|
| **Custom Provider** (`openai`) | an endpoint and an API key, which may be none (a local Ollama): any OpenAI-compatible API | `POST /chat/completions` at the endpoint |
| **DeepSeek** (`deepseek`) | an API key from [DeepSeek Platform](https://platform.deepseek.com/api_keys), with API credit | `POST /chat/completions` at `https://api.deepseek.com` |
| **ChatGPT** (`chatgpt`) | your ChatGPT account, in the browser ("Sign in with ChatGPT") | `POST /v1/responses` at `api.openai.com`, within the limits of your plan |
| **OpenCode Go** (`opencode-go`) | the API key of your OpenCode Go subscription, from the OpenCode console, and nothing else | `POST /chat/completions` at `https://opencode.ai/zen/go/v1` |
| **Claude Code** (`claude-code`, experimental) | the login of the official Claude Code CLI (`claude auth login`), with your Claude Pro, Max or Team plan | the CLI, which sends one `POST /v1/messages` to `api.anthropic.com` per call |

**DeepSeek** asks only for an API key, not an endpoint. The key is kept with the other credentials.
`DEEPSEEK_API_KEY` optionally offers it during setup; it does not change an existing access.
Models are fetched from `GET /models`, with `deepseek-flash` preferred when available. API usage
is billed separately from the DeepSeek chat app and needs API credit. Existing custom-provider
accesses pointing at DeepSeek are left unchanged.

**OpenCode Go** needs no endpoint from you: where it is reached is part of ouro. The key is kept with the
other credentials and sent as `Authorization: Bearer …`, with the ID of the conversation in
`x-opencode-session` and ouro's own `User-Agent`, as OpenCode asks of clients. Its models are listed with
`GET /models`. OpenCode Go answers some of them in the form of other APIs, on `/responses` (the models of
OpenAI, xAI and Muse) or `/messages` (those of Anthropic, MiniMax and Qwen): ouro speaks chat completions
to it and offers the models that answer there (`glm-…`, `kimi-…`, `deepseek-…`, `mimo-…` and the like),
not the others. The list does not say the route, so ouro tells it by the family of the model, as
[the documentation of OpenCode Go](https://opencode.ai/docs/go/) lists them. A key that is refused is asked
for again, and an OpenCode Go that does not answer is told as that.

An access you set up before as a custom provider stays what it is, also when its endpoint is the one of
OpenCode Go: its ID, its key and the model in use are not touched, and nothing turns it into another kind.

**Claude Code** lets the agents answer within your Claude plan instead of with an API key. It needs the
official CLI, installed (`npm install -g @anthropic-ai/claude-code`) and signed in with `claude auth login`
as the user ouro runs as. ouro looks for `claude` on `PATH` and where it is usually installed
(`~/.local/bin`, `~/.claude/local`, `/opt/homebrew/bin`, …), and the setup waits until the CLI says it is
signed in. ouro keeps no token: the access holds where the CLI is, the e-mail address and the plan, and the
directory of the login if `CLAUDE_CONFIG_DIR` named one when you signed in. What a call costs counts
against the usage your plan has for the Agent SDK and `claude -p`, as for every app built on them. That is
less per window than the interactive Claude Code gets. `/usage` shows the limits of the plan as the CLI
fetches them.

Every call starts the CLI once, with none of its own tools, settings, slash commands or MCP servers. ouro
hands it the conversation as stream-json on stdin. The CLI takes the earlier messages without answering
them and answers the last one. Its requests go to a relay of ouro on the loopback interface. The relay
sends the first request to the API with the tools of the agent in place of those of the CLI, and refuses
every request after it. It reads the answer as it passes: the text, the calls of tools, what the model
thought (signed, to go back with the next call) and the usage. The agent runs the tools as with every other
provider. The relay also puts every request of a conversation in the same form: the messages of the user
as ouro sent them, without the reminders the CLI adds, and the date at the end. So the API finds what it
cached of the conversation from the second call on. Variables that would send the CLI to another API or
with another key (`ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_USE_BEDROCK`, …) are not passed
on. The CLI does not update itself while ouro runs it.

The models are those the CLI offers for your plan. A plain model ID runs within the 200K window the CLI
gives a gateway. This provider is experimental: it depends on how the CLI speaks stream-json, which is not a
public interface of Anthropic and may change with an update of the CLI.

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
- **Nothing is switched for you, unless you list fallbacks.** An access that fails, or a plan whose limit is
  reached, stops the answer with what went wrong. The node never moves to another access on its own: only
  to those you named for it, see [Fallbacks](#fallbacks).
- **Manage the accesses in the setup**: `/setup` in the terminal UI and `ouro start --setup` on the terminal
  both start with the accesses of the node. There you add one, sign in to one again (a new key, a login that
  ran out), rename one and remove one. Renaming changes no ID, so nothing that refers to the access breaks.
  The last access cannot be removed. Removing the access in use hands over to the first of the others, with
  the model that one offers by default, and says so; if that one does not list its models, nothing is
  removed. A ChatGPT access is signed out at OpenAI before it is removed.
- **Peers answer with the same access and model as you do, until you choose others for them.** `p` on a
  model in `/model` makes it the one the agent answers peers with, of whatever access it is, and marks it
  `for peers`: you chat through ChatGPT, say, and peers are answered by a local Ollama. It holds from the
  next message of a peer on, and what the model before thought is not sent to another access. `p` on that
  model again takes the choice back, and peers are answered with what you are. The choice is `peer_access`
  and `peer_model` in the configuration, of every profile its own; no proposal of the agent changes it.
  Removing the access peers are answered through takes the choice back too.

#### Fallbacks

**A call that breaks off on its way is asked again** first, by every access: when the connection is closed or
reset before the answer is in (as a gateway does that closes a connection it kept open), or a gateway or the
service is overloaded for a moment (HTTP 502, 503, 504, 529), a second after and four seconds after that. The
log says so with the call; only when the third does not get through either does a fallback take over, or the
answer stop with why.

A fallback is a model of an access that answers you when the one before it **cannot answer for now**. `f` on
a model in `/model` makes it the last of the fallbacks, marked `fallback 1`, `fallback 2` … in their order,
and `f` on a fallback takes it out again. Without fallbacks nothing changes: the answer stops with why.

- **What hands on to a fallback**: a limit or a quota that is reached (HTTP 429 or 402, the limit of a ChatGPT
  or Claude plan), a service that fails (HTTP 5xx, a plan that cannot be used right now), and an access that is not
  reached at all, like a local server that is down. **What does not**: an error of what was asked, like a
  context that is too long or a model the access does not have, a key or a login that is refused, and an
  answer you stopped. Those are told as they are.
- **In their order.** The first fallback that can answers; one that cannot for now hands on to the next. If
  none can, the answer fails with why the access could not and why the last fallback could not.
- **It says so.** The title of the conversation shows `answered by the fallback MODEL of ACCESS`, and the
  [log](#the-log) has it with every call, with what the access before it said. What the call cost is shown
  as for any call: a fallback may charge where a plan did not.
- **For five minutes** a fallback that answered goes on answering, so that not every call waits for an
  access that is down; then the access before it is asked again, and answers again as soon as it can.
- **A conversation goes on**, as when you pick another model: a fallback is not sent what the model before
  it thought, and what it thought is not kept.
- **For you alone.** Peers are answered through your access, or the one you chose for them, and not through
  a fallback: a peer does not make the node spend where you did not say so.

The fallbacks are `fallbacks` in the configuration, a list of `{"access": …, "model": …}`, of every profile
its own. A fallback whose access is removed is gone with it.

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

1. Pick "ChatGPT" in the setup. ouro opens the page of OpenAI in your browser (and puts its link on the
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
proposes a connection. Three rules keep that from running in circles: the agent never sends the peer it is
answering a request, a message is passed on by at most 3 agents, and all the messages of a task share one
budget, see [Dialogs between peers](#dialogs-between-peers).

`ask_peer` takes the name or ID of a node that is online, as `list_peers` shows it, and no address: an
agent cannot be sent to a `host:port` somebody put into its head. Only you address a node that way, with
`ouro ask`.

`list_peers` tells for each node how far the connection has come: `connected`, `connection requested`,
`not connected`, or `trusted, but not reachable right now`. The last is a peer you connected to whose node
did not prove to hold its key within 2 seconds, or whose node is not found at all: it is gone, slow, or
someone else answers at its address. It stays your peer and needs no confirmation; the agent tries it again
later. A trusted peer whose node is not found is listed after the others, by what your contacts keep of it, and
`ouro ask` and `ask_peer` say that it is not online rather than that no node of that name is known.

### Dialogs between peers

Your agent (Terra) asks the agent of a peer (Luna) to do something, and Luna lacks something only Terra
knows. Luna does not have to guess or give up: it **asks back**, Terra answers, and Luna goes on where it was
and delivers its result, with which Terra goes on.

```
Terra                                   Luna
ask_peer(luna, "clean up")   ──────►    starts for the request
       ◄──────  question q1: "which file?"        ask_back("which file?"), waits
answer_peer(luna, q1, "a.txt") ────►    goes on where it was
       ◄──────  result: "removed a.txt"
tell_peer(luna, "thanks")    ──────►    kept, nothing starts
```

What a message is, is said by the message and enforced by the nodes. It is not read from its words, and
no prompt decides it:

| Message | Tool | What it starts at the receiver |
|---|---|---|
| **request** | `ask_peer` | its agent, which answers with its result or with a question |
| **question** | `ask_back`, by the agent that answers a request | nothing: it is what the request is answered with for now, and the agent that asked waits |
| **answer** | `answer_peer` | nothing new: the agent that asked the question goes on. It answers exactly the question whose ID it names |
| **note** | `tell_peer` | nothing: it is kept, and the agent reads it when it is asked the next time. Thanks are a note |
| **result** | the answer of the agent | nothing: it ends the request it belongs to |

An agent answers only a question it was really given: `answer_peer` with an ID no peer ever asked it sends
nothing and says so, and a note to a peer it never talked to is sent, but the agent is told that it has no
message of that peer to go on. Both are kept per answer, as that is how long a question stays open.

- **No agent waits for one that waits for it.** A question is no request of its own, which the two agents
  would wait on forever: it travels as the answer to the request, and the answer to it as a message of its
  own. So only one side waits at a time. While an agent waits for the answer to its question, a new request
  in the same conversation is told that the question comes first.
- **Nothing is handled twice.** Every message has an ID. One that is delivered once more is answered with
  what it was answered with, and starts nothing; a question that was answered takes no second answer. A new
  message is a new one, whatever it says.
- **One budget for the whole task.** All messages your agent sends for one answer to you, and everything
  the peers send for them in turn, questions and answers and what they ask of other peers, are one task: at
  most **16 messages** and **30 minutes**. The budget travels with every message and its answer, and each
  node takes the lower of what it knows and what it is told: a peer can spend it, but not fill it, and a
  request passed on to a third node starts no new one. When it is used up, nothing more is sent, and the
  agent is told to finish with what it has.
- **Stopping stops all of it.** When the answer of your agent ends or is stopped (`esc`, `/cancel`) while a
  request is on its way, the peer stops working on it. A question that was not answered by then is given
  up, and the agent that asked it stops. A question nobody answers ends by itself after 10 minutes. Your
  other conversations and the other peers are not touched.
- **No new rights.** Questions, answers and notes go between connected peers only, and none asks for a
  connection. `ask_back` reaches the peer that sent the request and nobody else, and what an agent does for
  a peer is what `tools.a2a` allows it: list `ask_back` there to let your agent ask peers back. It gives a
  peer nothing of yours but the question.
- **What happened stays readable.** The requests, questions and answers are calls of tools in your
  conversation and in its archive; at the peer, the conversation in the `Peers` tab of `/session` shows
  what it was asked and told, notes included.

The dialog works between nodes of any host, over the same signed and pinned requests as every message. It is
an extension of A2A, carried in the `metadata` of the messages (`ouroboros.kind`, `ouroboros.question`,
`ouroboros.task`): a node that does not know it gets plain requests and gives plain answers. A front end
that cannot answer a question, like `ouro ask`, gets it as what the agent said, and gives it up.

### Peers you trust to build

Two agents can build something together, like a way to send files that ouro does not have yet: one asks,
the other answers in the same conversation, and each changes and builds its own ouro until both do the same.
For that the agent that answers a peer needs what your agent has: `bash` and `rebuild`. No peer gets them
through `a2a` or `"*"`. You give them to one peer, by the ID of its node as `list_peers` shows it, in a list of
its own:

```json
"tools": {
  "a2a": ["list_peers"],
  "peers": {
    "e982c39668a3": [
      "list_peers", "ask_back", "rebuild",
      {"tool": "read",  "allow": ["~/src/ouro", "~/peer-work"]},
      {"tool": "write", "allow": ["~/src/ouro", "~/peer-work"]},
      {"tool": "edit",  "allow": ["~/src/ouro", "~/peer-work"]},
      {"tool": "bash",  "allow": ["~/src/ouro", "~/peer-work", "~/Library/Caches/go-build", "~/go"]}
    ]
  }
}
```

- **The list of the peer applies to it alone**, in place of `a2a`; it is checked like `a2a`, and a key that is
  no ID of a node is an error. Every other peer is answered as before.
- **`bash` and `service` run nothing for a peer**, also when its list names them: they run in no sandbox,
  which would hold the commands of a peer.
- **`rebuild` of a peer** builds the checkout as yours does: the tests it names and the
  build run as you. The node starts anew with it once no answer is on its way, to you or to a peer; the
  conversation with the peer goes on there, as the archive kept it. The agent knows from its prompt that you
  trust that peer to work on ouro with it, and to change only what the two agree on.
- **What happened stays readable**: the conversation of the peer, with every call of a tool, is in the `Peers`
  tab of `/session`. `ouro rollback` goes back to the build before, see [Updates](#updates).

**Known risk.** A peer with `rebuild` runs code on your machine: what its agent and yours write, as tests and
as the build, runs as you, with network. All of ouro is in reach, the rules of its tools too. Give it to a peer whose
owner you trust as far as you trust yourself, and take it back by removing its list.

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
- The agent that answers peers looks an ID up with the tool `explain_error`. It
  has it whatever else `a2a` lists, as long as it lists one tool: a peer without a tool
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
  calls of tools that ended and how much was sent to the LLM in all,
  the messages and the tools of every call as JSON. That grows with every call, as each sends the whole
  conversation again. Where the API says what a call cost, its line and the last line also have the tokens
  sent and how many of them the provider had cached: a call that goes on from the one before should find
  most of its tokens cached. What the LLM says along with
  calls of tools follows them as `said "…"`; a call that ends in `text` has no text in its line, because that
  text is the answer and is in the last line.
- **Signals**: before the end of an answer that ouro could have done better, a line `signals: …` says what the
  numbers show, like `bash took 42s; read ran 3 times with {"path":"a"}; 18 calls of the LLM`. A signal is a
  tool call of 30 s or more, an answer that worked 3 minutes
  or more, 3 tool calls or more that failed, the same tool with the same arguments 3 times or more, 15 calls of
  the LLM or more, a tool result that was cut, as the agent has to get what was left out with
  another step, a call that broke off and was asked again or that a [fallback](#fallbacks) answered, less
  than half of the tokens cached from the third call on, an answer that gave up after too many calls, one
  without words, and a tool that failed and then worked in the same answer (`a call of bash failed, a later one
  worked`), as the way that worked may be worth [remembering](#memories). What
  waits for someone else, `user_question`, `ask_peer` and `ask_back`, is never slow, and its time is not counted
  to the answer. An answer without a signal has no such line.
- **Follow-up**: a short message of the owner, of at most 60 characters, within 15 minutes of an answer, like
  `did anything happen?` or `?`, is a signal of that answer, as an answer that only said what it would do looks
  well in the log. The line `signals of the answer before: the owner wrote next, 2m0s later: "?"` follows the
  message, and the answer before is reviewed with it. Only the message right after an answer follows up on it.
- **Review**: an answer of the owner with signals is looked back on once it is given, or once a follow-up comes. The LLM of the agent, with
  its fallbacks, is sent the tools of the agent, the lines of the log of the answer (of a long one the first and as many of the last as fit in 12 KB) and the signals, and asked for the one
  change of ouro that would have made it better: a tool that is missing, slow or misleading, a default, or the
  prompt. What comes from the task or the provider is no change. The line `review: "…"` has what it proposes,
  `review: nothing to change` that it found nothing, `review failed: …` why it could not. What it proposes
  is shown in the chat and waits in `/suggestions`, see [Terminal UI](#terminal-ui); it changes nothing by itself.
  Whether the owner took or dropped it is logged too: `the owner took a suggestion: "…"`. The review may also
  remember what the owner said, see [Memories](#memories): `review: remembered "…"` for each, and
  `the owner took back a memory: "…"` once the owner took one back. A correction of style or of the way to do a
  task that belongs to a [skill](#skills) is suggested as a change of that skill, `In the skill review: …`, which
  waits in `/suggestions` like any other; taking it asks the agent to change the skill with `skill_write`.
- **Topic finished**: a conversation other than the thread that the owner left for 2 hours, with 3 answers or
  more or with a signal, is reviewed once more, with the messages of the owner in it, for what to remember of it;
  the signal is `topic finished: the owner left the topic after 5 answers`. A node that stops right
  after the answer, as `ouro chat` stops one it started, stops the review with it.
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
2. Alice receives a question in her thread with Bob's identity and public key. Only **Approve connection** agrees, and only
   then Bob gets a **connection request** (`contact/request`). It carries no message; Bob records it and
   answers `pending`, no LLM call happens. With reject the proposal is dropped and Bob never hears of it.
3. Bob receives the incoming request as a question in his thread, using the same question interface.
   Only **Approve connection** grants trust; **Reject connection** refuses it. Cancellation or arbitrary text never approves.
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
  assistant          builds the agents of the node
  base               the console and the dispatcher of the commands of all plugins
  chatgpt, openai,   the providers of LLMs: ChatGPT, OpenAI-compatible APIs, DeepSeek,
    deepseek,        OpenCode Go and Claude Code
    opencode,
    claudecode
  discovery,         finds the nodes online, on the network and on the tailnet
    tailscale
  files              the tools for files: read, write, edit, list, delete, search
  history            the tools history_search, history_read, history_sessions
  node               the commands start, stop, serve and the setup dialog
  peers              the network of peers and the tools that reach them (list_peers, ask_peer, …)
  session            the sessions of the owner
  shell              the tool bash
  skills             the tool skill: the skills of the owner
  talk               the commands list, ask and chat
  tui                the terminal UI as a plugin; the UI itself is internal/tui
  update             the command update and the tool rebuild
internal/kernel      plugin kernel: services, contributions, start order by declared dependencies
internal/tui         terminal UI (Bubble Tea), with the splash and the setup
  tui.go             the Model: what a key does, the input and its placeholder (New)
  view.go            what is drawn: the header (headerView), the chat and its line of context and cache (chatTitle), the footer (footerView)
  question.go        the input (inputView), and a question of the agent in it
  commands.go        the slash commands of the UI and /help
  session.go         the conversations and the screen of /session
  thread.go          the thread: /thread
  context.go, contents.go  the screen of /context
  settings.go, model.go, effort.go  the screens of /settings, /model, /effort
  onboard.go         the setup
  empty.go, empty_animation.go  the empty chat: the name and its tagline
  splash.go, logo.go, mesh.go  the splash and its pictures
  markdown.go        how an answer is formatted
internal/setup       the setup of the node: its accesses, model, name; the terminal and the UI both use it
internal/llm         what a provider of an LLM is: how it signs in, lists its models and answers, and the one access it reaches
internal/credentials the accesses of the node, in credentials.json: what it signs in to its LLMs with
internal/chatgpt     the provider that signs in with ChatGPT: the login, its tokens, the Responses API (+ chatgpttest fake)
internal/claudecode  the provider that answers through the Claude Code CLI: the CLI, the relay of its one request, the stream of the answer
internal/node        the local node: Server runs it, Local controls the background one
internal/session     the sessions of the owner: the Service the node runs them in, and the client of its socket
internal/assistant   blueprint of the agents (owner and peers), their permitted tools and structured tool calls; owner activity goes to the log
internal/guard       which files a tool may touch: the secrets and the node that are fixed, protected, allow and deny
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
internal/update      self-update: from the newest main, or from the checkout as it is
internal/sandbox     runs a command writing only where it may, without network unless allowed: sandbox-exec, bubblewrap; bash uses it no more
scripts, hooks       the local quality checks and the Git hooks, see [Checks](#checks)
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
| a tool with settings in `config.json` | wrap the `plugin.Tool` in `plugin.Configurable`, with a struct that has a JSON field per setting; its tags `about` and `unset` say in `/settings` what a setting does and what holds while it is not set | `internal/plugins/files` |
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
running after cancellation.

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

The quality checks run on your machine: there is no CI. One command runs all of them. A Git hook runs
them before every commit, all but the mutation tests. Those run when main is pushed.

```sh
make check   # everything below, on the working tree
make hooks   # once per clone: the checks before every commit, the mutation tests before a push to main
```

| Command | Checks | Fails when |
|---|---|---|
| `make fmt-check` | formatting with `gofmt`; lists files, changes none | a Go file is not formatted |
| `make lint` | `staticcheck` with every check on ([staticcheck.conf](../staticcheck.conf)) | it reports anything |
| `make test` | `go vet`, then the tests with the race detector | vet reports anything, a test fails, a race is seen |
| `make cover-check` | statement coverage of the production code | one statement is not covered |
| `make mutation` | mutation tests with `gremlins` ([.gremlins.yaml](../.gremlins.yaml)) | never because of survivors; they are reported |
| `make check` | all of the above, in this order | any of them fails |

`make cover` only measures and prints the percentages; `make tools` builds the two tools ahead of time.

The agent tool `check_sanity({})` runs the whole Go test suite without cached results,
with race detection and statement coverage, followed by native GUI and JavaScript
frontend tests. It reports named failures, skips and uncovered source ranges ranked
by missing statements. Full logs and the coverage profile remain in the private
temporary directory named in its result. The default overall timeout is eight
minutes (`timeout_minutes`: 1–60). `gui: false` explicitly skips both GUI checks.
Missing GUI libraries or Node are reported as problems, not silently skipped.
This uses the checkout the running binary was built from, not the conversation
directory, and installs nothing. Coverage is raw (including test-helper packages),
excludes subprocess execution and the separate tagged GUI run, and can be incomplete
after test failures. It identifies candidates for new tests, not assertion quality
or branch coverage. Other platforms/build tags, lint and mutation tests are outside
this tool’s scope; `make check` remains the comprehensive quality gate.

### Prerequisites

- **Go** as named in `go.mod`. It is found in `PATH`, in `~/.local/go/bin` (where `./install.sh` puts it; set
  `OURO_GO_DIR` for another place) and in `/usr/local/go/bin`. `gofmt`, `go vet` and the coverage tools come
  with it.
- **make**, **git** and a POSIX shell with `awk`, `sed`, `grep`. `timeout` (coreutils) is used to limit the
  mutation tests in the pre-push hook; without it they run to the end.
- **Linux or macOS**, with pseudo-terminals (`/dev/ptmx`): the end-to-end test runs `ouro` at one. The macOS
  half of that test is written but was never run; the checks were set up and measured on Linux.
- **Network on the first run**: `staticcheck` and `gremlins` are built from source with `go install pkg@version`
  into `.tools/` of the checkout. Their versions are pinned in [scripts/tool-versions](../scripts/tool-versions),
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
- **What is left out**: only the packages listed in [coverage-exclusions.txt](../coverage-exclusions.txt), each with
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
  So is code of one system alone, like `sandbox_linux.go`, on the others, where it is not built: it is listed
  for the runs there, and a run on its own system tries these mutants and does not call their lines stale.
- **Packages that send signals to process IDs are not mutated**, today `internal/daemon`,
  `internal/plugins/shell` and `cmd/ouro` (its test kills the node). A mutant there lets a PID of 0 or -1 through, and the code under test then signals its
  own process group (that happened when the checks were set up: the run terminated itself) or every process
  you own. `scripts/mutation.sh` finds these packages by the calls (`syscall.Kill`, `unix.Kill`,
  `os.FindProcess`), in code and tests, and names them in the report. Such code needs its assertions reviewed
  by hand.
- The mutants of other packages reach that code unchanged, through `daemon.Stop`. Whatever they hand it, one
  function sends the signal, and it refuses a PID of 1 or less (`terminate` in `internal/daemon`, with a test).

**A survivor that no entry accepts fails the run**: `make mutation` exits non-zero, and with it the pre-push
hook. There is no threshold and no score to reach; the list of equivalents is the gate. Each survivor is one of
two things:

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

### Git hooks

```sh
make hooks           # install the pre-commit and pre-push hooks into this clone
make hooks CHAIN=1   # the same, keeping hooks that are already there
make unhooks         # remove them again
```

The hooks are `hooks/pre-commit` and `hooks/pre-push`, versioned with the code. `make hooks` writes a few
lines to `.git/hooks/pre-commit` and `.git/hooks/pre-push` that run them, so they change along with the branch
you are on.

- **They are local.** Every clone needs its own `make hooks` (the worktrees of a clone share them).
  `--no-verify` skips them. Nothing on the server enforces them: they are not merge protection, and a
  commit that never saw the checks can be pushed.
- **An existing setup is kept.** If there already is a pre-commit or pre-push hook, or `core.hooksPath` points
  somewhere else, `make hooks` installs nothing and says how to run both. With `CHAIN=1` the existing hook is
  moved to `pre-commit.local` or `pre-push.local` and runs first; `make unhooks` moves it back. Other hooks are
  not touched.
- **pre-commit checks what you commit.** The staged files are copied to a temporary directory and the checks run
  there: unstaged changes and untracked files play no part, and neither your working tree nor the index is
  changed. Nothing is formatted, fixed or staged for you.
- **pre-commit runs** `fmt-check`, `lint`, `test` and `cover-check`, and rejects the commit when one fails,
  with the findings and the command to run that check alone. The mutation tests it leaves out.
- A commit that stages no Go code and nothing of the checks themselves (only `README.md`, say) is not checked.
- **pre-push runs the mutation tests when main is pushed**, on a copy of the pushed commit, **for the packages
  whose production Go files the push changes on main**, leaving out the packages of `coverage-exclusions.txt`
  and the packages of which the push changes only tests. It runs to the end, however many packages a push
  changes, and rejects the push when a survivor is left that `mutation-equivalents.txt` does not accept, or when
  the mutation tests cannot run. A push to any other branch runs nothing.

| Variable | Effect |
|---|---|
| `OURO_HOOK_MUTATION=off` | leave the mutation tests out of this push to main |
| `OURO_HOOK_MUTATION_TIME=600` | stop them after that many seconds, about a push of many packages; the packages left over are named |
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
| the pre-commit hook | 1.5 min |
| the pre-push hook, to main | 5 s for a small package, never more than 5 min |

The hook takes longer than `make test` and `make cover-check` together because it runs them on a copy of the
staged files, where no earlier test result can be reused.

Mutation tests are slow by nature: every mutant is compiled and tested on its own, about 1000 of them. That
is why they run only before a push to main, why they then only mutate the packages the push changes and
stop after 5 minutes, and why the whole module is left to `make mutation` and `make check`. A
mutant that makes its tests hang costs up to 90 s.

### Troubleshooting

| Problem | What to do |
|---|---|
| `go not found` | Go is neither in `PATH` nor in `~/.local/go/bin` nor in `/usr/local/go/bin`: run `./install.sh` once (it puts Go into `~/.local/go`), or set `OURO_GO_DIR` to where it is |
| `make check` works, plain `go test` says `command not found` | `make` and the hook find a Go outside `PATH` themselves, your shell does not: `export PATH="$HOME/.local/go/bin:$PATH"` |
| `could not build staticcheck` / `gremlins` | the first run downloads them: check the network, then `make tools` |
| a tool behaves oddly after an update of Go | `rm -rf .tools && make tools` |
| a hook does not run | `make hooks` in this clone; `git config core.hooksPath` has to be empty |
| `make hooks` refuses | a hook setup is already there; the message names it and the two ways to keep both |
| the hook passes or fails, `make check` does the opposite | the hook checks the staged files, `make check` the working tree: `git status` shows the difference |
| a path in the hook's output starts with `/tmp/ouro-pre-commit.` | that is the copy of the staged files; the same path is in your checkout |
| the end-to-end test fails with ports or a node already running | it starts a real node on free ports; run it alone with `go test ./cmd/ouro` |
| the end-to-end test fails at `/dev/ptmx` or `/dev/pts` | it runs `ouro` at a pseudo-terminal; a container without `devpts` has none |
| a test fails with `node did not start` | should not happen any more: a node built with the race detector needs 0.7 s to come up and several seconds on a busy machine, so starting waits up to a minute, and ends at once when the process dies. If it does, the log named in the message says why the node ended |
| a test fails only while the machine is busy | a wait in that test is too short; say which one. The checks were run with every core busy without a failure, but only a few times |
| mutants are all `TIMED OUT` | the machine is busy: run them again when it is not. A mutant gets 90 s, or three times what the unchanged tests take (`mutant_time` in `scripts/mutation.sh`) |
| the mutation tests take too long before a push to main | `OURO_HOOK_MUTATION=off git push`, then `make mutation PKGS=…` later |
| it has to go in now | `git commit --no-verify`, and run `make check` afterwards |

## License

Ouroboros is licensed under the [Apache License 2.0](../LICENSE) (`Apache-2.0`); the copyright notice is in
[NOTICE](../NOTICE). The binary links third-party modules under their respective licenses, listed with
their license texts in [THIRD_PARTY_NOTICES](../THIRD_PARTY_NOTICES). Pass all three files on with the binary
and satisfy the listed dependencies' redistribution requirements, including any source-code availability requirements.

### Conversational background results

The thread acknowledges work briefly and remains available while topic sessions run.
Completions return to the thread agent with their full result and topic, rather than being
shown verbatim. It connects them to the current conversation, naturally returning to the
earlier topic when necessary. Internal result messages are not owner messages. Background
work alone no longer keeps the terminal input in its thinking state.

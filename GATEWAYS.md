# Messaging gateways

Gateways are profile-local plugin contributions, not default frontends. Terminal
and desktop settings discover plugin.Gateway contributions dynamically. Each
supplies public settings, a schema-defined form and an owner-only save callback.
Secret fields are write-only. Future transports can define their own text,
secret, list and boolean fields without extending either frontend.

plugin.GatewayAdapter provides an explicitly authorized, identity-bound subset
of the existing session service. It does not expose global session listing,
arbitrary session attachment, owner settings or trust administration. Transport
code remains responsible for authentication, room privacy, replay prevention,
rate limits and safe event presentation. Bindings are persisted in the protected
profile store, never accepted from incoming messages.

The one exception is the thread, the one conversation of the owner, see "The
thread" in the [manual](docs/manual.md#channels): `GatewayAdapter.Thread` returns its ID to
every identity the host grants `OwnerSession`, and the other operations take it
without a binding. Every such identity talks in the same thread, as the owner
does in the terminal UI, and its snapshots hold all of it: also what was said in
it elsewhere, and what the sessions it hands messages to answered. That is no
more than the grant gives already, as the agent of the owner reads its history.
`Send` queues there while the agent of the thread hands on, and answers no
question the thread asks: only `Reply` does, which keeps its own authorization.

Gateway implementations pass `plugin.WithGatewayName("matrix")` (or their own
stable transport name, such as `"whatsapp"`) to `NewGatewayAdapter` and
`NewGatewayAdapterWithBindings`. This generically attributes text, images and
question replies as `source.interface = api` with `source.gateway = matrix`,
including in archives and model context. Gateway names are not a fixed list;
use ASCII letters, digits, dots, underscores or hyphens, up to 64 bytes. The
name is host configuration, never message content or an account identifier, and
does not affect authorization. Adapters without a name retain plain API attribution.

### Per-message context

Gateways can keep descriptive state per authenticated room and sender and attach
an immutable JSON snapshot to each input. Construct it with
`plugin.NewGatewayMetadata(map[string]any{...})`, then pass
`plugin.WithGatewayMetadata(ctx, snapshot)` to the adapter's Send or Reply
(including image and attachment variants). The adapter supplies its own source
interface and gateway name; caller-provided MessageSource attribution is not
inherited. Invalid JSON values or snapshots larger than 16 KiB are rejected.
Never include tokens, device keys or other credentials.

Snapshots are stored as `source.metadata` JSON objects with the message, survive
socket transport and archives, and are appended to model input as descriptive
gateway data, not instructions. Later state changes do not alter older messages.
Metadata grants no authority and is not a way to change permission policy.
The gateway owns state collection, isolation and freshness; the core does not
interpret transport-specific fields. A new call without context inherits none.

Matrix collects static `m.location` messages and MSC3672 live beacons without
starting an assistant answer. The next text/media message carries `location`
with `uri`, `timestamp` (Unix milliseconds), `live`, `active`, optional
`description`, and `expires_at` for live sharing. `active` describes live sharing
at receipt, not coordinate freshness; always consider the point's timestamp.
Stopped/expired live locations are retained only as last-known points with
`active: false`. State is in memory per connection and room/sender. On reconnect,
current beacon announcements can be restored from joined-room state, but a new
coordinate update is required; static locations must be shared again.

Coordinates require the same trusted-device decryption and private-room checks
as incoming messages. Beacon start/stop announcements normally are plaintext
Matrix room state; these are accepted only from joined-room sync with the
allowlisted sender's state key and rechecked encrypted one-to-one membership.
These announcements are homeserver-authenticated, not device-authenticated, and
cannot supply coordinates. The tracker currently keeps the latest sharing
session per room and sender, not multiple concurrent independent trackers.

## Matrix

Use make build or make install: these select the pure-Go Matrix crypto backend
with the goolm build tag. Raw Go builds without that tag deliberately refuse
Matrix operation rather than falling back to plaintext. The regular Makefile
test targets also select goolm.

Open **Settings → Gateways → Matrix** in the desired profile. Enter the HTTPS
homeserver URL and full bot account ID (for example @ouro:example.org). Enter the
password to sign in, add allowed full user IDs (such as @lucas:example.org),
enable the gateway and save. An empty allowlist allows nobody. The homeserver
must support password login; SSO-only login is not implemented.

**Only allow people you trust with this profile's agent tools.** The existing
session backend executes with the profile's agent permissions. Allowlisting is a
deliberate delegation of those permissions, including answering agent permission
questions; it is not a sandbox or restricted guest-chat role. It does not expose
the local settings/trust UI. Use a separate profile with a restricted tool
configuration if remote users should have fewer capabilities.

The password is used only for login. Access tokens, device keys, sync state and
conversation mappings live under that profile's protected gateways/matrix
directory. Directory/file permissions protect these local credentials; this is
not encryption at rest. Never commit or share that directory.

The serving node owns the network connection. Opening another local UI does not
create another Matrix device or sync loop. Changes are picked up by the running
node; the gateway is unavailable while the node is stopped. Use a separate bot
account for each profile.

Every room talks in the thread: one conversation about everything, whose agent
hands each message to the Ouro session of its topic, or starts one, and whose
replies are the answers and questions of those sessions, see "The thread" in the
the [manual](docs/manual.md). So there is no /new: a new topic is a new session by itself. A room is
not sent what the thread said before it talked there; after that it is sent all
the thread answers and asks, also to what was written in the terminal UI, and
what the owner wrote in the thread elsewhere, messages and answers to its
questions, as "You, in the terminal:" (or "in the app", "via GATEWAY") and the
words, since the bot cannot write as the owner; what any room wrote is not sent
to the rooms. A room kept with an earlier delivery state takes all of the thread
so far as sent, once, and is sent what comes from then on. A room
that had a session of its own before talks in the thread from the next start of
the gateway on, without being sent the thread so far; its session stays in Ouro,
and the thread hands messages on to it while it is of the last few hours the
settings name, 3 hours by default. Replies
contain agent output and questions, not raw tool arguments, raw tool output or
hidden reasoning. Ordinary messages go to the thread, or answer the pending
question when there is one. Just send your answer; for numbered options, sending
the number selects that option. /answer YOUR ANSWER remains available. Use
/cancel to stop what the thread and its sessions work on. A room says where the
shared conversation works and moves it: /project (or /cd) answers with its
project directory, /cd PATH changes it, /cd --workspace returns to the profile
workspace. The project is the thread's, so it is the same one the terminal shows;
relative paths of tools and the project scope of memories resolve there, and a
conversation that answers or works on a delegated task is not moved. Pictures go
to the session the thread hands the message to. The bot types while anything works for
the thread, and not while it waits for an answer.

The Matrix plugin contributes an output instruction through an answer hook when
the latest user input is attributed to Matrix. It asks for short replies and says
that Markdown is rendered, so that answers may use emphasis, lists, tables, links
and code fences. It appears once in each model request throughout that answer, is
not stored in conversation history, and does not apply to other interfaces or peer
conversations. Outgoing Markdown is rendered into the HTML subset that the Matrix
specification permits and sent next to the plain body, which clients that render
no HTML show instead; raw HTML in an answer is shown as text, a line break stays
a line break, and a fenced code block is closed and reopened when a message is
split. A part is split at the JSON size one encrypted event may carry, escapes
included, and HTML that no longer fits next to its plain body is left out, so
that the body alone is sent; other gateways may contribute
their own answer hooks and formatting without adding transport-specific rules to
the core.

Outgoing answers and commentary use opaque presentation IDs, persisted with the
archive and reused when session events are reconstructed after a restart. The
Matrix delivery ledger tracks these IDs, not timestamps or usage metadata; two
identical messages still have different IDs. Legacy delivery hashes cannot be
converted reliably. On the first upgrade, the existing snapshot is baselined as
already delivered (including any previously undelivered output); subsequent
output uses the new ledger. This is not an exactly-once outbox: a send that
succeeds before the ledger save fails can still be retried. Pending questions
remain live-only and are not restored from the archive.

Matrix renders a pending restart as “Restarting…” when observed before the
connection closes. A durable backend marker produces a separate “Restarted.”
event only in the replacement process, delivered to the triggering conversation
when Matrix reconnects. Completion events have stable IDs, so reconnects and
repeated polls do not resend them after successful ledger persistence. The TUI
keeps its existing restart behavior; it does not display these gateway notices.

The initial transport is limited to encrypted private one-to-one rooms with an
allowlisted user. It must refuse group/public/unencrypted rooms and recheck
membership before sending. Image messages (PNG, JPEG, GIF and WebP, up to
10 MiB) are downloaded through the authenticated homeserver media endpoint,
decrypted with integrity verification when encrypted, and passed to the model
with their caption. Image bytes remain in the live model context, not the
archived chat history; resend an image if needed after a restart.
Other attachments and rich streaming edits are not supported. Images cannot
answer a pending agent question: answer it with text first, then resend the image.

### Gateway output tools

Gateway plugins contribute output capabilities; the agent uses the neutral
`gateway_list`, `gateway_send_image`, `gateway_send_audio`, and
`gateway_set_avatar` tools rather than protocol-specific tools. `gateway_list`
describes registered capabilities and backend restrictions, not connection
health. A capability can be registered while its account is disabled or
disconnected; the backend then rejects the action. Specify `gateway` when more
than one provider supports the operation. Message source metadata does not
select an account or recipient.

For example, `gateway_send_image({gateway: "matrix", path: "/path/to/image.png"})`
sends through the running Matrix connection. Alternatively use `attachment: 1`
for a live chat image. `gateway_send_audio({path: "/path/to/sound.wav"})` sends
a local audio file as an encrypted audio message, so sounds can be heard away
from the machine. Matrix accepts `.wav`, `.ogg`, `.mp3` and `.m4a` files of up
to 10 MiB whose content matches the extension. If multiple known rooms exist,
specify `destination` as the Matrix room ID. The backend binds that room to its
allowed recipient and retains its encryption and membership checks. Unknown
rooms are not accepted.

The former `matrix_send_image` and `matrix_set_avatar` tools are no longer
registered. Explicit owner-tool allowlists and tool-specific permission rules
must be updated to the corresponding neutral names; include `gateway_list` for
capability discovery. This is not done automatically: granting a neutral tool
can also grant access to other installed providers of that capability.

### Set a gateway profile picture

The owner-only agent tool `gateway_set_avatar({gateway: "matrix", path: "/path/to/avatar.png"})`
sets the configured bot account's profile picture. To use an image sent in this
chat instead of a file, call `gateway_set_avatar({gateway: "matrix", attachment: 1})` (or omit
both path and attachment for the first image). The index selects an image in the most recent user message
containing pictures, even after subsequent text-only turns. Only this live
conversation is accessible, never other chats or tool-generated pictures.
Attachments stay in memory and must be resent after a restart or when trimmed
from the conversation; they are not written to the chat archive.
Matrix must be enabled and
logged in. PNG, JPEG and GIF files up to 5 MiB, 4096 pixels per side and
4 megapixels overall are supported; the normal protected-file rules apply.
Relative paths use the current project or workspace.
The tool uploads the file, sets the account avatar and reads it back to verify
the result. This changes the public account profile, not a room avatar: the
image is **not end-to-end encrypted**. No new login or sync loop is started.
If owner tools are explicitly allowlisted, include `gateway_set_avatar` there;
it cannot be granted to peers.

### Cross-signing of the Matrix device

Matrix clients warn about messages from a device its own account has not
signed (“not verified by its owner”). Each time the gateway connects, Ouro
therefore makes the bot account vouch for its device; no verification by hand
is needed. A bot account without cross-signing keys gets new ones. Their
private parts go to the account's secret storage on the homeserver, and the
recovery key that unlocks it stays in the protected gateway state next to the
crypto directories. A later login of the same account signs its new device
with them. Homeservers take the first keys of an account without a password.

If the account already has cross-signing keys whose recovery key Ouro does not
have, for example set up in another client, Ouro leaves them alone and the
gateway status says why clients still warn. The gateway works either way.

Logout disables the gateway and forgets its locally stored access token.
Device/room key state remains protected on disk; remove unwanted sessions also
in the homeserver's account/device settings when revoking a device.

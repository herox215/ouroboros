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

Gateway implementations pass `plugin.WithGatewayName("matrix")` (or their own
stable transport name, such as `"whatsapp"`) to `NewGatewayAdapter` and
`NewGatewayAdapterWithBindings`. This generically attributes text, images and
question replies as `source.interface = api` with `source.gateway = matrix`,
including in archives and model context. Gateway names are not a fixed list;
use ASCII letters, digits, dots, underscores or hyphens, up to 64 bytes. The
name is host configuration, never message content or an account identifier, and
does not affect authorization. Adapters without a name retain plain API attribution.

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

Each room is associated with its own Ouro session. Replies contain agent output
and questions, not raw tool arguments, raw tool output or hidden reasoning.
Ordinary messages start an answer, or answer the pending agent question when
there is one. Just send your answer; for numbered options, sending the number
selects that option. /answer YOUR ANSWER remains available. Use /cancel to stop
the current answer. Use /new to start a fresh Ouro
conversation in the same Matrix room. This stops the previous answer, preserves
the old chat in Ouro, and keeps the new room binding across restarts.

The Matrix plugin contributes a plain-text output instruction through an answer
hook when the latest user input is attributed to Matrix. It appears once in each
model request throughout that answer, is not stored in conversation history, and
does not apply to other interfaces or peer conversations. Outgoing text is not
rewritten or stripped of Markdown after generation. Other gateways may contribute
their own answer hooks without adding transport-specific rules to the core.

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

### Set the Matrix profile picture

The owner-only agent tool `matrix_set_avatar({path: "/path/to/avatar.png"})`
sets the configured bot account's profile picture. To use an image sent in this
chat instead of a file, call `matrix_set_avatar({attachment: 1})` (or `{}` for
the first image). The index selects an image in the most recent user message
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
If owner tools are explicitly allowlisted, include `matrix_set_avatar` there;
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

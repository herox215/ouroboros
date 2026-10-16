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
Ordinary messages start an answer. Use /cancel to stop it; answer a pending agent
question explicitly with /answer YOUR ANSWER.

The initial transport is limited to encrypted private one-to-one rooms with an
allowlisted user. It must refuse group/public/unencrypted rooms and recheck
membership before sending. Attachments, rich streaming edits and interactive
device-verification/recovery UI are outside this first version.

Logout disables the gateway and forgets its locally stored access token.
Device/room key state remains protected on disk; remove unwanted sessions also
in the homeserver's account/device settings when revoking a device.

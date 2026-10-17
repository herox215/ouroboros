// Package matrix is an encrypted-only Matrix bot transport backed by mautrix.
//
// Build with -tags=goolm to select mautrix's pure Go Olm/Megolm implementation.
// Without that tag Login still works, but Run fails explicitly: there is never a
// plaintext fallback. No libolm installation is needed. SQLite uses modernc.
//
// Create a client with New, run Run with a cancellable context and a callback,
// cancel and wait for Run to return, then Close. Callbacks run synchronously in
// the sync loop and should hand long work to a bounded queue. Send may be called
// concurrently but sends are serialized. Recreate the client to restart Run.
// StartVerification initiates a SAS exchange with another device of the same
// account. Show VerificationStatus's seven ordered emojis only to the local
// owner, then call ConfirmVerification with that exact transaction ID only
// after explicit comparison. Verification is not a room message command.
// Active verification transactions expire and are never resumed after restart.
//
// StateDir contains private, identity-specific crypto directories keyed by the
// homeserver, user and device. Do not copy or share an active device's directory.
// A nonblocking process lock prevents concurrent use of one crypto identity.
//
// Only direct, authenticated Megolm text and image events from exact allowlisted Matrix
// IDs in encrypted rooms with exactly two joined members are delivered. Any
// additional invited, joined or knocking member blocks receipt and sending.
// Full current state is fetched before handling messages and before sending;
// no membership-cache fallback is used on errors. Outbound keys are rotated for
// every reply and shared only with the two validated room users. Historical key
// requests are refused. A homeserver can always race membership state changes;
// E2EE prevents newly invited users without the session key reading replies.
//
// The initial sync and pre-start events are never delivered. Newly joined room
// timelines are suppressed too. Decryption retries are bounded and only cover
// live sync events; the transport never fetches room history. Empty allowlists
// deny every sender. Invite autojoin requires an exact allowlisted inviter,
// encrypted stripped state, and no visible third active member. Invite state is
// partial and never sufficient to authorize handling or sending a message.
package matrix

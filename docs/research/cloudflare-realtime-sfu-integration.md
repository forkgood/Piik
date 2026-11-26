# Cloudflare Realtime SFU integration

This document records the implementation boundary for the optional Cloudflare Realtime
media backend. It is intentionally separate from the existing Pion SFU.

## Goals

- Keep P2P as the default route.
- Keep the embedded Pion SFU available as a separate route.
- Add Cloudflare Realtime as an independently selectable media provider.
- Keep the Cloudflare App Secret on the Piik Server only.
- Reuse Piik room membership, route admission, publication generations, viewer
  authorization, and cleanup ownership.

## Existing Piik boundary

The v2.8.0 server routes media through `internal/server/sfu.Runtime`. The current
implementation owns Pion PeerConnections, ICE candidates, publication metadata,
subscriber transports, and physical resource cleanup. The room router authorizes
every SFU signal against room/session/publication fences before invoking that
runtime. The browser's `SfuPublisher` and `SfuSubscriber` currently exchange SDP
and trickled ICE candidates over the authenticated room WebSocket.

Cloudflare Realtime is not a drop-in implementation of that interface: its
Connection API is an HTTPS control plane. It creates a server-side session from
an SDP offer and returns an answer; publication and subscription are expressed
as track operations; subscriber answers are submitted through renegotiation.
Therefore the Cloudflare adapter needs a provider-specific negotiation contract
rather than pretending that Cloudflare accepts Pion's per-candidate methods.

## Current implementation checkpoint

The repository branch now includes:

- `CLOUDFLARE_SFU_APP_ID`, `CLOUDFLARE_SFU_APP_SECRET`, and optional
  `CLOUDFLARE_SFU_API_BASE_URL` parsing in server configuration.
- Validation that the App ID and App Secret are supplied together and that the
  API base URL uses HTTPS.
- Construction of the Cloudflare API client during application composition.
  No API request is made at startup, and the App Secret is not included in
  runtime capabilities.
- A Go API client for session creation/inspection, track create/update/close,
  renegotiation, and DataChannel transport/allocation/update/close operations.

- A per-session operation queue with cancellation and independent-session concurrency. Its callback boundary spans each API + SDP exchange.
- A server-side negotiation bridge that creates publisher sessions and local-track publications from a gathered Host offer, creates viewer sessions and remote-track subscriptions, and completes viewer SDP answers through renegotiation. The bridge remains policy-free; authenticated room signaling now owns its invocation and resource registration.
- A thread-safe ownership registry that binds Cloudflare session IDs to the exact Piik room, share generation, peer, signaling session, and role. It rejects cross-generation subscriptions and stale signaling owners, and returns immutable cleanup snapshots when a peer or share retires. It does not perform network cleanup itself; the signal lifecycle snapshots ownership and performs best-effort track closure outside the authority lock.

The Cloudflare Connection API is invoked by an authenticated WebSocket control
path. The server advertises Cloudflare availability in runtime capabilities and
marks each SFU route config with the provider choice. Host and Viewer route
factories select Cloudflare transport adapters for marked configurations; the
Pion publisher/subscriber remains the fallback when Cloudflare is not configured.
The provider-specific path has not yet been verified in a live end-to-end session.

## Implemented control flow (not yet live-E2E verified)

1. Host browser creates a Cloudflare-compatible publishing offer and gathers ICE
   candidates before submitting the completed SDP to Piik Server.
2. Piik Server validates the authenticated Host, room, share generation, route
   revision, and connection fence using the existing router authority.
3. Piik Server calls Cloudflare `sessions/new`, then publishes the requested
   screen video/audio tracks with `tracks/new`. The App Secret never leaves the
   server.
4. Server stores the Cloudflare session ID and track identifiers under the exact
   Piik room/share/publication generation.
5. An authorized Viewer asks to subscribe. The server creates that viewer's
   Cloudflare session and calls `tracks/new` with remote track descriptors from
   the Host publication. It returns the Cloudflare offer to the Viewer.
6. Viewer creates and gathers an SDP answer, then sends it to Piik Server.
   Server submits it through Cloudflare `renegotiate`.
7. Track/session close and room/share retirement release the corresponding
   Cloudflare resources. Mutations for one Cloudflare session are serialized.

## Data channels

Piik's remote-input feature currently uses a browser-created WebRTC DataChannel.
Cloudflare Realtime exposes a separate DataChannels API. Remote input must be
migrated explicitly to that API (including channel establishment, message
framing, authorization, and close); it must not silently assume the existing
RTCDataChannel object works through Cloudflare.

## Failure handling

- Check per-track result errors even when the HTTP response is successful.
- Serialize all mutating requests for a Cloudflare session.
- Do not blindly replay a request when the response is lost and the remote
  operation may already have succeeded. Reconcile session state or replace the
  affected session.
- Keep Cloudflare session IDs and secrets out of client-visible signaling.
- Close resources idempotently when a Piik publication, viewer subscription,
  room, or server lifecycle ends.

## Implementation stages

1. Add validated Cloudflare configuration and a server-only Connection API client.
2. Add authenticated Piik control messages and server-side publication/session
   ownership, with tests for room and generation fences.
3. Add browser offer/answer gathering and Cloudflare publication/subscription
   flows while retaining the existing Pion implementation.
4. Add DataChannels support for remote input.
5. Add provider selection, lifecycle/failure tests, and build/test gates.

The initial integration should be opt-in and should not change the existing
P2P-first or Pion-SFU behavior for deployments without Cloudflare configuration.


## Track lifecycle hardening

The negotiation bridge now validates returned track identities (stable track name, location, MID uniqueness, and publisher session for remote subscriptions) before callers may register them. It also exposes force-close cleanup for the exact recorded MIDs, serialized with other mutations on that Cloudflare session. This is a cleanup primitive, not yet wired to Piik disconnect/share/room lifecycle hooks.


## Browser SDP helper

The browser package now contains provider-specific helpers to create a publisher offer or answer a subscriber offer only after ICE gathering reaches `complete`. Both operations are abortable, bounded by a 15-second ICE gathering timeout, and reject empty SDP or SDP without gathered candidate/end-of-candidates evidence. The helper is now called by the Cloudflare Publisher / Subscriber route adapters through the room WebSocket protocol.


## Negotiated DataChannel bridge

The Cloudflare bridge now exposes serialized operations for:
- establishing a reserved remote channel such as `server-events`;
- allocating local or remote negotiated DataChannels and validating the returned endpoint-local IDs;
- explicitly granting or revoking a remote subscriber's `canReply` permission for a named channel;
- closing exact channel IDs allocated on a specific endpoint Session.

The bridge returns IDs and any SDP description to the signaling owner. It does not itself create browser DataChannels, grant room permissions, or bind channel IDs to Registry ownership. Those steps must be completed by the Piik signaling and browser transport integration before remote input is usable.


## Browser negotiated remote-input channel

The existing `RemoteInputChannel` now has an explicit negotiated-channel attachment path. It creates Piik's canonical input channel label with `negotiated: true` and the exact endpoint-local ID returned by Cloudflare, and retains the existing open/close/current semantics. Invalid SCTP stream IDs are rejected. This remains a browser-side primitive: Piik has not yet connected Cloudflare channel allocation to room authorization and route activation.


## Complete per-session resource ownership

The Registry can now retain both media MIDs and endpoint-local negotiated DataChannel IDs in the same immutable session resource snapshot. Channel IDs are range-checked, duplicate IDs are rejected, and returned cleanup snapshots deep-copy both slices. `Bridge.CloseResources` attempts exact track and DataChannel cleanup for that snapshot and joins any errors, so failure in one cleanup does not prevent attempting the other.


## Authenticated Cloudflare signaling checkpoint

The Go and TypeScript protocols now define a separate `cloudflare-sfu`
message family:
- Host `publish`: full gathered SDP offer plus unique MID / stable track-name pairs.
- Viewer `subscribe`: no publisher session ID is accepted from the browser; the server resolves the current publisher from its Registry.
- Viewer `complete`: full gathered SDP answer. The server resolves the subscriber session by authenticated owner and browser connection fence.

The signal server performs authority checks under its lock, runs Cloudflare HTTP
operations outside that lock, and revalidates the exact signaling session and
share generation before registering resources or returning a response. If the
operation becomes stale or registration fails, it attempts cleanup of the
newly allocated Cloudflare tracks. The Registry stores stable track names,
Cloudflare MIDs, and the browser connection fence for each endpoint.

The signaling lifecycle now retires exact Cloudflare resources on Host/Viewer
disconnect, stop-sharing, room closure, viewer revocation, and server shutdown.
Cleanup snapshots are detached from Registry locks and HTTP cleanup runs outside
the signal server lock.

## Browser Cloudflare SDP transport checkpoint

`src/client/sfu/cloudflare-session.ts` now contains separate Host publisher and
Viewer subscriber helpers. They exchange the new typed signaling messages,
create full-gathering SDP, apply the Cloudflare answer/offer, and wait for the
Viewer completion acknowledgement. The caller supplies the WebSocket response
correlator and may supply an existing RTCPeerConnection. A reply broker buffers
responses that arrive before the awaiting code registers its waiter and fences
replies by both operation kind and connection ID.

The helpers are now wired into `host-sfu-route` / `viewer-sfu-route` through
provider-specific transport adapters. The server marks Cloudflare routes when
credentials are configured; clients select the adapter for those route configs.
The Viewer adapter attaches received audio/video tracks to Piik presentation
streams and reports decoded-frame progress. Remote-input DataChannel negotiation
and live Cloudflare end-to-end tests remain pending. P2P is unchanged, and the
Native Pion route remains the fallback for deployments without Cloudflare config.

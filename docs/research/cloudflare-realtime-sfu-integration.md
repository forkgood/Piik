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

This checkpoint is deliberately not advertised as a usable Cloudflare route:
the signal router and browser still use the existing Pion SFU contract. The
Cloudflare client is initialized but not yet invoked by room signaling.

## Proposed control flow

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

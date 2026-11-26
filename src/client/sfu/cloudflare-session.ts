import type { CloudflareSfuClientMessage, CloudflareSfuServerMessage, CloudflareSfuTrack, SessionDescription } from "../../shared/protocol";
import { createCloudflareAnswer, createCloudflareOffer } from "./cloudflare-sdp";

export type CloudflareSfuSignaling = {
  publicationGeneration: string;
  connectionId: string;
  send(message: CloudflareSfuClientMessage): void;
  waitForReply(kind: CloudflareSfuServerMessage["kind"], connectionId: string, signal: AbortSignal): Promise<CloudflareSfuServerMessage>;
};

function assertFence(reply: CloudflareSfuServerMessage, signaling: CloudflareSfuSignaling): void {
  if (reply.publicationGeneration !== signaling.publicationGeneration || reply.connectionId !== signaling.connectionId) {
    throw new Error("Cloudflare SFU response does not match the active publication");
  }
}
function description(value: SessionDescription): RTCSessionDescriptionInit {
  return { type: value.type, sdp: value.sdp };
}
async function closeOnFailure<T>(pc: RTCPeerConnection, operation: Promise<T>): Promise<T> {
  try { return await operation; } catch (error) { pc.close(); throw error; }
}

export type CloudflareSfuTrackInput = { track: MediaStreamTrack; trackName: string; stream?: MediaStream };

// Browser-side Host transport. Route lifecycle and disposal remain with the caller.
export async function createCloudflarePublisher(
  tracks: CloudflareSfuTrackInput[],
  signaling: CloudflareSfuSignaling,
  signal: AbortSignal,
  pc = new RTCPeerConnection({ iceServers: [] }),
): Promise<{ pc: RTCPeerConnection; sessionId: string; tracks: CloudflareSfuTrack[] }> {
  if (tracks.length < 1 || tracks.length > 4) { pc.close(); throw new Error("Cloudflare publisher requires one to four tracks"); }
  const names = new Set<string>();
  const senders = tracks.map(({ track, trackName, stream }) => {
    if (!trackName || names.has(trackName) || track.readyState !== "live") { pc.close(); throw new Error("Cloudflare publisher tracks must be live and uniquely named"); }
    names.add(trackName);
    return { trackName, sender: pc.addTrack(track, stream ?? new MediaStream([track])) };
  });
  return closeOnFailure(pc, (async () => {
    const offer = await createCloudflareOffer(pc, signal);
    const localTracks = senders.map(({ trackName, sender }) => {
      const transceiver = pc.getTransceivers().find((item) => item.sender === sender);
      if (!transceiver?.mid) throw new Error("Cloudflare publisher transceiver has no MID");
      return { mid: transceiver.mid, trackName };
    });
    signaling.send({ type: "cloudflare-sfu", kind: "publish", publicationGeneration: signaling.publicationGeneration,
      connectionId: signaling.connectionId, description: offer, tracks: localTracks });
    const reply = await signaling.waitForReply("published", signaling.connectionId, signal);
    assertFence(reply, signaling);
    if (reply.kind !== "published" || !reply.description || !reply.sessionId || !reply.tracks) throw new Error("Cloudflare publisher response is incomplete");
    await pc.setRemoteDescription(description(reply.description));
    return { pc, sessionId: reply.sessionId, tracks: reply.tracks };
  })());
}

// Browser-side Viewer transport. onTrack attaches received media to Piik presentation.
export async function createCloudflareSubscriber(
  signaling: CloudflareSfuSignaling,
  signal: AbortSignal,
  onTrack: (event: RTCTrackEvent) => void,
  pc = new RTCPeerConnection({ iceServers: [] }),
): Promise<{ pc: RTCPeerConnection; sessionId: string; tracks: CloudflareSfuTrack[] }> {
  pc.addEventListener("track", onTrack);
  return closeOnFailure(pc, (async () => {
    signaling.send({ type: "cloudflare-sfu", kind: "subscribe", publicationGeneration: signaling.publicationGeneration, connectionId: signaling.connectionId });
    const reply = await signaling.waitForReply("subscribed", signaling.connectionId, signal);
    assertFence(reply, signaling);
    if (reply.kind !== "subscribed" || !reply.description || !reply.sessionId || !reply.tracks) throw new Error("Cloudflare subscriber response is incomplete");
    const answer = await createCloudflareAnswer(pc, description(reply.description), signal);
    signaling.send({ type: "cloudflare-sfu", kind: "complete", publicationGeneration: signaling.publicationGeneration, connectionId: signaling.connectionId, description: answer });
    const completed = await signaling.waitForReply("completed", signaling.connectionId, signal);
    assertFence(completed, signaling);
    if (completed.kind !== "completed") throw new Error("Cloudflare subscriber completion was not acknowledged");
    return { pc, sessionId: reply.sessionId, tracks: reply.tracks };
  })());
}

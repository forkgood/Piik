import type { CloudflareSfuClientMessage, CloudflareSfuServerMessage, CloudflareSfuTrack, SessionDescription } from "../../shared/protocol";
import { createCloudflareAnswer, createCloudflareOffer } from "./cloudflare-sdp";

export type CloudflareSfuSignaling = {
  shareGeneration: string;
  connectionId: string;
  send(message: CloudflareSfuClientMessage): void;
  waitForReply(kind: CloudflareSfuServerMessage["kind"], connectionId: string, signal: AbortSignal): Promise<CloudflareSfuServerMessage>;
};

function assertFence(reply: CloudflareSfuServerMessage, signaling: CloudflareSfuSignaling): void {
  if (reply.shareGeneration !== signaling.shareGeneration || reply.connectionId !== signaling.connectionId) {
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
): Promise<{ pc: RTCPeerConnection; tracks: CloudflareSfuTrack[] }> {
  if (tracks.length < 1 || tracks.length > 4) { pc.close(); throw new Error("Cloudflare publisher requires one to four tracks"); }
  const names = new Set<string>();
  const senders = tracks.map(({ track, trackName, stream }) => {
    if (!trackName || names.has(trackName) || track.readyState !== "live") { pc.close(); throw new Error("Cloudflare publisher tracks must be live and uniquely named"); }
    names.add(trackName);
    return { trackName, sender: pc.addTrack(track, stream ?? new MediaStream([track])) };
  });
  return closeOnFailure(pc, (async () => {
    const gatheredOffer = await createCloudflareOffer(pc, signal);
    const offer = { type: "offer" as const, sdp: gatheredOffer.sdp ?? "" };
    const localTracks = senders.map(({ trackName, sender }) => {
      const transceiver = pc.getTransceivers().find((item) => item.sender === sender);
      if (!transceiver?.mid) throw new Error("Cloudflare publisher transceiver has no MID");
      return { mid: transceiver.mid, trackName };
    });
    signaling.send({ type: "cloudflare-sfu", kind: "publish", shareGeneration: signaling.shareGeneration,
      connectionId: signaling.connectionId, description: offer, tracks: localTracks });
    const reply = await signaling.waitForReply("published", signaling.connectionId, signal);
    assertFence(reply, signaling);
    if (reply.kind !== "published" || !reply.description || !reply.tracks) throw new Error("Cloudflare publisher response is incomplete");
    await pc.setRemoteDescription(description(reply.description));
    return { pc, tracks: reply.tracks };
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
    signaling.send({ type: "cloudflare-sfu", kind: "subscribe", shareGeneration: signaling.shareGeneration, connectionId: signaling.connectionId });
    const reply = await signaling.waitForReply("subscribed", signaling.connectionId, signal);
    assertFence(reply, signaling);
    if (reply.kind !== "subscribed" || !reply.description || !reply.sessionId || !reply.tracks) throw new Error("Cloudflare subscriber response is incomplete");
    const gatheredAnswer = await createCloudflareAnswer(pc, description(reply.description), signal);
    const answer = { type: "answer" as const, sdp: gatheredAnswer.sdp ?? "" };
    signaling.send({ type: "cloudflare-sfu", kind: "complete", shareGeneration: signaling.shareGeneration, connectionId: signaling.connectionId, description: answer });
    const completed = await signaling.waitForReply("completed", signaling.connectionId, signal);
    assertFence(completed, signaling);
    if (completed.kind !== "completed") throw new Error("Cloudflare subscriber completion was not acknowledged");
    return { pc, sessionId: reply.sessionId, tracks: reply.tracks };
  })());
}


// Correlates Cloudflare server replies with the browser transport helpers. It
// buffers early replies because a very fast local server may answer before
// the caller begins awaiting the response.
export class CloudflareSfuReplyBroker {
  private readonly pending = new Map<string, {
    resolve: (message: CloudflareSfuServerMessage) => void;
    reject: (error: Error) => void;
    cleanup: () => void;
  }>();
  private readonly buffered = new Map<string, CloudflareSfuServerMessage>();

  accept(message: CloudflareSfuServerMessage): void {
    const key = this.key(message.kind, message.connectionId);
    const pending = this.pending.get(key);
    if (pending) {
      this.pending.delete(key);
      pending.cleanup();
      pending.resolve(message);
      return;
    }
    this.buffered.set(key, message);
  }

  waitForReply(
    kind: CloudflareSfuServerMessage["kind"],
    connectionId: string,
    signal: AbortSignal,
    timeoutMs = 20_000,
  ): Promise<CloudflareSfuServerMessage> {
    const key = this.key(kind, connectionId);
    const buffered = this.buffered.get(key);
    if (buffered) {
      this.buffered.delete(key);
      return Promise.resolve(buffered);
    }
    if (signal.aborted) return Promise.reject(signal.reason);
    if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) {
      return Promise.reject(new Error("Cloudflare reply timeout must be positive"));
    }
    if (this.pending.has(key)) return Promise.reject(new Error("Cloudflare reply is already being awaited"));
    return new Promise((resolve, reject) => {
      const timer = setTimeout(() => finish(new Error("Cloudflare SFU signaling timed out")), timeoutMs);
      const onAbort = (): void => finish(signal.reason instanceof Error ? signal.reason : new DOMException("Operation aborted", "AbortError"));
      const cleanup = (): void => {
        clearTimeout(timer);
        signal.removeEventListener("abort", onAbort);
      };
      const finish = (error?: Error, message?: CloudflareSfuServerMessage): void => {
        this.pending.delete(key);
        cleanup();
        if (error) reject(error);
        else if (message) resolve(message);
      };
      this.pending.set(key, { resolve: (message) => finish(undefined, message), reject: (error) => finish(error), cleanup });
      signal.addEventListener("abort", onAbort, { once: true });
      if (signal.aborted) onAbort();
    });
  }

  close(reason = new Error("Cloudflare signaling broker closed")): void {
    for (const pending of this.pending.values()) {
      pending.cleanup();
      pending.reject(reason);
    }
    this.pending.clear();
    this.buffered.clear();
  }

  private key(kind: CloudflareSfuServerMessage["kind"], connectionId: string): string {
    return kind + "\\0" + connectionId;
  }
}

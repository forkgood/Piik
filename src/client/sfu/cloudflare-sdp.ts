import { waitForConnectionOperation } from "../webrtc/connection-operation";

const DEFAULT_ICE_GATHERING_TIMEOUT_MS = 15_000;

// Cloudflare's Connection API expects complete SDP with gathered candidates;
// unlike native Piik signaling, this path does not trickle ICE afterward.
export async function waitForIceGatheringComplete(
  pc: RTCPeerConnection,
  signal: AbortSignal,
  timeoutMs = DEFAULT_ICE_GATHERING_TIMEOUT_MS,
): Promise<void> {
  signal.throwIfAborted();
  if (pc.iceGatheringState === "complete") return;
  if (!Number.isFinite(timeoutMs) || timeoutMs <= 0) {
    throw new Error("ICE gathering timeout must be a positive finite number");
  }
  await new Promise<void>((resolve, reject) => {
    let settled = false;
    const finish = (error?: Error): void => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      pc.removeEventListener("icegatheringstatechange", onStateChange);
      signal.removeEventListener("abort", onAbort);
      if (error) reject(error);
      else resolve();
    };
    const onStateChange = (): void => {
      if (pc.iceGatheringState === "complete") finish();
    };
    const onAbort = (): void => {
      finish(signal.reason instanceof Error ? signal.reason : new DOMException("Operation aborted", "AbortError"));
    };
    const timer = setTimeout(
      () => finish(new Error(`ICE gathering did not complete within ${timeoutMs} ms`)),
      timeoutMs,
    );
    pc.addEventListener("icegatheringstatechange", onStateChange);
    signal.addEventListener("abort", onAbort, { once: true });
    onStateChange();
    if (signal.aborted) onAbort();
  });
}

function requireGatheredDescription(
  description: RTCSessionDescription | RTCSessionDescriptionInit | null,
  type: "offer" | "answer",
): RTCSessionDescriptionInit {
  if (!description || description.type !== type || !description.sdp) {
    throw new Error(`Cloudflare peer has no gathered local SDP ${type}`);
  }
  if (!description.sdp.includes("a=candidate:") && !description.sdp.includes("a=end-of-candidates")) {
    throw new Error("Cloudflare SDP contains no gathered ICE candidates");
  }
  return { type, sdp: description.sdp };
}

export async function createCloudflareOffer(
  pc: RTCPeerConnection,
  signal: AbortSignal,
  options?: RTCOfferOptions,
): Promise<RTCSessionDescriptionInit> {
  const offer = await waitForConnectionOperation(signal, () => pc.createOffer(options));
  await waitForConnectionOperation(signal, () => pc.setLocalDescription(offer));
  await waitForIceGatheringComplete(pc, signal);
  return requireGatheredDescription(pc.localDescription, "offer");
}

export async function createCloudflareAnswer(
  pc: RTCPeerConnection,
  offer: RTCSessionDescriptionInit,
  signal: AbortSignal,
): Promise<RTCSessionDescriptionInit> {
  if (offer.type !== "offer" || !offer.sdp) {
    throw new Error("Cloudflare subscriber requires a non-empty remote SDP offer");
  }
  await waitForConnectionOperation(signal, () => pc.setRemoteDescription(offer));
  const answer = await waitForConnectionOperation(signal, () => pc.createAnswer());
  await waitForConnectionOperation(signal, () => pc.setLocalDescription(answer));
  await waitForIceGatheringComplete(pc, signal);
  return requireGatheredDescription(pc.localDescription, "answer");
}

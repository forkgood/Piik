import { afterEach, describe, expect, it, vi } from "vitest";
import {
  createCloudflareAnswer,
  createCloudflareOffer,
  waitForIceGatheringComplete,
} from "../src/client/sfu/cloudflare-sdp.ts";

class FakePeerConnection extends EventTarget {
  iceGatheringState: RTCIceGatheringState = "gathering";
  localDescription: RTCSessionDescriptionInit | null = null;
  readonly createOffer = vi.fn(async () => ({ type: "offer" as const, sdp: "offer" }));
  readonly createAnswer = vi.fn(async () => ({ type: "answer" as const, sdp: "answer" }));
  readonly setLocalDescription = vi.fn(async (description: RTCSessionDescriptionInit) => {
    this.localDescription = {
      ...description,
      sdp: `${description.sdp}\r\na=candidate:1 1 udp 1 192.0.2.1 5000 typ host\r\n`,
    };
  });
  readonly setRemoteDescription = vi.fn(async (_description: RTCSessionDescriptionInit) => undefined);
  gather(): void {
    this.iceGatheringState = "complete";
    this.dispatchEvent(new Event("icegatheringstatechange"));
  }
}

afterEach(() => vi.useRealTimers());

describe("Cloudflare full-SDP negotiation helpers", () => {
  it("waits for ICE gathering before returning the publisher offer", async () => {
    vi.useFakeTimers();
    const pc = new FakePeerConnection();
    const controller = new AbortController();
    const pending = createCloudflareOffer(pc as unknown as RTCPeerConnection, controller.signal);
    await Promise.resolve();
    await Promise.resolve();
    await Promise.resolve();
    expect(pc.createOffer).toHaveBeenCalledOnce();
    expect(pc.setLocalDescription).toHaveBeenCalledOnce();
    pc.gather();
    await expect(pending).resolves.toEqual({
      type: "offer",
      sdp: "offer\r\na=candidate:1 1 udp 1 192.0.2.1 5000 typ host\r\n",
    });
  });

  it("applies Cloudflare's offer and returns a fully gathered answer", async () => {
    const pc = new FakePeerConnection();
    const controller = new AbortController();
    const pending = createCloudflareAnswer(
      pc as unknown as RTCPeerConnection,
      { type: "offer", sdp: "remote-offer" },
      controller.signal,
    );
    await Promise.resolve();
    expect(pc.setRemoteDescription).toHaveBeenCalledWith({ type: "offer", sdp: "remote-offer" });
    pc.gather();
    await expect(pending).resolves.toEqual({
      type: "answer",
      sdp: "answer\r\na=candidate:1 1 udp 1 192.0.2.1 5000 typ host\r\n",
    });
  });

  it("rejects when ICE gathering times out", async () => {
    vi.useFakeTimers();
    const pc = new FakePeerConnection();
    const controller = new AbortController();
    const pending = waitForIceGatheringComplete(pc as unknown as RTCPeerConnection, controller.signal, 100);
    const assertion = expect(pending).rejects.toThrow("ICE gathering did not complete");
    await vi.advanceTimersByTimeAsync(100);
    await assertion;
  });

  it("rejects immediately when the owning connection is aborted", async () => {
    const pc = new FakePeerConnection();
    const controller = new AbortController();
    const pending = waitForIceGatheringComplete(pc as unknown as RTCPeerConnection, controller.signal);
    controller.abort(new Error("peer closed"));
    await expect(pending).rejects.toThrow("peer closed");
  });
});

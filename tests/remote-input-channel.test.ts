import { describe, expect, it, vi } from "vitest";
import { RemoteInputChannel } from "../src/client/webrtc/remote-input-channel.ts";

class FakeDataChannel extends EventTarget {
  readyState: RTCDataChannelState = "connecting";
  label = "piik-input";
  close = vi.fn(() => { this.readyState = "closed"; this.dispatchEvent(new Event("close")); });
  open(): void { this.readyState = "open"; this.dispatchEvent(new Event("open")); }
}

class FakePeerConnection extends EventTarget {
  connectionState: RTCPeerConnectionState = "connected";
  readonly channel = new FakeDataChannel();
  readonly createDataChannel = vi.fn((_label: string, _options?: RTCDataChannelInit) => this.channel as unknown as RTCDataChannel);
}

describe("RemoteInputChannel negotiated transport", () => {
  it("creates the Cloudflare-allocated channel with the exact ID", () => {
    const pc = new FakePeerConnection();
    const changed = vi.fn();
    const input = new RemoteInputChannel(changed);
    const channel = input.attachNegotiated(pc as unknown as RTCPeerConnection, 17);
    expect(pc.createDataChannel).toHaveBeenCalledWith("piik-input", {
      negotiated: true,
      id: 17,
      ordered: true,
    });
    expect(channel).toBe(pc.channel);
    expect(input.current).toBeNull();
    pc.channel.open();
    expect(input.current).toBe(pc.channel);
    expect(changed).toHaveBeenCalled();
  });

  it("rejects invalid negotiated IDs before touching the peer connection", () => {
    const pc = new FakePeerConnection();
    const input = new RemoteInputChannel(() => undefined);
    expect(() => input.attachNegotiated(pc as unknown as RTCPeerConnection, -1)).toThrow("ID is invalid");
    expect(() => input.attachNegotiated(pc as unknown as RTCPeerConnection, 65_535)).toThrow("ID is invalid");
    expect(pc.createDataChannel).not.toHaveBeenCalled();
  });

  it("closes the negotiated channel when disposed", () => {
    const pc = new FakePeerConnection();
    const input = new RemoteInputChannel(() => undefined);
    input.attachNegotiated(pc as unknown as RTCPeerConnection, 9);
    input.dispose();
    expect(pc.channel.close).toHaveBeenCalledOnce();
    expect(input.current).toBeNull();
  });
});

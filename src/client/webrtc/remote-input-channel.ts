import { INPUT_CHANNEL } from "../remote-control/input-route";

// Both Browser reception and the App's local playback bridge announce the
// same optional capability. Their media owner still owns the PeerConnection.
export class RemoteInputChannel {
  private channel: RTCDataChannel | null = null;
  private connection: RTCPeerConnection | null = null;
  get current(): RTCDataChannel | null {
    return this.connection?.connectionState === "connected" && this.channel?.readyState === "open" ? this.channel : null;
  }

  constructor(private readonly changed: () => void) {}

  attach(connection: RTCPeerConnection): void {
    this.dispose();
    this.connection = connection;
    connection.addEventListener("connectionstatechange", () => {
      if (this.connection === connection) this.changed();
    });
    connection.addEventListener("datachannel", ({ channel }) => {
      if (this.connection !== connection || channel.label !== INPUT_CHANNEL || this.channel) {
        channel.close();
        return;
      }
      this.bindChannel(channel);
    });
  }

  // Cloudflare allocates negotiated DataChannels through its Connection API.
  // Those channels are created locally with the endpoint-specific ID and do
  // not emit the remote "datachannel" event.
  attachNegotiated(connection: RTCPeerConnection, id: number): RTCDataChannel {
    if (!Number.isInteger(id) || id < 0 || id > 65_534) {
      throw new Error("Negotiated remote-input DataChannel ID is invalid");
    }
    this.dispose();
    this.connection = connection;
    connection.addEventListener("connectionstatechange", () => {
      if (this.connection === connection) this.changed();
    });
    const channel = connection.createDataChannel(INPUT_CHANNEL, {
      negotiated: true,
      id,
      ordered: true,
    });
    this.bindChannel(channel);
    return channel;
  }

  private bindChannel(channel: RTCDataChannel): void {
    this.channel = channel;
    channel.addEventListener("open", () => {
      if (this.channel === channel) this.changed();
    });
    channel.addEventListener("close", () => {
      if (this.channel === channel) {
        this.channel = null;
        this.changed();
      }
    });
    if (channel.readyState === "open") this.changed();
  }

  dispose(): void {
    this.connection = null;
    const channel = this.channel;
    this.channel = null;
    channel?.close();
  }
}

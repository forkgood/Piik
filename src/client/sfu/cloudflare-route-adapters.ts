import type { CloudflareSfuServerMessage, ClientMessage } from "../../shared/protocol";
import type { QualityProfile } from "../media/quality";
import type { BrowserVideoCodec } from "../webrtc/video-codec";
import type { SfuConnectionConfig } from "./peer";
import {
  CloudflareSfuReplyBroker,
  createCloudflarePublisher,
  createCloudflareSubscriber,
} from "./cloudflare-session";

type Send = (message: ClientMessage) => boolean;
type ShareGeneration = () => string | null;

function signaling(
  config: SfuConnectionConfig,
  shareGeneration: ShareGeneration,
  send: Send,
  broker: CloudflareSfuReplyBroker,
) {
  const generation = shareGeneration();
  if (!generation) throw new Error("Cloudflare share generation is unavailable");
  return {
    shareGeneration: generation,
    connectionId: config.connectionId,
    send: (message: ClientMessage) => send(message),
    waitForReply: (kind: CloudflareSfuServerMessage["kind"], connectionId: string, signal: AbortSignal) =>
      broker.waitForReply(kind, connectionId, signal),
  };
}

function cloneLive(track: MediaStreamTrack | undefined): MediaStreamTrack | null {
  if (!track || track.readyState !== "live") return null;
  return track.clone();
}

export class CloudflareSfuPublisherTransport {
  private config: SfuConnectionConfig | null = null;
  private pc: RTCPeerConnection | null = null;
  private video: MediaStreamTrack | null = null;
  private audio: MediaStreamTrack | null = null;
  private lifetime = new AbortController();
  private paused = false;
  private closed = false;

  constructor(
    private readonly send: Send,
    private readonly broker: CloudflareSfuReplyBroker,
    private readonly shareGeneration: ShareGeneration,
    private readonly onDisconnected: () => void,
  ) {}

  async connect(config: SfuConnectionConfig): Promise<boolean> {
    if (this.config || this.closed) throw new Error("Cloudflare publisher cannot be connected twice");
    this.config = config;
    return true;
  }

  updateConfig(config: SfuConnectionConfig): void {
    if (this.config?.connectionId === config.connectionId &&
        this.config.publicationGeneration === config.publicationGeneration) this.config = config;
  }

  acceptSignal(_message: import("../../shared/protocol").SfuSignalMessage): Promise<void> { return Promise.resolve(); }

  async activate(stream: MediaStream | null, profile: QualityProfile, _codec?: BrowserVideoCodec): Promise<boolean> {
    if (!this.config || this.pc || this.closed || !stream) return false;
    const sourceVideo = stream.getVideoTracks()[0];
    this.video = cloneLive(sourceVideo);
    this.audio = cloneLive(stream.getAudioTracks()[0]);
    if (!this.video) return false;
    try {
      await this.video.applyConstraints({
        width: { ideal: profile.resolution.width },
        height: { ideal: profile.resolution.height },
        frameRate: { ideal: profile.maxFramerate },
      }).catch(() => undefined);
      const tracks = [{ track: this.video, trackName: "screen-video", stream: new MediaStream([this.video]) }];
      if (this.audio) tracks.push({ track: this.audio, trackName: "screen-audio", stream: new MediaStream([this.audio]) });
      const pc = new RTCPeerConnection({ iceServers: this.config.iceServers?.length ? this.config.iceServers : [{ urls: "stun:stun.cloudflare.com:3478" }] });
      pc.addEventListener("connectionstatechange", () => {
        if (this.closed) return;
        if (pc.connectionState === "failed") this.onDisconnected();
      });
      const transport = await createCloudflarePublisher(
        tracks,
        signaling(this.config, this.shareGeneration, this.send, this.broker),
        this.lifetime.signal,
        pc,
      );
      this.pc = transport.pc;
      this.applyPaused();
      await this.updateProfile(profile);
      return true;
    } catch {
      this.disposeTracks();
      this.pc?.close();
      this.pc = null;
      return false;
    }
  }

  async deactivate(): Promise<boolean> {
    this.setPaused(true);
    return true;
  }

  async replaceStream(stream: MediaStream | null): Promise<boolean> {
    const pc = this.pc;
    if (!pc || !stream) return false;
    const nextVideo = cloneLive(stream.getVideoTracks()[0]);
    const nextAudio = cloneLive(stream.getAudioTracks()[0]);
    if (!nextVideo) return false;
    const senders = pc.getSenders();
    const videoSender = senders.find(sender => sender.track?.kind === "video");
    const audioSender = senders.find(sender => sender.track?.kind === "audio");
    try {
      if (!videoSender) throw new Error("Cloudflare video sender is missing");
      await videoSender.replaceTrack(nextVideo);
      if (Boolean(audioSender) !== Boolean(nextAudio)) throw new Error("Cloudflare audio track topology changed");
      if (audioSender) await audioSender.replaceTrack(nextAudio);
      this.video?.stop();
      this.audio?.stop();
      this.video = nextVideo;
      this.audio = nextAudio;
      this.applyPaused();
      return true;
    } catch {
      nextVideo.stop();
      nextAudio?.stop();
      return false;
    }
  }

  async updateProfile(profile: QualityProfile): Promise<boolean> {
    const video = this.video;
    const sender = this.pc?.getSenders().find(item => item.track?.kind === "video");
    if (!video || !sender) return !this.pc;
    try {
      await video.applyConstraints({
        width: { ideal: profile.resolution.width },
        height: { ideal: profile.resolution.height },
        frameRate: { ideal: profile.maxFramerate },
      }).catch(() => undefined);
      const parameters = sender.getParameters();
      if (parameters.encodings?.length) {
        parameters.encodings[0].maxBitrate = profile.maxBitrate;
        parameters.encodings[0].maxFramerate = profile.maxFramerate;
        await sender.setParameters(parameters);
      }
      return true;
    } catch {
      return false;
    }
  }

  setPaused(paused: boolean): void {
    this.paused = paused;
    this.applyPaused();
  }

  getQualityWarning(): null { return null; }
  getFailureStage(): null { return null; }

  async disconnect(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    this.lifetime.abort(new Error("Cloudflare publisher disconnected"));
    this.pc?.close();
    this.pc = null;
    this.disposeTracks();
  }

  private applyPaused(): void {
    if (this.video) this.video.enabled = !this.paused;
    if (this.audio) this.audio.enabled = !this.paused;
  }

  private disposeTracks(): void {
    this.video?.stop();
    this.audio?.stop();
    this.video = null;
    this.audio = null;
  }
}

type CloudflareSubscriberEvents = {
  onPlaybackBufferStatus?: (status: import("../webrtc/receiver-buffer-target").PlaybackBufferStatus) => void;
  onInputChanged?: () => void;
  onStream: (stream: MediaStream | null) => void;
  onVideoAvailability: (available: boolean) => void;
  onStats: (metrics: import("../types").ConnectionMetrics) => void;
  onFirstDecodedFrame: () => boolean;
  onState: (state: "connected" | "reconnecting") => void;
  onDisconnected: (failure?: "VIDEO_REJECTED") => void;
};

export class CloudflareSfuSubscriberTransport {
  private config: SfuConnectionConfig | null = null;
  private pc: RTCPeerConnection | null = null;
  private stream = new MediaStream();
  private active = false;
  private closed = false;
  private lifetime = new AbortController();
  private proofTimer: ReturnType<typeof setInterval> | null = null;
  private playbackBufferStatus: import("../webrtc/receiver-buffer-target").PlaybackBufferStatus = null;

  constructor(
    private readonly events: CloudflareSubscriberEvents,
    private readonly send: Send,
    private readonly broker: CloudflareSfuReplyBroker,
    private readonly shareGeneration: ShareGeneration,
  ) {}

  setPlaybackBufferTarget(_target: number | null): import("../webrtc/receiver-buffer-target").PlaybackBufferStatus {
    // Cloudflare endpoint currently has no Piik receiver-buffer controller.
    this.playbackBufferStatus = null;
    this.events.onPlaybackBufferStatus?.(null);
    return null;
  }
  getPlaybackBufferStatus(): import("../webrtc/receiver-buffer-target").PlaybackBufferStatus { return this.playbackBufferStatus; }
  getRemoteInput(): RTCDataChannel | null { return null; }

  async connect(config: SfuConnectionConfig): Promise<boolean> {
    if (this.config || this.closed) throw new Error("Cloudflare subscriber cannot be connected twice");
    this.config = config;
    const pc = new RTCPeerConnection({ iceServers: [] });
    this.pc = pc;
    pc.addEventListener("connectionstatechange", () => {
      if (this.closed) return;
      if (pc.connectionState === "connected") this.events.onState("connected");
      else if (pc.connectionState === "disconnected") this.events.onState("reconnecting");
      else if (pc.connectionState === "failed") this.events.onDisconnected();
    });
    try {
      await createCloudflareSubscriber(
        signaling(config, this.shareGeneration, this.send, this.broker),
        this.lifetime.signal,
        ({ track }) => {
          if (this.closed || this.stream.getTracks().some(item => item.id === track.id)) return;
          this.stream.addTrack(track);
          track.addEventListener("ended", () => {
            if (this.stream.getTracks().every(item => item.readyState === "ended")) {
              this.events.onVideoAvailability(false);
            }
          }, { once: true });
          if (track.kind === "video") this.events.onVideoAvailability(true);
          if (this.active) this.events.onStream(this.stream);
        },
        pc,
      );
      if (pc.connectionState === "connected") this.events.onState("connected");
      return true;
    } catch {
      pc.close();
      this.pc = null;
      return false;
    }
  }

  updateConfig(config: SfuConnectionConfig): void {
    if (this.config?.connectionId === config.connectionId &&
        this.config.publicationGeneration === config.publicationGeneration) this.config = config;
  }
  isConnected(): boolean { return this.pc?.connectionState === "connected"; }
  acceptSignal(_message: import("../../shared/protocol").SfuSignalMessage): Promise<void> { return Promise.resolve(); }
  reconnect(): boolean { return false; }
  resync(): void { void this.disconnect(); }
  activate(): boolean {
    if (!this.pc || this.closed) return false;
    this.active = true;
    this.events.onStream(this.stream);
    this.startProof();
    return true;
  }
  deactivate(): boolean { void this.disconnect(); return true; }
  armDecodedFrameProof(_requireProgress = false): void { this.startProof(); }
  stopDecodedFrameProof(): void { this.stopProof(); }

  async disconnect(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    this.active = false;
    this.lifetime.abort(new Error("Cloudflare subscriber disconnected"));
    this.stopProof();
    this.pc?.close();
    this.pc = null;
    for (const track of this.stream.getTracks()) track.stop();
    this.stream = new MediaStream();
    this.events.onStream(null);
    this.events.onVideoAvailability(false);
  }

  private startProof(): void {
    if (this.proofTimer !== null || !this.pc || !this.active) return;
    this.proofTimer = setInterval(() => {
      const pc = this.pc;
      if (!pc || this.closed) return;
      void pc.getStats().then(report => {
        for (const stat of report.values()) {
          if (stat.type === "inbound-rtp" && stat.kind === "video" && Number(stat.framesDecoded ?? 0) > 0) {
            if (this.events.onFirstDecodedFrame()) this.stopProof();
            break;
          }
        }
      }).catch(() => undefined);
    }, 500);
  }

  private stopProof(): void {
    if (this.proofTimer !== null) clearInterval(this.proofTimer);
    this.proofTimer = null;
  }
}

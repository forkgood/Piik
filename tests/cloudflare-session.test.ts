import { describe, expect, it } from "vitest";
import { CloudflareSfuReplyBroker } from "../src/client/sfu/cloudflare-session";
import type { CloudflareSfuServerMessage } from "../src/shared/protocol";

const reply: CloudflareSfuServerMessage = {
  type: "cloudflare-sfu",
  kind: "published",
  shareGeneration: "share_generation_1",
  connectionId: "connection_123456",
  sessionId: "cloudflare_session_123",
  description: { type: "answer", sdp: "v=0" },
  tracks: [{ mid: "0", trackName: "screen-video" }],
};

describe("CloudflareSfuReplyBroker", () => {
  it("buffers a response that arrives before waitForReply", async () => {
    const broker = new CloudflareSfuReplyBroker();
    broker.accept(reply);
    await expect(broker.waitForReply("published", reply.connectionId, new AbortController().signal))
      .resolves.toEqual(reply);
    broker.close();
  });

  it("correlates responses by kind and connection ID", async () => {
    const broker = new CloudflareSfuReplyBroker();
    const controller = new AbortController();
    const waiting = broker.waitForReply("published", reply.connectionId, controller.signal);
    broker.accept({ ...reply, connectionId: "another_connection_123" });
    broker.accept(reply);
    await expect(waiting).resolves.toEqual(reply);
    broker.close();
  });

  it("rejects pending waits on abort", async () => {
    const broker = new CloudflareSfuReplyBroker();
    const controller = new AbortController();
    const waiting = broker.waitForReply("published", reply.connectionId, controller.signal);
    controller.abort(new Error("cancelled"));
    await expect(waiting).rejects.toThrow("cancelled");
    broker.close();
  });
});

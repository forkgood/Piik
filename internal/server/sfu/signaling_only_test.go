package sfu

import (
  "context"
  "testing"

  "github.com/pion/webrtc/v4"
)

func TestSignalingOnlyRuntimeDoesNotCreatePionMedia(t *testing.T) {
  runtime := NewSignalingOnlyRuntime()
  fence := ResourceFence{RoomID:"room_12345678", ShareGeneration:"share_12345678", PublicationGeneration:"publication_12345678"}
  if runtime.HasPublication(fence, "connection_12345678") {
    t.Fatal("signaling-only runtime must not claim a Pion publication")
  }
  if demand, err := runtime.PublicationDemand(fence, "connection_12345678"); err != nil || demand != 0 {
    t.Fatalf("PublicationDemand = %d, %v; want 0, nil", demand, err)
  }
  if err := runtime.PreparePublication(fence, "connection_12345678", PublicationMedia{}); err == nil {
    t.Fatal("Pion publication should be rejected in Cloudflare mode")
  }
  if err := runtime.ClosePublication(fence); err != nil {
    t.Fatalf("ClosePublication: %v", err)
  }
  if err := runtime.CloseSubscription(SubscriptionFence{}, "connection_12345678"); err != nil {
    t.Fatalf("CloseSubscription: %v", err)
  }
  if _, err := runtime.PrepareSubscriber(context.Background(), SubscriptionFence{}, "connection_12345678"); err == nil {
    t.Fatal("Pion subscriber should be rejected in Cloudflare mode")
  }
  if _, err := runtime.AcceptPublisherOffer(fence, "connection_12345678", webrtc.SessionDescription{}); err == nil {
    t.Fatal("Pion SDP should be rejected in Cloudflare mode")
  }
}

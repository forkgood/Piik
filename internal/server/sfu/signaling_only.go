package sfu

import (
  "context"
  "errors"

  "github.com/pion/webrtc/v4"
)

// SignalingOnlyRuntime provides SFU route admission for a Cloudflare-backed
// media provider without opening a Pion UDP media listener. Cloudflare SDP
// negotiation is handled by the authenticated Cloudflare signal path; Pion
// media operations are intentionally unavailable.
type SignalingOnlyRuntime struct{}

func NewSignalingOnlyRuntime() *SignalingOnlyRuntime { return &SignalingOnlyRuntime{} }

func (*SignalingOnlyRuntime) HasPublication(ResourceFence, string) bool { return false }
func (*SignalingOnlyRuntime) PreparePublication(ResourceFence, string, PublicationMedia) error {
  return errors.New("Pion publication is unavailable in Cloudflare signaling-only mode")
}
func (*SignalingOnlyRuntime) UpdatePublication(ResourceFence, string, PublicationMedia) error {
  return errors.New("Pion publication is unavailable in Cloudflare signaling-only mode")
}
func (*SignalingOnlyRuntime) PublicationDemand(ResourceFence, string) (int, error) { return 0, nil }
func (*SignalingOnlyRuntime) AcceptPublisherOffer(ResourceFence, string, webrtc.SessionDescription) (webrtc.SessionDescription, error) {
  return webrtc.SessionDescription{}, errors.New("Pion publisher offer is unavailable in Cloudflare signaling-only mode")
}
func (*SignalingOnlyRuntime) AddPublisherICE(ResourceFence, string, webrtc.ICECandidateInit) error {
  return errors.New("Pion ICE is unavailable in Cloudflare signaling-only mode")
}
func (*SignalingOnlyRuntime) PrepareSubscriber(context.Context, SubscriptionFence, string) (webrtc.SessionDescription, error) {
  return webrtc.SessionDescription{}, errors.New("Pion subscriber is unavailable in Cloudflare signaling-only mode")
}
func (*SignalingOnlyRuntime) ApplySubscriberAnswer(SubscriptionFence, string, webrtc.SessionDescription) error {
  return errors.New("Pion subscriber answer is unavailable in Cloudflare signaling-only mode")
}
func (*SignalingOnlyRuntime) AddSubscriberICE(SubscriptionFence, string, webrtc.ICECandidateInit) error {
  return errors.New("Pion ICE is unavailable in Cloudflare signaling-only mode")
}
func (*SignalingOnlyRuntime) RestartSubscriber(SubscriptionFence, string) (webrtc.SessionDescription, error) {
  return webrtc.SessionDescription{}, errors.New("Pion subscriber restart is unavailable in Cloudflare signaling-only mode")
}
func (*SignalingOnlyRuntime) CloseSubscription(SubscriptionFence, string) error { return nil }
func (*SignalingOnlyRuntime) ClosePublication(ResourceFence) error { return nil }

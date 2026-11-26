package cloudflare

import "testing"

func TestRegistryFencesPublisherAndSubscriberByShareGeneration(t *testing.T) {
	registry := NewRegistry()
	host := Owner{RoomID:"room-a", ShareGeneration:"share-1", PeerID:"host-peer", SignalingID:"host-session-1", Role:"host"}
	viewer := Owner{RoomID:"room-a", ShareGeneration:"share-1", PeerID:"viewer-peer", SignalingID:"viewer-session-1", Role:"viewer"}
	if err := registry.RegisterPublisher(host, "cf-publisher-1", []string{"0","1"}); err != nil { t.Fatal(err) }
	if err := registry.RegisterSubscriber(viewer, "cf-viewer-1", "cf-publisher-1", []string{"0"}); err != nil { t.Fatal(err) }
	if _, ok := registry.Publisher("room-a","share-1"); !ok { t.Fatal("expected publisher for active generation") }
	if _, ok := registry.Publisher("room-a","share-2"); ok { t.Fatal("stale generation resolved publisher") }
	if !registry.Owns("cf-viewer-1", viewer) { t.Fatal("expected exact viewer owner") }
	replaced := viewer
	replaced.SignalingID = "viewer-session-2"
	if registry.Owns("cf-viewer-1", replaced) { t.Fatal("replaced signaling session retained ownership") }
	if err := registry.RegisterSubscriber(Owner{RoomID:"room-a",ShareGeneration:"share-2",PeerID:"viewer-2",SignalingID:"viewer-session-2",Role:"viewer"}, "cf-viewer-2", "cf-publisher-1", []string{"0"}); err == nil {
		t.Fatal("cross-generation subscription was accepted")
	}
}

func TestRegistryRetireShareReturnsCopiesAndDoesNotTouchNextGeneration(t *testing.T) {
	registry := NewRegistry()
	host1 := Owner{RoomID:"room-a",ShareGeneration:"share-1",PeerID:"host",SignalingID:"host-1",Role:"host"}
	viewer1 := Owner{RoomID:"room-a",ShareGeneration:"share-1",PeerID:"viewer",SignalingID:"viewer-1",Role:"viewer"}
	host2 := Owner{RoomID:"room-a",ShareGeneration:"share-2",PeerID:"host",SignalingID:"host-2",Role:"host"}
	if err := registry.RegisterPublisher(host1,"cf-host-1",[]string{"0"}); err != nil { t.Fatal(err) }
	if err := registry.RegisterSubscriber(viewer1,"cf-viewer-1","cf-host-1",[]string{"1"}); err != nil { t.Fatal(err) }
	if err := registry.RegisterPublisher(host2,"cf-host-2",[]string{"0"}); err != nil { t.Fatal(err) }
	retired := registry.RetireShare("room-a","share-1")
	if len(retired) != 2 { t.Fatalf("retired %d resources, want 2",len(retired)) }
	retired[0].MIDs[0] = "mutated"
	if _, ok := registry.Publisher("room-a","share-1"); ok { t.Fatal("retired publisher still registered") }
	if got, ok := registry.Publisher("room-a","share-2"); !ok || got.SessionID != "cf-host-2" { t.Fatal("new generation was retired") }
}

func TestRegistryRetirePeerRequiresExactSignalingSession(t *testing.T) {
	registry := NewRegistry()
	host := Owner{RoomID:"room-a",ShareGeneration:"share-1",PeerID:"host",SignalingID:"host-session",Role:"host"}
	if err := registry.RegisterPublisher(host,"cf-host",[]string{"0"}); err != nil { t.Fatal(err) }
	if retired := registry.RetirePeer("room-a","share-1","host","old-session"); len(retired) != 0 { t.Fatal("stale disconnect retired current owner") }
	if retired := registry.RetirePeer("room-a","share-1","host","host-session"); len(retired) != 1 { t.Fatal("exact owner did not retire") }
}


func TestRegistryTracksChannelIDsAsImmutableOwnedResources(t *testing.T) {
	registry := NewRegistry()
	host := Owner{RoomID:"room-a", ShareGeneration:"share-1", PeerID:"host", SignalingID:"host-session", Role:"host"}
	if err := registry.RegisterPublisherResources(host, "cf-host", []string{"0"}, []int{3, 5}); err != nil {
		t.Fatal(err)
	}
	resource, ok := registry.Publisher("room-a", "share-1")
	if !ok || len(resource.ChannelIDs) != 2 || resource.ChannelIDs[0] != 3 {
		t.Fatalf("unexpected registered channel resources: %#v", resource)
	}
	resource.ChannelIDs[0] = 99
	retired := registry.RetireShare("room-a", "share-1")
	if len(retired) != 1 || retired[0].ChannelIDs[0] != 3 {
		t.Fatalf("registry did not preserve immutable channel ID snapshot: %#v", retired)
	}
}

func TestRegistryRejectsInvalidChannelResourceIDs(t *testing.T) {
	registry := NewRegistry()
	host := Owner{RoomID:"room-a", ShareGeneration:"share-1", PeerID:"host", SignalingID:"host-session", Role:"host"}
	for _, ids := range [][]int{{-1}, {65_535}, {2, 2}} {
		if err := registry.RegisterPublisherResources(host, "cf-host", []string{"0"}, ids); err == nil {
			t.Fatalf("invalid channel IDs accepted: %#v", ids)
		}
	}
}

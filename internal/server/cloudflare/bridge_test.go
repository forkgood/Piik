package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBridgePublishUsesGatheredOfferAndReturnsAnswer(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/v1/apps/test/sessions/new":
			_ = json.NewEncoder(w).Encode(map[string]string{"sessionId":"publisher-session"})
		case "/v1/apps/test/sessions/publisher-session/tracks/new":
			var body struct {
				SessionDescription SessionDescription `json:"sessionDescription"`
				Tracks []Track `json:"tracks"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Fatal(err) }
			if body.SessionDescription.Type != "offer" || !strings.Contains(body.SessionDescription.SDP, "a=candidate:") {
				t.Fatalf("expected fully gathered offer, got %#v", body.SessionDescription)
			}
			if len(body.Tracks) != 1 || body.Tracks[0].Location != "local" || body.Tracks[0].TrackName != "screen-video" {
				t.Fatalf("unexpected local tracks: %#v", body.Tracks)
			}
			_ = json.NewEncoder(w).Encode(TracksResponse{
				SessionDescription: &SessionDescription{Type:"answer", SDP:"v=0\r\n"},
				Tracks: []TrackResult{{Track:Track{Location:"local", MID:"0", TrackName:"screen-video"}}},
			})
		default:
			http.NotFound(w,r)
		}
	}))
	defer server.Close()

	client, err := New("test", "secret", WithBaseURL(server.URL+"/v1"), WithHTTPClient(server.Client()))
	if err != nil { t.Fatal(err) }
	bridge, err := NewBridge(client)
	if err != nil { t.Fatal(err) }
	result, err := bridge.Publish(context.Background(), SessionDescription{Type:"offer", SDP:"v=0\r\na=candidate:1 1 udp 1 192.0.2.1 5000 typ host\r\n"}, []Track{
		{Location:"local", MID:"0", TrackName:"screen-video"},
	})
	if err != nil { t.Fatal(err) }
	if result.SessionID != "publisher-session" || result.Answer.Type != "answer" || len(result.Tracks) != 1 {
		t.Fatalf("unexpected publication: %#v", result)
	}
	if len(calls) != 2 || calls[0] != "POST /v1/apps/test/sessions/new" || calls[1] != "POST /v1/apps/test/sessions/publisher-session/tracks/new" {
		t.Fatalf("unexpected API sequence: %#v", calls)
	}
}

func TestBridgeSubscribeAndCompleteAnswer(t *testing.T) {
	var renegotiated bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/apps/test/sessions/new":
			_ = json.NewEncoder(w).Encode(map[string]string{"sessionId":"viewer-session"})
		case "/v1/apps/test/sessions/viewer-session/tracks/new":
			var body struct { Tracks []Track `json:"tracks"` }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Fatal(err) }
			if len(body.Tracks) != 1 || body.Tracks[0].Location != "remote" ||
				body.Tracks[0].SessionID != "publisher-session" || body.Tracks[0].TrackName != "screen-video" {
				t.Fatalf("unexpected remote tracks: %#v", body.Tracks)
			}
			_ = json.NewEncoder(w).Encode(TracksResponse{
				SessionDescription: &SessionDescription{Type:"offer", SDP:"v=0\r\n"},
				Tracks: []TrackResult{{Track:Track{Location:"remote", MID:"0", SessionID:"publisher-session", TrackName:"screen-video"}}},
			})
		case "/v1/apps/test/sessions/viewer-session/renegotiate":
			var body struct { SessionDescription SessionDescription `json:"sessionDescription"` }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Fatal(err) }
			if body.SessionDescription.Type != "answer" || body.SessionDescription.SDP == "" {
				t.Fatalf("unexpected answer: %#v", body.SessionDescription)
			}
			renegotiated = true
			_ = json.NewEncoder(w).Encode(map[string]any{})
		default:
			http.NotFound(w,r)
		}
	}))
	defer server.Close()

	client, err := New("test", "secret", WithBaseURL(server.URL+"/v1"), WithHTTPClient(server.Client()))
	if err != nil { t.Fatal(err) }
	bridge, err := NewBridge(client)
	if err != nil { t.Fatal(err) }
	subscription, err := bridge.Subscribe(context.Background(), "publisher-session", []string{"screen-video"})
	if err != nil { t.Fatal(err) }
	if subscription.SessionID != "viewer-session" || subscription.Offer.Type != "offer" {
		t.Fatalf("unexpected subscription: %#v", subscription)
	}
	if err := bridge.CompleteSubscription(context.Background(), subscription.SessionID, SessionDescription{Type:"answer", SDP:"v=0\r\n"}); err != nil {
		t.Fatal(err)
	}
	if !renegotiated { t.Fatal("expected subscription answer to be renegotiated") }
}


func TestBridgeCloseTracksUsesForceAndExactMIDs(t *testing.T) {
	var closed []TrackReference
	var force bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/apps/test/sessions/session-1/tracks/close" || r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Tracks []TrackReference `json:"tracks"`
			Force bool `json:"force"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Fatal(err) }
		closed, force = body.Tracks, body.Force
		_ = json.NewEncoder(w).Encode(TracksResponse{})
	}))
	defer server.Close()
	client, err := New("test", "secret", WithBaseURL(server.URL+"/v1"), WithHTTPClient(server.Client()))
	if err != nil { t.Fatal(err) }
	bridge, err := NewBridge(client)
	if err != nil { t.Fatal(err) }
	if err := bridge.CloseTracks(context.Background(), "session-1", []string{"0","2"}); err != nil { t.Fatal(err) }
	if !force || len(closed) != 2 || closed[0].MID != "0" || closed[1].MID != "2" {
		t.Fatalf("unexpected close request: force=%v tracks=%#v", force, closed)
	}
}

func TestValidateTrackResultsRejectsUnexpectedOrDuplicateMID(t *testing.T) {
	requested := []Track{{Location:"local", MID:"0", TrackName:"screen-video"}}
	if err := validateTrackResults(requested, []TrackResult{{Track:Track{Location:"local", MID:"0", TrackName:"screen-audio"}}}); err == nil {
		t.Fatal("unexpected track name was accepted")
	}
	if err := validateTrackResults([]Track{
		{Location:"local", MID:"0", TrackName:"screen-video"},
		{Location:"local", MID:"1", TrackName:"screen-audio"},
	}, []TrackResult{
		{Track:Track{Location:"local", MID:"0", TrackName:"screen-video"}},
		{Track:Track{Location:"local", MID:"0", TrackName:"screen-audio"}},
	}); err == nil {
		t.Fatal("duplicate MID was accepted")
	}
}


func TestBridgeNegotiatedDataChannelLifecycle(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/v1/apps/test/sessions/session-1/datachannels/establish":
			_ = json.NewEncoder(w).Encode(DataChannelsResponse{
				SessionDescription: &SessionDescription{Type:"offer", SDP:"reserved-offer"},
				DataChannel: &DataChannelResult{DataChannel:DataChannel{Location:"remote", DataChannelName:"server-events"}, ID:0},
			})
		case "/v1/apps/test/sessions/session-1/datachannels/new":
			var body struct { DataChannels []DataChannel `json:"dataChannels"` }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Fatal(err) }
			if len(body.DataChannels) != 1 || body.DataChannels[0].DataChannelName != "remote-input" {
				t.Fatalf("unexpected new data channels: %#v", body.DataChannels)
			}
			_ = json.NewEncoder(w).Encode(DataChannelsResponse{
				DataChannels: []DataChannelResult{{DataChannel:DataChannel{Location:"local", DataChannelName:"remote-input"}, ID:2}},
			})
		case "/v1/apps/test/sessions/session-1/datachannels/update":
			var body struct { DataChannels []DataChannel `json:"dataChannels"` }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Fatal(err) }
			if len(body.DataChannels) != 1 || body.DataChannels[0].CanReply == nil || !*body.DataChannels[0].CanReply {
				t.Fatalf("expected explicit canReply grant: %#v", body.DataChannels)
			}
			_ = json.NewEncoder(w).Encode(DataChannelsResponse{})
		case "/v1/apps/test/sessions/session-1/datachannels/close":
			var body struct { DataChannels []DataChannelReference `json:"dataChannels"` }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Fatal(err) }
			if len(body.DataChannels) != 1 || body.DataChannels[0].ID != 2 {
				t.Fatalf("unexpected close IDs: %#v", body.DataChannels)
			}
			_ = json.NewEncoder(w).Encode(DataChannelsResponse{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New("test", "secret", WithBaseURL(server.URL+"/v1"), WithHTTPClient(server.Client()))
	if err != nil { t.Fatal(err) }
	bridge, err := NewBridge(client)
	if err != nil { t.Fatal(err) }

	reserved, offer, err := bridge.EstablishDataChannel(context.Background(), "session-1", "server-events")
	if err != nil { t.Fatal(err) }
	if reserved.ID != 0 || offer == nil || offer.Type != "offer" {
		t.Fatalf("unexpected reserved allocation: %#v %#v", reserved, offer)
	}
	allocations, _, err := bridge.AllocateDataChannels(context.Background(), "session-1", []DataChannel{
		{Location:"local", DataChannelName:"remote-input"},
	})
	if err != nil { t.Fatal(err) }
	if len(allocations) != 1 || allocations[0].ID != 2 {
		t.Fatalf("unexpected allocations: %#v", allocations)
	}
	if err := bridge.SetRemoteDataChannelReply(context.Background(), "session-1", "publisher-session", "remote-input", true); err != nil {
		t.Fatal(err)
	}
	if err := bridge.CloseDataChannels(context.Background(), "session-1", []int{2}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 {
		t.Fatalf("unexpected API calls: %#v", calls)
	}
}

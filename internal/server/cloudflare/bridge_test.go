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
				Tracks: []TrackResult{{Track:Track{Location:"remote", SessionID:"publisher-session", TrackName:"screen-video"}}},
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

package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewSessionKeepsSecretServerSideAndUsesBearerAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/apps/app-123/sessions/new" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer top-secret" {
			t.Fatalf("authorization = %q", got)
		}
		if r.Header.Get("Content-Type") != "" {
			t.Fatalf("unexpected content type for bodyless request")
		}
		_, _ = w.Write([]byte(`{"sessionId":"session-1"}`))
	}))
	defer server.Close()

	client, err := New("app-123", "top-secret", WithBaseURL(server.URL+"/v1"), WithHTTPClient(server.Client()))
	if err != nil { t.Fatal(err) }
	got, err := client.NewSession(context.Background())
	if err != nil { t.Fatal(err) }
	if got.SessionID != "session-1" { t.Fatalf("sessionId = %q", got.SessionID) }
}

func TestNewTracksRejectsMixedLocationsAndChecksResourceErrors(t *testing.T) {
	client, err := New("app", "secret")
	if err != nil { t.Fatal(err) }
	_, err = client.NewTracks(context.Background(), "s", nil, []Track{
		{Location:"local", MID:"0", TrackName:"screen"},
		{Location:"remote", SessionID:"publisher", TrackName:"screen"},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot mix") { t.Fatalf("unexpected error: %v", err) }

	response := TracksResponse{Tracks: []TrackResult{{ErrorCode:"invalid_params", ErrorDescription:"bad mid"}}}
	if got := response.ResourceErrors(); len(got) != 1 || got[0] != "invalid_params: bad mid" {
		t.Fatalf("resource errors = %#v", got)
	}
}

func TestRenegotiateSendsAnswer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1/apps/app/sessions/session/renegotiate" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var body struct { SessionDescription SessionDescription `json:"sessionDescription"` }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil { t.Fatal(err) }
		if body.SessionDescription.Type != "answer" || body.SessionDescription.SDP != "v=0" {
			t.Fatalf("unexpected body: %#v", body)
		}
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	client, err := New("app", "secret", WithBaseURL(server.URL+"/v1"), WithHTTPClient(server.Client()))
	if err != nil { t.Fatal(err) }
	if _, err := client.Renegotiate(context.Background(), "session", SessionDescription{Type:"answer", SDP:"v=0"}); err != nil {
		t.Fatal(err)
	}
}

func TestAPIErrorPreservesPublicErrorCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"errorCode":"session_error","errorDescription":"not ready"}`))
	}))
	defer server.Close()
	client, err := New("app", "secret", WithBaseURL(server.URL+"/v1"), WithHTTPClient(server.Client()))
	if err != nil { t.Fatal(err) }
	_, err = client.NewSession(context.Background())
	apiErr, ok := err.(*APIError)
	if !ok || apiErr.Code != "session_error" || apiErr.StatusCode != http.StatusConflict {
		t.Fatalf("unexpected API error: %#v", err)
	}
}

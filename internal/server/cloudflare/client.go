// Package cloudflare implements the server-side Cloudflare Realtime Connection API.
// It deliberately contains no room policy: callers must authorize every operation
// against Piik's room/session/publication fences before calling this package.
package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net"
	"net/url"
	"strings"
	"time"
)

const DefaultBaseURL = "https://rtc.live.cloudflare.com/v1"

type Client struct {
	appID   string
	secret  string
	baseURL string
	http    *http.Client
}

type Option func(*Client)

func WithBaseURL(value string) Option { return func(c *Client) { c.baseURL = strings.TrimRight(value, "/") } }
func WithHTTPClient(value *http.Client) Option { return func(c *Client) { if value != nil { c.http = value } } }

func New(appID, secret string, options ...Option) (*Client, error) {
	if strings.TrimSpace(appID) == "" || strings.TrimSpace(secret) == "" {
		return nil, errors.New("Cloudflare Realtime App ID and App Secret are required")
	}
	c := &Client{appID: strings.TrimSpace(appID), secret: secret, baseURL: DefaultBaseURL,
		http: &http.Client{Timeout: 20 * time.Second}}
	for _, option := range options { option(c) }
	u, err := url.Parse(c.baseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "https" && !(u.Scheme == "http" && isLoopbackHost(u.Hostname()))) {
		return nil, errors.New("Cloudflare Realtime API URL must use HTTPS (HTTP is allowed only for loopback tests)")
	}
	return c, nil
}

type APIError struct {
	StatusCode int
	Code       string `json:"errorCode"`
	Description string `json:"errorDescription"`
	Body       string
}
func (e *APIError) Error() string {
	if e.Code != "" { return fmt.Sprintf("Cloudflare Realtime API: HTTP %d %s: %s", e.StatusCode, e.Code, e.Description) }
	return fmt.Sprintf("Cloudflare Realtime API: HTTP %d: %s", e.StatusCode, e.Body)
}

type SessionDescription struct { Type string `json:"type"`; SDP string `json:"sdp"` }
type SessionResponse struct { SessionID string `json:"sessionId"` }
type Track struct {
	Location string `json:"location"`
	MID string `json:"mid,omitempty"`
	TrackName string `json:"trackName,omitempty"`
	SessionID string `json:"sessionId,omitempty"`
}
type TrackResult struct {
	Track Track `json:"track"`
	ErrorCode string `json:"errorCode,omitempty"`
	ErrorDescription string `json:"errorDescription,omitempty"`
}
type TracksResponse struct {
	SessionDescription *SessionDescription `json:"sessionDescription,omitempty"`
	RequiresImmediateRenegotiation bool `json:"requiresImmediateRenegotiation,omitempty"`
	Tracks []TrackResult `json:"tracks,omitempty"`
}
type RenegotiateResponse struct { SessionDescription *SessionDescription `json:"sessionDescription,omitempty"`; RequiresImmediateRenegotiation bool `json:"requiresImmediateRenegotiation,omitempty"` }
type SessionInfo struct {
	SessionID string `json:"sessionId,omitempty"`
	Tracks []struct { Location string `json:"location"`; MID string `json:"mid"`; TrackName string `json:"trackName"`; Status string `json:"status"` } `json:"tracks"`
}
type TrackReference struct { MID string `json:"mid"` }
type DataChannel struct {
	Location string `json:"location"`
	DataChannelName string `json:"dataChannelName"`
	SessionID string `json:"sessionId,omitempty"`
	CanReply bool `json:"canReply,omitempty"`
}
type DataChannelResult struct { DataChannel DataChannel `json:"dataChannel"`; ID int `json:"id,omitempty"`; ErrorCode string `json:"errorCode,omitempty"`; ErrorDescription string `json:"errorDescription,omitempty"` }
type DataChannelsResponse struct { SessionDescription *SessionDescription `json:"sessionDescription,omitempty"`; RequiresImmediateRenegotiation bool `json:"requiresImmediateRenegotiation,omitempty"`; DataChannels []DataChannelResult `json:"dataChannels,omitempty"` }

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") { return true }
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (c *Client) endpoint(path string) string { return c.baseURL + "/apps/" + url.PathEscape(c.appID) + path }
func (c *Client) do(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil { raw, err := json.Marshal(input); if err != nil { return err }; body = bytes.NewReader(raw) }
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint(path), body)
	if err != nil { return err }
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Accept", "application/json")
	if input != nil { req.Header.Set("Content-Type", "application/json") }
	resp, err := c.http.Do(req)
	if err != nil { return fmt.Errorf("Cloudflare Realtime request outcome may be unknown: %w", err) }
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil { return fmt.Errorf("read Cloudflare Realtime response: %w", err) }
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr APIError
		apiErr.StatusCode = resp.StatusCode
		apiErr.Body = string(raw)
		_ = json.Unmarshal(raw, &apiErr)
		return &apiErr
	}
	if output != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, output); err != nil { return fmt.Errorf("decode Cloudflare Realtime response: %w", err) }
	}
	return nil
}

// NewSession creates one SFU session for one endpoint PeerConnection.
func (c *Client) NewSession(ctx context.Context) (SessionResponse, error) {
	var out SessionResponse
	err := c.do(ctx, http.MethodPost, "/sessions/new", nil, &out)
	if err == nil && out.SessionID == "" { err = errors.New("Cloudflare Realtime returned an empty sessionId") }
	return out, err
}

// NewTracks publishes local tracks or subscribes to remote tracks. A request
// must not mix local and remote entries; each result must be checked separately.
func (c *Client) NewTracks(ctx context.Context, sessionID string, description *SessionDescription, tracks []Track) (TracksResponse, error) {
	if sessionID == "" || len(tracks) == 0 { return TracksResponse{}, errors.New("sessionId and at least one track are required") }
	location := tracks[0].Location
	if location != "local" && location != "remote" { return TracksResponse{}, errors.New("track location must be local or remote") }
	for _, track := range tracks {
		if track.Location != location { return TracksResponse{}, errors.New("a tracks request cannot mix local and remote tracks") }
		if location == "local" && (track.MID == "" || track.TrackName == "") { return TracksResponse{}, errors.New("local tracks require mid and trackName") }
		if location == "remote" && (track.SessionID == "" || track.TrackName == "") { return TracksResponse{}, errors.New("remote tracks require publisher sessionId and trackName") }
	}
	input := struct { SessionDescription *SessionDescription `json:"sessionDescription,omitempty"`; Tracks []Track `json:"tracks"` }{description, tracks}
	var out TracksResponse
	err := c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/tracks/new", input, &out)
	return out, err
}

func (c *Client) Renegotiate(ctx context.Context, sessionID string, answer SessionDescription) (RenegotiateResponse, error) {
	if sessionID == "" || answer.Type != "answer" || answer.SDP == "" { return RenegotiateResponse{}, errors.New("sessionId and a non-empty SDP answer are required") }
	var out RenegotiateResponse
	err := c.do(ctx, http.MethodPut, "/sessions/"+url.PathEscape(sessionID)+"/renegotiate", struct { SessionDescription SessionDescription `json:"sessionDescription"` }{answer}, &out)
	return out, err
}

func (c *Client) GetSession(ctx context.Context, sessionID string) (SessionInfo, error) {
	var out SessionInfo
	if sessionID == "" { return out, errors.New("sessionId is required") }
	err := c.do(ctx, http.MethodGet, "/sessions/"+url.PathEscape(sessionID), nil, &out)
	return out, err
}

func (c *Client) CloseTracks(ctx context.Context, sessionID string, mids []string) error {
	if sessionID == "" || len(mids) == 0 { return errors.New("sessionId and at least one mid are required") }
	tracks := make([]TrackReference, 0, len(mids))
	for _, mid := range mids {
		if mid == "" { return errors.New("track mid must not be empty") }
		tracks = append(tracks, TrackReference{MID: mid})
	}
	return c.do(ctx, http.MethodPut, "/sessions/"+url.PathEscape(sessionID)+"/tracks/close", struct {
		Tracks []TrackReference `json:"tracks"`
	}{tracks}, nil)
}

func (c *Client) EstablishDataChannel(ctx context.Context, sessionID, name string) (DataChannelsResponse, error) {
	if sessionID == "" || name == "" { return DataChannelsResponse{}, errors.New("sessionId and dataChannelName are required") }
	input := struct { DataChannel DataChannel `json:"dataChannel"` }{DataChannel{Location:"remote", DataChannelName:name}}
	var out DataChannelsResponse
	err := c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/datachannels/establish", input, &out)
	return out, err
}

func (c *Client) NewDataChannels(ctx context.Context, sessionID string, channels []DataChannel) (DataChannelsResponse, error) {
	if sessionID == "" || len(channels) == 0 { return DataChannelsResponse{}, errors.New("sessionId and at least one data channel are required") }
	location := channels[0].Location
	if location != "local" && location != "remote" { return DataChannelsResponse{}, errors.New("data channel location must be local or remote") }
	for _, channel := range channels {
		if channel.Location != location || channel.DataChannelName == "" { return DataChannelsResponse{}, errors.New("data channel entries must share a location and have a name") }
		if location == "remote" && channel.SessionID == "" { return DataChannelsResponse{}, errors.New("remote data channels require publisher sessionId") }
	}
	var out DataChannelsResponse
	err := c.do(ctx, http.MethodPost, "/sessions/"+url.PathEscape(sessionID)+"/datachannels/new", struct { DataChannels []DataChannel `json:"dataChannels"` }{channels}, &out)
	return out, err
}

// ResourceErrors returns every per-resource failure; HTTP 200 is not sufficient
// evidence that a batch operation succeeded.
func (r TracksResponse) ResourceErrors() []string {
	var out []string
	for _, item := range r.Tracks { if item.ErrorCode != "" { out = append(out, item.ErrorCode+": "+item.ErrorDescription) } }
	return out
}
func (r DataChannelsResponse) ResourceErrors() []string {
	var out []string
	for _, item := range r.DataChannels { if item.ErrorCode != "" { out = append(out, item.ErrorCode+": "+item.ErrorDescription) } }
	return out
}

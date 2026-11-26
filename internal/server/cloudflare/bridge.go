package cloudflare

import (
	"context"
	"errors"
	"fmt"
)

// Bridge coordinates the Cloudflare Connection API's two SDP directions.
// It deliberately has no Piik room policy; callers must authorize and fence
// every operation before invoking it.
type Bridge struct {
	client *Client
	queue  *SessionQueue
}

func NewBridge(client *Client) (*Bridge, error) {
	if client == nil {
		return nil, errors.New("Cloudflare Realtime client is required")
	}
	return &Bridge{client: client, queue: NewSessionQueue()}, nil
}

// Publication is the Cloudflare-side identity and answer for a Host publisher.
type Publication struct {
	SessionID string
	Tracks    []TrackResult
	Answer    SessionDescription
}

// Subscription is a viewer's Cloudflare session and the offer it must apply.
type Subscription struct {
	SessionID string
	Tracks    []TrackResult
	Offer     SessionDescription
}

// Publish creates a publisher session and sends the browser's fully gathered
// SDP offer together with its local track MIDs and stable Piik track names.
func (b *Bridge) Publish(ctx context.Context, offer SessionDescription, tracks []Track) (Publication, error) {
	if offer.Type != "offer" || offer.SDP == "" {
		return Publication{}, errors.New("publisher requires a non-empty SDP offer")
	}
	if len(tracks) == 0 {
		return Publication{}, errors.New("publisher requires at least one local track")
	}
	for _, track := range tracks {
		if track.Location != "local" {
			return Publication{}, errors.New("publisher tracks must use local location")
		}
	}
	session, err := b.client.NewSession(ctx)
	if err != nil {
		return Publication{}, fmt.Errorf("create Cloudflare publisher session: %w", err)
	}

	var response TracksResponse
	err = b.queue.Do(ctx, session.SessionID, func(operationCtx context.Context) error {
		var operationErr error
		response, operationErr = b.client.NewTracks(operationCtx, session.SessionID, &offer, tracks)
		if operationErr != nil {
			return operationErr
		}
		if failures := response.ResourceErrors(); len(failures) != 0 {
			return fmt.Errorf("Cloudflare publisher track allocation failed: %v", failures)
		}
		if response.SessionDescription == nil ||
			response.SessionDescription.Type != "answer" ||
			response.SessionDescription.SDP == "" {
			return errors.New("Cloudflare publisher response did not contain an SDP answer")
		}
		if err := validateTrackResults(tracks, response.Tracks); err != nil {
			return fmt.Errorf("validate Cloudflare publisher tracks: %w", err)
		}
		return nil
	})
	if err != nil {
		return Publication{SessionID: session.SessionID}, fmt.Errorf("publish Cloudflare tracks: %w", err)
	}
	return Publication{SessionID: session.SessionID, Tracks: response.Tracks, Answer: *response.SessionDescription}, nil
}

// Subscribe creates a viewer session and asks Cloudflare to receive the
// publisher's named remote tracks. The returned SDP offer must be applied by
// the viewer browser before its answer is submitted with CompleteSubscription.
func (b *Bridge) Subscribe(ctx context.Context, publisherSessionID string, trackNames []string) (Subscription, error) {
	if publisherSessionID == "" || len(trackNames) == 0 {
		return Subscription{}, errors.New("publisher session ID and at least one track name are required")
	}
	tracks := make([]Track, 0, len(trackNames))
	for _, name := range trackNames {
		if name == "" {
			return Subscription{}, errors.New("remote track name must not be empty")
		}
		tracks = append(tracks, Track{Location: "remote", SessionID: publisherSessionID, TrackName: name})
	}
	session, err := b.client.NewSession(ctx)
	if err != nil {
		return Subscription{}, fmt.Errorf("create Cloudflare viewer session: %w", err)
	}

	var response TracksResponse
	err = b.queue.Do(ctx, session.SessionID, func(operationCtx context.Context) error {
		var operationErr error
		response, operationErr = b.client.NewTracks(operationCtx, session.SessionID, nil, tracks)
		if operationErr != nil {
			return operationErr
		}
		if failures := response.ResourceErrors(); len(failures) != 0 {
			return fmt.Errorf("Cloudflare remote track subscription failed: %v", failures)
		}
		if response.SessionDescription == nil ||
			response.SessionDescription.Type != "offer" ||
			response.SessionDescription.SDP == "" {
			return errors.New("Cloudflare subscription response did not contain an SDP offer")
		}
		if err := validateTrackResults(tracks, response.Tracks); err != nil {
			return fmt.Errorf("validate Cloudflare subscription tracks: %w", err)
		}
		return nil
	})
	if err != nil {
		return Subscription{SessionID: session.SessionID}, fmt.Errorf("subscribe Cloudflare tracks: %w", err)
	}
	return Subscription{SessionID: session.SessionID, Tracks: response.Tracks, Offer: *response.SessionDescription}, nil
}

// CompleteSubscription submits the viewer's gathered SDP answer. This call
// is serialized with every other mutation for the same Cloudflare session.
func (b *Bridge) CompleteSubscription(ctx context.Context, sessionID string, answer SessionDescription) error {
	return b.queue.Do(ctx, sessionID, func(operationCtx context.Context) error {
		_, err := b.client.Renegotiate(operationCtx, sessionID, answer)
		return err
	})
}


// CloseTracks retires the exact Cloudflare MIDs recorded for a Piik-owned
// session. Force closure avoids creating a second SDP exchange while the
// browser may already be gone. The caller must fence the lifecycle event
// before removing its ownership record; cleanup itself is best-effort.
func (b *Bridge) CloseTracks(ctx context.Context, sessionID string, mids []string) error {
	if sessionID == "" || len(mids) == 0 {
		return nil
	}
	return b.queue.Do(ctx, sessionID, func(operationCtx context.Context) error {
		response, err := b.client.CloseTracks(operationCtx, sessionID, mids, true, nil)
		if err != nil {
			return err
		}
		if failures := response.ResourceErrors(); len(failures) != 0 {
			return fmt.Errorf("Cloudflare track cleanup failed: %v", failures)
		}
		return nil
	})
}

// validateTrackResults verifies that Cloudflare returned one unique MID for
// every requested stable track name. Partial or unexpected results must not
// be committed into Piik's resource registry.
func validateTrackResults(requested []Track, returned []TrackResult) error {
	if len(requested) != len(returned) {
		return fmt.Errorf("Cloudflare returned %d track results for %d requested tracks", len(returned), len(requested))
	}
	seen := make(map[string]struct{}, len(returned))
	for _, result := range returned {
		if result.MID == "" || result.TrackName == "" {
			return errors.New("Cloudflare track result is missing MID or trackName")
		}
		if _, exists := seen[result.MID]; exists {
			return fmt.Errorf("Cloudflare returned duplicate track MID %q", result.MID)
		}
		seen[result.MID] = struct{}{}
		matched := false
		for _, input := range requested {
			if input.TrackName == result.TrackName && input.Location == result.Location &&
				(input.Location != "remote" || input.SessionID == result.SessionID) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("Cloudflare returned unexpected track %q", result.TrackName)
		}
	}
	return nil
}

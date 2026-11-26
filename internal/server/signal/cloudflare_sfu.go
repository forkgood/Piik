package signal

import (
	"context"
	"time"

	cloudflarerealtime "github.com/TNTcraftHIM/Piik/internal/server/cloudflare"
	"github.com/TNTcraftHIM/Piik/internal/server/protocol")

// handleCloudflareSFU is called with Server.mu held. It validates authority
// synchronously, then delegates all Cloudflare network work to a goroutine.
func (s *Server) handleCloudflareSFU(sess *session, authenticated *authenticatedSession, message protocol.CloudflareSFUMessage) {
	if s.cloudflareBridge == nil || s.cloudflareRegistry == nil {
		s.sendError(sess, "FORBIDDEN", "Cloudflare SFU is not configured")
		return
	}
	if !s.sessions.Has(sess) || sess.authenticated != authenticated || !s.isCurrentSession(sess) {
		return
	}
	generation := s.shares[authenticated.roomID].generation
	if generation == "" || generation != message.ShareGeneration {
		s.sendError(sess, "FORBIDDEN", "Cloudflare SFU publication is stale")
		return
	}
	role := "viewer"
	if authenticated.role == protocol.RoleHost {
		role = "host"
		if authenticated.shareGeneration != generation {
			s.sendError(sess, "FORBIDDEN", "Host publication generation is stale")
			return
		}
	}
	if message.Kind == "publish" && role != "host" || message.Kind != "publish" && role != "viewer" {
		s.sendError(sess, "FORBIDDEN", "Cloudflare SFU operation is not permitted for this role")
		return
	}
	owner := cloudflarerealtime.Owner{
		RoomID: authenticated.roomID, ShareGeneration: generation,
		PeerID: authenticated.peerID, SignalingID: sess.sessionID, Role: role,
	}
	switch message.Kind {
	case "publish":
		if _, exists := s.cloudflareRegistry.SessionForOwner(owner, message.ConnectionID); exists {
			s.sendError(sess, "FORBIDDEN", "Cloudflare publisher already exists for this connection")
			return
		}
	case "subscribe":
		if _, exists := s.cloudflareRegistry.SessionForOwner(owner, message.ConnectionID); exists {
			s.sendError(sess, "FORBIDDEN", "Cloudflare subscriber already exists for this connection")
			return
		}
	case "complete":
		if _, exists := s.cloudflareRegistry.SessionForOwner(owner, message.ConnectionID); !exists {
			s.sendError(sess, "FORBIDDEN", "Cloudflare subscriber session is not owned by this connection")
			return
		}
	}
	var publisher cloudflarerealtime.SessionResource
	if message.Kind == "subscribe" {
		var ok bool
		publisher, ok = s.cloudflareRegistry.Publisher(authenticated.roomID, generation)
		if !ok || len(publisher.TrackNames) == 0 {
			s.sendError(sess, "PEER_NOT_FOUND", "Cloudflare publisher is not ready")
			return
		}
	}
	var subscriber cloudflarerealtime.SessionResource
	if message.Kind == "complete" {
		subscriber, _ = s.cloudflareRegistry.SessionForOwner(owner, message.ConnectionID)
	}
	go s.runCloudflareSFU(sess, authenticated, owner, message, publisher, subscriber)
}

func (s *Server) runCloudflareSFU(
	sess *session,
	authenticated *authenticatedSession,
	owner cloudflarerealtime.Owner,
	message protocol.CloudflareSFUMessage,
	publisher cloudflarerealtime.SessionResource,
	subscriber cloudflarerealtime.SessionResource,
) {
	ctx, cancel := context.WithTimeout(sess.ctx, 20*time.Second)
	defer cancel()
	var response protocol.CloudflareSFUServerMessage
	response = protocol.CloudflareSFUServerMessage{
		Type: "cloudflare-sfu", ShareGeneration: owner.ShareGeneration,
		ConnectionID: message.ConnectionID,
	}
	var cleanup *cloudflarerealtime.SessionResource
	var operationErr error

	switch message.Kind {
	case "publish":
		tracks := make([]cloudflarerealtime.Track, 0, len(message.Tracks))
		for _, track := range message.Tracks {
			tracks = append(tracks, cloudflarerealtime.Track{Location: "local", MID: track.MID, TrackName: track.TrackName})
		}
		publication, err := s.cloudflareBridge.Publish(ctx, cloudflarerealtime.SessionDescription{
			Type: message.Description.Type, SDP: message.Description.SDP,
		}, tracks)
		operationErr = err
		if err == nil {
			resource := cloudflarerealtime.SessionResource{SessionID: publication.SessionID, Owner: owner, ConnectionID: message.ConnectionID}
			for _, track := range publication.Tracks { resource.MIDs = append(resource.MIDs, track.MID); resource.TrackNames = append(resource.TrackNames, track.TrackName) }
			cleanup = &resource
			response.Kind, response.SessionID = "published", publication.SessionID
			response.Description = &protocol.SessionDescription{Type: publication.Answer.Type, SDP: publication.Answer.SDP}
			for _, track := range publication.Tracks { response.Tracks = append(response.Tracks, protocol.CloudflareSFUTrack{MID: track.MID, TrackName: track.TrackName}) }
		} else if publication.SessionID != "" {
			resource := cloudflarerealtime.SessionResource{SessionID: publication.SessionID, Owner: owner}
			for _, track := range publication.Tracks { resource.MIDs = append(resource.MIDs, track.MID) }
			cleanup = &resource
		}
	case "subscribe":
		subscription, err := s.cloudflareBridge.Subscribe(ctx, publisher.SessionID, publisher.TrackNames)
		operationErr = err
		if err == nil {
			resource := cloudflarerealtime.SessionResource{SessionID: subscription.SessionID, Owner: owner, ConnectionID: message.ConnectionID}
			for _, track := range subscription.Tracks { resource.MIDs = append(resource.MIDs, track.MID); resource.TrackNames = append(resource.TrackNames, track.TrackName) }
			cleanup = &resource
			response.Kind, response.SessionID = "subscribed", subscription.SessionID
			response.Description = &protocol.SessionDescription{Type: subscription.Offer.Type, SDP: subscription.Offer.SDP}
			for _, track := range subscription.Tracks { response.Tracks = append(response.Tracks, protocol.CloudflareSFUTrack{MID: track.MID, TrackName: track.TrackName}) }
		} else if subscription.SessionID != "" {
			resource := cloudflarerealtime.SessionResource{SessionID: subscription.SessionID, Owner: owner}
			for _, track := range subscription.Tracks { resource.MIDs = append(resource.MIDs, track.MID) }
			cleanup = &resource
		}
	case "complete":
		operationErr = s.cloudflareBridge.CompleteSubscription(ctx, subscriber.SessionID, cloudflarerealtime.SessionDescription{
			Type: message.Description.Type, SDP: message.Description.SDP,
		})
		response.Kind = "completed"
	}

	s.mu.Lock()
	current := !s.closing && s.sessions.Has(sess) && sess.open() &&
		sess.authenticated == authenticated && s.isCurrentSession(sess) &&
		s.shares[owner.RoomID].generation == owner.ShareGeneration
	if operationErr == nil && current {
		switch message.Kind {
		case "publish":
			operationErr = s.cloudflareRegistry.RegisterPublisherTracks(owner, message.ConnectionID, response.SessionID, tracksFromProtocol(response.Tracks, "local", ""))
		case "subscribe":
			operationErr = s.cloudflareRegistry.RegisterSubscriberTracks(owner, message.ConnectionID, response.SessionID, publisher.SessionID, tracksFromProtocol(response.Tracks, "remote", publisher.SessionID))
		}
	}
	if operationErr == nil && current {
		s.send(sess, response)
	} else if operationErr != nil && current {
		s.sendError(sess, "SERVER_ERROR", "Cloudflare SFU operation failed")
	}
	s.mu.Unlock()

	if cleanup != nil && (operationErr != nil || !current) {
		s.cleanupCloudflareResources([]cloudflarerealtime.SessionResource{*cleanup})
	}
}

func tracksFromProtocol(tracks []protocol.CloudflareSFUTrack, location, sessionID string) []cloudflarerealtime.TrackResult {
	result := make([]cloudflarerealtime.TrackResult, 0, len(tracks))
	for _, track := range tracks {
		result = append(result, cloudflarerealtime.TrackResult{
			Track: cloudflarerealtime.Track{Location: location, SessionID: sessionID, MID: track.MID, TrackName: track.TrackName},
		})
	}
	return result
}


package protocol

import "errors"

// CloudflareSFUTrack maps a browser transceiver MID to a stable Cloudflare track name.
type CloudflareSFUTrack struct {
	MID string `json:"mid"`
	TrackName string `json:"trackName"`
}

type CloudflareSFUMessage struct {
	Type string `json:"type"`
	Kind string `json:"kind"`
	ShareGeneration string `json:"shareGeneration"`
	ConnectionID string `json:"connectionId"`
	Description *SessionDescription `json:"description,omitempty"`
	Tracks []CloudflareSFUTrack `json:"tracks,omitempty"`
}

func (CloudflareSFUMessage) isClientMessage() {}

func decodeCloudflareSFU(data []byte) (ClientMessage, error) {
	var message CloudflareSFUMessage
	present, err := decodeObject(data, &message)
	if err != nil { return nil, err }
	if err := present.require("type", "kind", "shareGeneration", "connectionId"); err != nil { return nil, err }
	if err := present.optional("description", "tracks"); err != nil { return nil, err }
	if message.Type != "cloudflare-sfu" || !ValidOpaqueID(message.ShareGeneration) || !ValidOpaqueID(message.ConnectionID) {
		return nil, errors.New("invalid Cloudflare SFU fence")
	}
	switch message.Kind {
	case "publish":
		if message.Description == nil || message.Description.Type != "offer" || len(message.Tracks) < 1 || len(message.Tracks) > 4 {
			return nil, errors.New("invalid Cloudflare publisher offer")
		}
		seenMID, seenName := map[string]bool{}, map[string]bool{}
		for _, track := range message.Tracks {
			if track.MID == "" || len(track.MID) > 128 || track.TrackName == "" || len(track.TrackName) > 64 || seenMID[track.MID] || seenName[track.TrackName] {
				return nil, errors.New("invalid or duplicate Cloudflare publisher track")
			}
			seenMID[track.MID], seenName[track.TrackName] = true, true
		}
	case "subscribe":
		if message.Description != nil || len(message.Tracks) != 0 { return nil, errors.New("invalid Cloudflare subscription request") }
	case "complete":
		if message.Description == nil || message.Description.Type != "answer" || len(message.Tracks) != 0 { return nil, errors.New("invalid Cloudflare subscription answer") }
	default:
		return nil, errors.New("unknown Cloudflare SFU operation")
	}
	return message, nil
}

// CloudflareSFUServerMessage returns an SDP handoff or completion acknowledgement.
type CloudflareSFUServerMessage struct {
	Type string `json:"type"`
	Kind string `json:"kind"`
	ShareGeneration string `json:"shareGeneration"`
	ConnectionID string `json:"connectionId"`
	SessionID string `json:"sessionId,omitempty"`
	Description *SessionDescription `json:"description,omitempty"`
	Tracks []CloudflareSFUTrack `json:"tracks,omitempty"`
}

func (CloudflareSFUServerMessage) isServerMessage() {}

func decodeCloudflareSFUServer(data []byte) (ServerMessage, error) {
	var message CloudflareSFUServerMessage
	present, err := decodeObject(data, &message)
	if err != nil { return nil, err }
	if err := present.require("type", "kind", "shareGeneration", "connectionId"); err != nil { return nil, err }
	if err := present.optional("sessionId", "description", "tracks"); err != nil { return nil, err }
	if message.Type != "cloudflare-sfu" || !ValidOpaqueID(message.ShareGeneration) || !ValidOpaqueID(message.ConnectionID) {
		return nil, errors.New("invalid Cloudflare SFU response fence")
	}
	switch message.Kind {
	case "published", "subscribed":
		if !ValidOpaqueID(message.SessionID) || message.Description == nil || len(message.Tracks) == 0 {
			return nil, errors.New("incomplete Cloudflare SFU SDP response")
		}
	case "completed":
		if message.SessionID != "" || message.Description != nil || len(message.Tracks) != 0 {
			return nil, errors.New("invalid Cloudflare SFU completion response")
		}
	default:
		return nil, errors.New("unknown Cloudflare SFU response")
	}
	return message, nil
}

package cloudflare

import (
	"errors"
	"sync"
)

// Owner is the Piik authority tuple that owns one Cloudflare Session.
// Session IDs are never accepted from an untrusted browser as authority.
type Owner struct {
	RoomID          string
	ShareGeneration string
	PeerID          string
	SignalingID     string
	Role            string
}

func (o Owner) valid() bool {
	return o.RoomID != "" && o.ShareGeneration != "" && o.PeerID != "" &&
		o.SignalingID != "" && (o.Role == "host" || o.Role == "viewer")
}

// SessionResource is a snapshot suitable for asynchronous best-effort cleanup.
// MIDs are Cloudflare-local identifiers; callers must not confuse them with Piik IDs.
type SessionResource struct {
	SessionID string
	Owner     Owner
	ConnectionID string
	MIDs      []string
	TrackNames []string
	ChannelIDs []int
}

// Registry binds Cloudflare sessions to Piik room/share/participant authority.
// It performs no network I/O and never holds its lock while callers clean up.
type Registry struct {
	mu        sync.Mutex
	sessions  map[string]SessionResource
	publishers map[string]string // room + generation -> publisher session
}

func NewRegistry() *Registry {
	return &Registry{
		sessions: make(map[string]SessionResource),
		publishers: make(map[string]string),
	}
}

func publicationKey(roomID, generation string) string {
	return roomID + "\x00" + generation
}

// RegisterPublisher binds the Host's Cloudflare publisher session to the
// exact current share generation. A second publisher for the same generation
// is rejected rather than silently replacing the first.
func (r *Registry) RegisterPublisher(owner Owner, sessionID string, mids []string) error {
	return r.RegisterPublisherResources(owner, sessionID, mids, nil)
}

// RegisterPublisherResources binds all exact Cloudflare resources allocated on the Host endpoint.
func (r *Registry) RegisterPublisherResources(owner Owner, sessionID string, mids []string, channelIDs []int) error {
	if !owner.valid() || owner.Role != "host" || sessionID == "" || len(mids) == 0 || !validChannelIDs(channelIDs) {
		return errors.New("invalid Cloudflare publisher ownership")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := publicationKey(owner.RoomID, owner.ShareGeneration)
	if _, exists := r.publishers[key]; exists {
		return errors.New("Cloudflare publisher already exists for this share generation")
	}
	if _, exists := r.sessions[sessionID]; exists {
		return errors.New("Cloudflare session is already registered")
	}
	r.sessions[sessionID] = SessionResource{SessionID: sessionID, Owner: owner, MIDs: cloneStrings(mids), ChannelIDs: cloneInts(channelIDs)}
	r.publishers[key] = sessionID
	return nil
}

// RegisterSubscriber binds a Viewer session to the publisher generation it
// consumes. The publisher must already be registered and the subscriber's
// share generation must match exactly.
func (r *Registry) RegisterSubscriber(owner Owner, sessionID, publisherSessionID string, mids []string) error {
	return r.RegisterSubscriberResources(owner, sessionID, publisherSessionID, mids, nil)
}

// RegisterSubscriberResources binds all exact resources allocated on a Viewer endpoint.
func (r *Registry) RegisterSubscriberResources(owner Owner, sessionID, publisherSessionID string, mids []string, channelIDs []int) error {
	if !owner.valid() || owner.Role != "viewer" || sessionID == "" || publisherSessionID == "" || len(mids) == 0 || !validChannelIDs(channelIDs) {
		return errors.New("invalid Cloudflare subscriber ownership")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := publicationKey(owner.RoomID, owner.ShareGeneration)
	if r.publishers[key] != publisherSessionID {
		return errors.New("Cloudflare publisher does not belong to this room share")
	}
	if _, exists := r.sessions[sessionID]; exists {
		return errors.New("Cloudflare session is already registered")
	}
	r.sessions[sessionID] = SessionResource{SessionID: sessionID, Owner: owner, MIDs: cloneStrings(mids), ChannelIDs: cloneInts(channelIDs)}
	return nil
}

// Publisher returns the registered Host publisher only for the exact room
// and share generation. A stale share cannot resolve a newer publisher.
func (r *Registry) Publisher(roomID, generation string) (SessionResource, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id := r.publishers[publicationKey(roomID, generation)]
	resource, ok := r.sessions[id]
	return cloneResource(resource), ok
}

// Owns reports whether a signaling session still owns the specified Cloudflare
// session. It fences messages from replaced browser connections.
func (r *Registry) Owns(sessionID string, owner Owner) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	resource, ok := r.sessions[sessionID]
	return ok && resource.Owner == owner
}

// RetirePeer removes every Cloudflare session owned by the exact participant
// and returns immutable cleanup snapshots. A replaced session ID cannot retire
// resources belonging to its successor.
func (r *Registry) RetirePeer(roomID, generation, peerID, signalingID string) []SessionResource {
	return r.retire(func(resource SessionResource) bool {
		o := resource.Owner
		return o.RoomID == roomID && o.ShareGeneration == generation &&
			o.PeerID == peerID && o.SignalingID == signalingID
	})
}

// RetireShare removes the publisher and all viewer sessions for one share
// generation. It intentionally does not touch a later generation.
func (r *Registry) RetireShare(roomID, generation string) []SessionResource {
	return r.retire(func(resource SessionResource) bool {
		return resource.Owner.RoomID == roomID && resource.Owner.ShareGeneration == generation
	})
}

func (r *Registry) retire(match func(SessionResource) bool) []SessionResource {
	r.mu.Lock()
	defer r.mu.Unlock()
	var retired []SessionResource
	for id, resource := range r.sessions {
		if !match(resource) {
			continue
		}
		retired = append(retired, cloneResource(resource))
		delete(r.sessions, id)
		if resource.Owner.Role == "host" {
			key := publicationKey(resource.Owner.RoomID, resource.Owner.ShareGeneration)
			if r.publishers[key] == id {
				delete(r.publishers, key)
			}
		}
	}
	return retired
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}

func cloneResource(resource SessionResource) SessionResource {
	resource.MIDs = cloneStrings(resource.MIDs)
	resource.TrackNames = cloneStrings(resource.TrackNames)
	resource.ChannelIDs = cloneInts(resource.ChannelIDs)
	return resource
}

func cloneInts(values []int) []int { return append([]int(nil), values...) }

func validChannelIDs(values []int) bool {
	seen := make(map[int]struct{}, len(values))
	for _, id := range values {
		if id < 0 || id > 65_534 { return false }
		if _, exists := seen[id]; exists { return false }
		seen[id] = struct{}{}
	}
	return true
}


// RetireAll removes every registered session and returns immutable cleanup
// snapshots. It is used during process shutdown when individual disconnect
// callbacks intentionally skip room mutations.
func (r *Registry) RetireAll() []SessionResource {
	return r.retire(func(SessionResource) bool { return true })
}


// RegisterPublisherTracks records the Host's Cloudflare track names and browser
// connection fence alongside the endpoint-local MIDs.
func (r *Registry) RegisterPublisherTracks(owner Owner, connectionID, sessionID string, tracks []TrackResult) error {
	if !owner.valid() || owner.Role != "host" || connectionID == "" || sessionID == "" || len(tracks) == 0 {
		return errors.New("invalid Cloudflare publisher track ownership")
	}
	mids, names := make([]string, 0, len(tracks)), make([]string, 0, len(tracks))
	seenMID, seenName := map[string]bool{}, map[string]bool{}
	for _, track := range tracks {
		if track.Location != "local" || track.MID == "" || track.TrackName == "" || seenMID[track.MID] || seenName[track.TrackName] {
			return errors.New("invalid Cloudflare publisher track result")
		}
		seenMID[track.MID], seenName[track.TrackName] = true, true
		mids, names = append(mids, track.MID), append(names, track.TrackName)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := publicationKey(owner.RoomID, owner.ShareGeneration)
	if _, exists := r.publishers[key]; exists { return errors.New("Cloudflare publisher already exists for this share generation") }
	if _, exists := r.sessions[sessionID]; exists { return errors.New("Cloudflare session is already registered") }
	r.sessions[sessionID] = SessionResource{SessionID: sessionID, Owner: owner, ConnectionID: connectionID, MIDs: mids, TrackNames: names}
	r.publishers[key] = sessionID
	return nil
}

// RegisterSubscriberTracks binds the Viewer session to the exact publisher
// and preserves the track-name mapping for diagnostics and later operations.
func (r *Registry) RegisterSubscriberTracks(owner Owner, connectionID, sessionID, publisherSessionID string, tracks []TrackResult) error {
	if !owner.valid() || owner.Role != "viewer" || connectionID == "" || sessionID == "" || publisherSessionID == "" || len(tracks) == 0 {
		return errors.New("invalid Cloudflare subscriber track ownership")
	}
	mids, names := make([]string, 0, len(tracks)), make([]string, 0, len(tracks))
	seenMID, seenName := map[string]bool{}, map[string]bool{}
	for _, track := range tracks {
		if track.Location != "remote" || track.SessionID != publisherSessionID || track.MID == "" || track.TrackName == "" || seenMID[track.MID] || seenName[track.TrackName] {
			return errors.New("invalid Cloudflare subscriber track result")
		}
		seenMID[track.MID], seenName[track.TrackName] = true, true
		mids, names = append(mids, track.MID), append(names, track.TrackName)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	key := publicationKey(owner.RoomID, owner.ShareGeneration)
	if r.publishers[key] != publisherSessionID { return errors.New("Cloudflare publisher does not belong to this room share") }
	if _, exists := r.sessions[sessionID]; exists { return errors.New("Cloudflare session is already registered") }
	r.sessions[sessionID] = SessionResource{SessionID: sessionID, Owner: owner, ConnectionID: connectionID, MIDs: mids, TrackNames: names}
	return nil
}

// SessionForOwner resolves a Cloudflare endpoint only for the exact signaling
// owner and client connection fence.
func (r *Registry) SessionForOwner(owner Owner, connectionID string) (SessionResource, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, resource := range r.sessions {
		if resource.Owner == owner && resource.ConnectionID == connectionID {
			return cloneResource(resource), true
		}
	}
	return SessionResource{}, false
}

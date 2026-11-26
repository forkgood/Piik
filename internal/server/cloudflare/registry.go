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
	MIDs      []string
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
	if !owner.valid() || owner.Role != "host" || sessionID == "" || len(mids) == 0 {
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
	r.sessions[sessionID] = SessionResource{SessionID: sessionID, Owner: owner, MIDs: cloneStrings(mids)}
	r.publishers[key] = sessionID
	return nil
}

// RegisterSubscriber binds a Viewer session to the publisher generation it
// consumes. The publisher must already be registered and the subscriber's
// share generation must match exactly.
func (r *Registry) RegisterSubscriber(owner Owner, sessionID, publisherSessionID string, mids []string) error {
	if !owner.valid() || owner.Role != "viewer" || sessionID == "" || publisherSessionID == "" || len(mids) == 0 {
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
	r.sessions[sessionID] = SessionResource{SessionID: sessionID, Owner: owner, MIDs: cloneStrings(mids)}
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
	return resource
}

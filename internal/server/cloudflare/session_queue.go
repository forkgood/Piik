package cloudflare

import (
	"context"
	"errors"
	"sync"
)

// SessionQueue serializes complete operations for each Cloudflare session.
// The callback must include the entire SDP exchange when an API response
// requires immediate renegotiation; returning after merely forwarding an
// offer to a browser would release the queue too early.
type SessionQueue struct {
	mu       sync.Mutex
	sessions map[string]*sessionGate
}

type sessionGate struct {
	token chan struct{}
	refs  int
}

func NewSessionQueue() *SessionQueue {
	return &SessionQueue{sessions: make(map[string]*sessionGate)}
}

// Do runs operation exclusively with respect to other operations targeting
// the same session. Different sessions remain independent. Waiting is
// cancellable, and canceled waiters release their queue reference.
func (q *SessionQueue) Do(ctx context.Context, sessionID string, operation func(context.Context) error) error {
	if q == nil {
		return errors.New("Cloudflare session queue is nil")
	}
	if sessionID == "" {
		return errors.New("Cloudflare session ID is required")
	}
	if operation == nil {
		return errors.New("Cloudflare session operation is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	q.mu.Lock()
	if q.sessions == nil {
		q.sessions = make(map[string]*sessionGate)
	}
	gate := q.sessions[sessionID]
	if gate == nil {
		gate = &sessionGate{token: make(chan struct{}, 1)}
		q.sessions[sessionID] = gate
	}
	gate.refs++
	q.mu.Unlock()

	select {
	case gate.token <- struct{}{}:
		defer func() {
			<-gate.token
			q.release(sessionID, gate)
		}()
	case <-ctx.Done():
		q.release(sessionID, gate)
		return ctx.Err()
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	return operation(ctx)
}

func (q *SessionQueue) release(sessionID string, gate *sessionGate) {
	q.mu.Lock()
	defer q.mu.Unlock()
	gate.refs--
	if gate.refs == 0 && q.sessions[sessionID] == gate {
		delete(q.sessions, sessionID)
	}
}

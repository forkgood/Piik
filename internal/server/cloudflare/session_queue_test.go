package cloudflare

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSessionQueueSerializesSameSession(t *testing.T) {
	queue := NewSessionQueue()
	var active atomic.Int32
	var maximum atomic.Int32
	var wait sync.WaitGroup
	for i := 0; i < 12; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			err := queue.Do(context.Background(), "same-session", func(context.Context) error {
				current := active.Add(1)
				for {
					old := maximum.Load()
					if current <= old || maximum.CompareAndSwap(old, current) { break }
				}
				time.Sleep(5 * time.Millisecond)
				active.Add(-1)
				return nil
			})
			if err != nil { t.Errorf("Do: %v", err) }
		}()
	}
	wait.Wait()
	if got := maximum.Load(); got != 1 { t.Fatalf("maximum concurrent operations = %d, want 1", got) }
}

func TestSessionQueueAllowsDifferentSessionsConcurrently(t *testing.T) {
	queue := NewSessionQueue()
	started := make(chan string, 2)
	release := make(chan struct{})
	var wait sync.WaitGroup
	for _, id := range []string{"one", "two"} {
		wait.Add(1)
		go func(id string) {
			defer wait.Done()
			if err := queue.Do(context.Background(), id, func(context.Context) error {
				started <- id
				<-release
				return nil
			}); err != nil { t.Errorf("Do(%s): %v", id, err) }
		}(id)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			wait.Wait()
			t.Fatal("different session operations did not overlap")
		}
	}
	close(release)
	wait.Wait()
}

func TestSessionQueueCancelsWaitingOperation(t *testing.T) {
	queue := NewSessionQueue()
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- queue.Do(context.Background(), "session", func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	waitingDone := make(chan error, 1)
	go func() {
		waitingDone <- queue.Do(ctx, "session", func(context.Context) error {
			return errors.New("canceled waiter must not execute")
		})
	}()
	cancel()
	select {
	case err := <-waitingDone:
		if !errors.Is(err, context.Canceled) { t.Fatalf("waiting operation error = %v", err) }
	case <-time.After(time.Second):
		close(release)
		<-firstDone
		t.Fatal("waiting operation did not cancel")
	}
	close(release)
	if err := <-firstDone; err != nil { t.Fatal(err) }
}

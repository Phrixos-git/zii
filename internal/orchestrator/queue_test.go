package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRequestQueueSerializesSameConversationFIFO(t *testing.T) {
	q, err := NewRequestQueue(QueueConfig{MaxQueued: 4, MaxRunning: 3, QueueWait: time.Second, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	gate := make(chan struct{})
	var mu sync.Mutex
	order := []int{}
	first := make(chan struct{})
	go func() {
		_ = q.Submit(context.Background(), "same", func(context.Context) error {
			close(first)
			<-gate
			mu.Lock()
			order = append(order, 1)
			mu.Unlock()
			return nil
		})
	}()
	<-first
	secondDone := make(chan error, 1)
	go func() {
		secondDone <- q.Submit(context.Background(), "same", func(context.Context) error { mu.Lock(); order = append(order, 2); mu.Unlock(); return nil })
	}()
	time.Sleep(30 * time.Millisecond)
	close(gate)
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Fatalf("order = %v", order)
	}
}

func TestRequestQueueRunsDifferentConversationsConcurrently(t *testing.T) {
	q, err := NewRequestQueue(QueueConfig{MaxQueued: 2, MaxRunning: 2, QueueWait: time.Second, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	results := make(chan error, 2)
	for _, key := range []string{"a", "b"} {
		key := key
		go func() {
			results <- q.Submit(context.Background(), key, func(context.Context) error { started <- struct{}{}; <-release; return nil })
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("requests did not run concurrently")
		}
	}
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRequestQueueReturnsBusyAtCapacity(t *testing.T) {
	q, err := NewRequestQueue(QueueConfig{MaxQueued: 1, MaxRunning: 1, QueueWait: time.Second, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	go q.Submit(context.Background(), "a", func(context.Context) error { close(started); <-release; return nil })
	<-started
	queued := make(chan error, 1)
	go func() { queued <- q.Submit(context.Background(), "b", func(context.Context) error { return nil }) }()
	time.Sleep(20 * time.Millisecond)
	if err := q.Submit(context.Background(), "c", func(context.Context) error { return nil }); err != ErrBusy {
		t.Fatalf("Submit error = %v, want ErrBusy", err)
	}
	close(release)
	if err := <-queued; err != nil {
		t.Fatal(err)
	}
}

func TestSubmitWithAcceptedRunsAfterQueueInsertionAndBeforeWork(t *testing.T) {
	q, err := NewRequestQueue(QueueConfig{MaxQueued: 1, MaxRunning: 1, QueueWait: time.Second, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	workStarted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- q.SubmitWithAccepted(context.Background(), "accepted", func(ctx context.Context) error {
			// The current job already occupies the only queue slot, and the
			// callback can acquire q.mu because it runs outside that lock.
			if err := q.Submit(ctx, "other", func(context.Context) error { return nil }); !errors.Is(err, ErrBusy) {
				return fmt.Errorf("nested submit error = %v, want ErrBusy", err)
			}
			return nil
		}, func(context.Context) error { close(workStarted); return nil })
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("admission callback blocked on queue mutex")
	}
	select {
	case <-workStarted:
	default:
		t.Fatal("work did not start after successful acceptance callback")
	}
}

func TestSubmitWithAcceptedFailureSkipsWorkAndReleasesQueueSlot(t *testing.T) {
	q, err := NewRequestQueue(QueueConfig{MaxQueued: 1, MaxRunning: 1, QueueWait: time.Second, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	acceptErr := errors.New("acknowledgement failed")
	workRan := false
	err = q.SubmitWithAccepted(context.Background(), "same", func(context.Context) error { return acceptErr }, func(context.Context) error {
		workRan = true
		return nil
	})
	if !errors.Is(err, acceptErr) || workRan {
		t.Fatalf("submit error=%v workRan=%t", err, workRan)
	}
	if err := q.Submit(context.Background(), "same", func(context.Context) error { return nil }); err != nil {
		t.Fatalf("queue slot/key was not released after admission failure: %v", err)
	}
}

func TestSubmitWithAcceptedDoesNotRunCallbackWhenQueueIsFull(t *testing.T) {
	q, err := NewRequestQueue(QueueConfig{MaxQueued: 1, MaxRunning: 1, QueueWait: time.Second, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- q.Submit(context.Background(), "active", func(context.Context) error { close(started); <-release; return nil })
	}()
	<-started
	queued := make(chan error, 1)
	go func() {
		queued <- q.Submit(context.Background(), "queued", func(context.Context) error { return nil })
	}()
	time.Sleep(20 * time.Millisecond)
	called := false
	if err := q.SubmitWithAccepted(context.Background(), "full", func(context.Context) error { called = true; return nil }, func(context.Context) error { return nil }); !errors.Is(err, ErrBusy) {
		t.Fatalf("full queue error = %v, want ErrBusy", err)
	}
	if called {
		t.Fatal("acceptance callback ran for a request rejected by the full queue")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-queued; err != nil {
		t.Fatal(err)
	}
}

func TestShutdownCancelsAnAdmissionCallbackWithoutStartingWork(t *testing.T) {
	q, err := NewRequestQueue(QueueConfig{MaxQueued: 1, MaxRunning: 1, QueueWait: time.Second, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	acceptedStarted := make(chan struct{})
	workRan := false
	submitted := make(chan error, 1)
	go func() {
		submitted <- q.SubmitWithAccepted(context.Background(), "admitting", func(ctx context.Context) error {
			close(acceptedStarted)
			<-ctx.Done()
			return ctx.Err()
		}, func(context.Context) error {
			workRan = true
			return nil
		})
	}()
	<-acceptedStarted
	if err := q.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := <-submitted; !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("admitting request result = %v, want ErrQueueClosed", err)
	}
	if workRan {
		t.Fatal("work ran after shutdown canceled its admission callback")
	}
}

func TestRequestQueueWaitTimeoutDoesNotStartExpiredWork(t *testing.T) {
	q, err := NewRequestQueue(QueueConfig{MaxQueued: 2, MaxRunning: 1, QueueWait: 30 * time.Millisecond, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- q.Submit(context.Background(), "active", func(context.Context) error {
			close(started)
			<-release
			return nil
		})
	}()
	<-started
	secondStarted := make(chan struct{}, 1)
	err = q.Submit(context.Background(), "queued", func(context.Context) error {
		secondStarted <- struct{}{}
		return nil
	})
	if !errors.Is(err, ErrQueueWaitTimeout) {
		t.Fatalf("queued Submit error = %v, want ErrQueueWaitTimeout", err)
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondStarted:
		t.Fatal("expired queued work was started")
	case <-time.After(150 * time.Millisecond):
	}
}

func TestRequestQueueCancelsWorkAtRequestDeadline(t *testing.T) {
	q, err := NewRequestQueue(QueueConfig{MaxQueued: 1, MaxRunning: 1, QueueWait: time.Second, RequestTimeout: 30 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Shutdown(context.Background())
	workCanceled := make(chan struct{})
	err = q.Submit(context.Background(), "deadline", func(ctx context.Context) error {
		<-ctx.Done()
		close(workCanceled)
		return ctx.Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Submit error = %v, want request deadline", err)
	}
	select {
	case <-workCanceled:
	case <-time.After(time.Second):
		t.Fatal("request deadline did not cancel work")
	}
}

func TestRequestQueueShutdownCancelsUnstartedAndActiveRequests(t *testing.T) {
	q, err := NewRequestQueue(QueueConfig{MaxQueued: 2, MaxRunning: 1, QueueWait: time.Second, RequestTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	activeStarted := make(chan struct{})
	activeCanceled := make(chan struct{})
	activeResult := make(chan error, 1)
	go func() {
		activeResult <- q.Submit(context.Background(), "active", func(ctx context.Context) error {
			close(activeStarted)
			<-ctx.Done()
			close(activeCanceled)
			return ctx.Err()
		})
	}()
	<-activeStarted
	queuedStarted := make(chan struct{}, 1)
	queuedResult := make(chan error, 1)
	go func() {
		queuedResult <- q.Submit(context.Background(), "queued", func(context.Context) error {
			queuedStarted <- struct{}{}
			return nil
		})
	}()
	time.Sleep(20 * time.Millisecond)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err = q.Shutdown(shutdownCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want grace deadline after cancel", err)
	}
	if err := <-activeResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("active request result = %v, want cancellation", err)
	}
	if err := <-queuedResult; !errors.Is(err, ErrQueueClosed) {
		t.Fatalf("queued request result = %v, want ErrQueueClosed", err)
	}
	select {
	case <-activeCanceled:
	case <-time.After(time.Second):
		t.Fatal("active request was not canceled at shutdown deadline")
	}
	select {
	case <-queuedStarted:
		t.Fatal("unstarted request ran after shutdown")
	default:
	}
}

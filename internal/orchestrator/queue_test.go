package orchestrator

import (
	"context"
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

package orchestrator

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrBusy = errors.New("orchestrator: request queue is full")
var ErrQueueWaitTimeout = errors.New("orchestrator: request queue wait timed out")
var ErrQueueClosed = errors.New("orchestrator: request queue is closed")

const (
	defaultQueueSize      = 10
	defaultQueueWait      = 180 * time.Second
	defaultRequestTimeout = 300 * time.Second
	defaultLLMConcurrency = 2
	defaultMCPConcurrency = 4
)

type queuedRequest struct {
	ctx        context.Context
	cancel     context.CancelFunc
	key        string
	enqueuedAt time.Time
	started    chan struct{}
	ready      bool
	work       func(context.Context) error
	result     chan error
}

// RequestQueue dispatches FIFO work while preventing overlapping work for a
// single conversation key. Different conversations may run concurrently.
type RequestQueue struct {
	mu             sync.Mutex
	queue          []*queuedRequest
	active         map[string]context.CancelFunc
	closed         bool
	maxQueued      int
	maxRunning     int
	queueWait      time.Duration
	requestTimeout time.Duration
	wake           chan struct{}
	running        sync.WaitGroup
}

type QueueConfig struct {
	MaxQueued, MaxRunning     int
	QueueWait, RequestTimeout time.Duration
}

func NewRequestQueue(cfg QueueConfig) (*RequestQueue, error) {
	if cfg.MaxQueued == 0 {
		cfg.MaxQueued = defaultQueueSize
	}
	if cfg.MaxRunning == 0 {
		cfg.MaxRunning = 10
	}
	if cfg.QueueWait == 0 {
		cfg.QueueWait = defaultQueueWait
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = defaultRequestTimeout
	}
	if cfg.MaxQueued < 1 || cfg.MaxRunning < 1 || cfg.QueueWait <= 0 || cfg.RequestTimeout <= 0 {
		return nil, errors.New("orchestrator: invalid queue configuration")
	}
	q := &RequestQueue{active: make(map[string]context.CancelFunc), maxQueued: cfg.MaxQueued, maxRunning: cfg.MaxRunning, queueWait: cfg.QueueWait, requestTimeout: cfg.RequestTimeout, wake: make(chan struct{}, 1)}
	go q.dispatch()
	return q, nil
}

func (q *RequestQueue) Submit(ctx context.Context, conversationKey string, work func(context.Context) error) error {
	return q.SubmitWithAccepted(ctx, conversationKey, nil, work)
}

// SubmitWithAccepted reserves a queue slot before calling accepted. The
// callback runs outside the queue lock and must succeed before the work can be
// dispatched. This lets callers acknowledge an accepted request without
// allowing its processing to start first.
func (q *RequestQueue) SubmitWithAccepted(ctx context.Context, conversationKey string, accepted func(context.Context) error, work func(context.Context) error) error {
	if q == nil || ctx == nil || work == nil || conversationKey == "" {
		return errors.New("orchestrator: invalid queued request")
	}
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrQueueClosed
	}
	if len(q.queue) >= q.maxQueued {
		q.mu.Unlock()
		return ErrBusy
	}
	requestCtx, cancel := context.WithTimeout(ctx, q.requestTimeout)
	job := &queuedRequest{ctx: requestCtx, cancel: cancel, key: conversationKey, started: make(chan struct{}), work: work, result: make(chan error, 1)}
	q.queue = append(q.queue, job)
	q.mu.Unlock()

	if accepted != nil {
		if err := accepted(requestCtx); err != nil {
			q.mu.Lock()
			removed := false
			for i, queued := range q.queue {
				if queued == job {
					q.queue = append(q.queue[:i], q.queue[i+1:]...)
					removed = true
					break
				}
			}
			closed := q.closed
			q.mu.Unlock()
			cancel()
			if removed {
				q.signal()
				return err
			}
			if closed {
				return ErrQueueClosed
			}
			select {
			case resultErr := <-job.result:
				return resultErr
			default:
				return err
			}
		}
	}

	q.mu.Lock()
	queued := false
	for _, candidate := range q.queue {
		if candidate == job {
			queued = true
			break
		}
	}
	if !queued || q.closed {
		closed := q.closed
		q.mu.Unlock()
		cancel()
		if closed {
			return ErrQueueClosed
		}
		select {
		case err := <-job.result:
			return err
		default:
			return requestCtx.Err()
		}
	}
	if err := requestCtx.Err(); err != nil {
		for i, candidate := range q.queue {
			if candidate == job {
				q.queue = append(q.queue[:i], q.queue[i+1:]...)
				break
			}
		}
		q.mu.Unlock()
		cancel()
		q.signal()
		return err
	}
	job.enqueuedAt = time.Now()
	job.ready = true
	q.mu.Unlock()
	q.signal()
	timer := time.NewTimer(q.queueWait)
	defer timer.Stop()
	defer cancel()
	select {
	case err := <-job.result:
		return err
	case <-requestCtx.Done():
		select {
		case err := <-job.result:
			return err
		default:
			return requestCtx.Err()
		}
	case <-timer.C:
		select {
		case <-job.started:
			return <-job.result
		default:
			return ErrQueueWaitTimeout
		}
	case <-job.started:
		timer.Stop()
		select {
		case err := <-job.result:
			return err
		case <-requestCtx.Done():
			return requestCtx.Err()
		}
	}
}

func (q *RequestQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *RequestQueue) dispatch() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	running := 0
	for {
		q.mu.Lock()
		for i := 0; i < len(q.queue); {
			job := q.queue[i]
			if job.ready && (job.ctx.Err() != nil || time.Since(job.enqueuedAt) >= q.queueWait) {
				q.queue = append(q.queue[:i], q.queue[i+1:]...)
				if job.ctx.Err() != nil {
					job.result <- job.ctx.Err()
				} else {
					job.result <- ErrQueueWaitTimeout
				}
				job.cancel()
				continue
			}
			i++
		}
		for running < q.maxRunning {
			idx := -1
			for i, job := range q.queue {
				if job.ready {
					if _, busy := q.active[job.key]; !busy {
						idx = i
						break
					}
				}
			}
			if idx < 0 {
				break
			}
			job := q.queue[idx]
			q.queue = append(q.queue[:idx], q.queue[idx+1:]...)
			close(job.started)
			workCtx, cancel := context.WithTimeout(job.ctx, q.requestTimeout)
			q.active[job.key] = cancel
			running++
			q.running.Add(1)
			go func(job *queuedRequest) {
				err := job.work(workCtx)
				cancel()
				job.result <- err
				q.mu.Lock()
				delete(q.active, job.key)
				q.mu.Unlock()
				q.running.Done()
				q.signal()
				q.mu.Lock()
				running--
				q.mu.Unlock()
			}(job)
		}
		closed := q.closed && len(q.queue) == 0 && running == 0
		q.mu.Unlock()
		if closed {
			return
		}
		select {
		case <-q.wake:
		case <-ticker.C:
		}
	}
}

// Shutdown stops intake, cancels queued work, and waits up to grace for active work.
func (q *RequestQueue) Shutdown(ctx context.Context) error {
	if q == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("orchestrator: shutdown context is nil")
	}
	q.mu.Lock()
	q.closed = true
	for _, job := range q.queue {
		job.result <- ErrQueueClosed
		job.cancel()
	}
	q.queue = nil
	q.mu.Unlock()
	q.signal()
	done := make(chan struct{})
	go func() { q.running.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		q.mu.Lock()
		for _, cancel := range q.active {
			cancel()
		}
		q.mu.Unlock()
		<-done
		return ctx.Err()
	}
}

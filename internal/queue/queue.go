// Package queue delivers jobs to sinks on a worker pool with retries.
//
// Coolify does not retry when the relay answers with an error status, so the
// relay accepts every valid event immediately and owns delivery from here on.
// The queue is in-memory: a job in flight when the process dies is lost.
package queue

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/4shanzahid/coolify-notify-relay/internal/httpx"
	"github.com/4shanzahid/coolify-notify-relay/internal/metrics"
	"github.com/4shanzahid/coolify-notify-relay/internal/sink"
)

type Task struct {
	Sink sink.Sink
	Job  *sink.Job
}

type Options struct {
	Workers     int
	Size        int
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	SendTimeout time.Duration
}

type Queue struct {
	opts    Options
	log     *slog.Logger
	metrics *metrics.Registry

	mu     sync.RWMutex
	closed bool
	ch     chan Task
	wg     sync.WaitGroup

	// stop is cancelled when shutdown runs out of time; it aborts in-flight
	// sends and backoff sleeps.
	stop   context.Context
	cancel context.CancelFunc
}

var ErrFull = errors.New("queue full")

func New(opts Options, log *slog.Logger, m *metrics.Registry) *Queue {
	if opts.Workers <= 0 {
		opts.Workers = 4
	}
	if opts.Size <= 0 {
		opts.Size = 256
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = 6
	}
	if opts.BaseDelay <= 0 {
		opts.BaseDelay = time.Second
	}
	if opts.MaxDelay <= 0 {
		opts.MaxDelay = time.Minute
	}
	if opts.SendTimeout <= 0 {
		opts.SendTimeout = 30 * time.Second
	}
	stop, cancel := context.WithCancel(context.Background())
	q := &Queue{opts: opts, log: log, metrics: m, ch: make(chan Task, opts.Size), stop: stop, cancel: cancel}
	m.Gauge("relay_queue_depth", "Jobs waiting for a worker.", func() float64 { return float64(len(q.ch)) })
	return q
}

func (q *Queue) Start() {
	for range q.opts.Workers {
		q.wg.Go(func() {
			for t := range q.ch {
				q.deliver(t)
			}
		})
	}
}

// Enqueue adds a task without blocking.
func (q *Queue) Enqueue(t Task) error {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.closed {
		return errors.New("queue closed")
	}
	select {
	case q.ch <- t:
		return nil
	default:
		return ErrFull
	}
}

// Shutdown stops accepting tasks and drains the queue until ctx expires, then
// aborts whatever is still in flight.
func (q *Queue) Shutdown(ctx context.Context) error {
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		close(q.ch)
	}
	q.mu.Unlock()

	done := make(chan struct{})
	go func() { q.wg.Wait(); close(done) }()
	select {
	case <-done:
		q.cancel()
		return nil
	case <-ctx.Done():
		q.cancel()
		<-done
		return ctx.Err()
	}
}

func (q *Queue) deliver(t Task) {
	log := q.log.With("sink", t.Sink.Name(), "route", t.Job.Route.Name, "event", t.Job.Event.Event,
		"app", t.Job.Event.ShortAppName(), "deployment_uuid", t.Job.Event.DeploymentUUID)

	for attempt := 1; ; attempt++ {
		ctx, cancel := context.WithTimeout(q.stop, q.opts.SendTimeout)
		err := t.Sink.Send(ctx, t.Job)
		cancel()
		if err == nil {
			q.metrics.Inc("relay_deliveries_total", "sink", t.Sink.Name(), "result", "ok")
			log.Info("delivered", "attempt", attempt)
			return
		}
		if !httpx.Retryable(err) || attempt >= q.opts.MaxAttempts || q.stop.Err() != nil {
			q.metrics.Inc("relay_deliveries_total", "sink", t.Sink.Name(), "result", "failed")
			log.Error("delivery failed", "attempt", attempt, "err", err)
			return
		}
		delay := q.backoff(attempt, httpx.RetryAfter(err))
		q.metrics.Inc("relay_retries_total", "sink", t.Sink.Name())
		log.Warn("delivery failed, retrying", "attempt", attempt, "in", delay.String(), "err", err)
		select {
		case <-time.After(delay):
		case <-q.stop.Done():
			q.metrics.Inc("relay_deliveries_total", "sink", t.Sink.Name(), "result", "aborted")
			log.Error("delivery aborted by shutdown", "attempt", attempt)
			return
		}
	}
}

// backoff is exponential with ±20% jitter, never shorter than retryAfter.
func (q *Queue) backoff(attempt int, retryAfter time.Duration) time.Duration {
	d := q.opts.BaseDelay << (attempt - 1)
	if d <= 0 || d > q.opts.MaxDelay {
		d = q.opts.MaxDelay
	}
	d = max(retryAfter, time.Duration(float64(d)*(0.8+0.4*rand.Float64())))
	return d
}

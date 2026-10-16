package queue

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/4shanzahid/coolify-notify-relay/internal/config"
	"github.com/4shanzahid/coolify-notify-relay/internal/coolify"
	"github.com/4shanzahid/coolify-notify-relay/internal/httpx"
	"github.com/4shanzahid/coolify-notify-relay/internal/metrics"
	"github.com/4shanzahid/coolify-notify-relay/internal/sink"
)

type fakeSink struct {
	calls atomic.Int32
	fail  func(n int32) error
}

func (f *fakeSink) Name() string { return "fake" }
func (f *fakeSink) Send(context.Context, *sink.Job) error {
	return f.fail(f.calls.Add(1))
}

func newQueue(maxAttempts int) *Queue {
	q := New(Options{Workers: 2, MaxAttempts: maxAttempts, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond},
		slog.New(slog.NewTextHandler(io.Discard, nil)), metrics.New())
	q.Start()
	return q
}

func task(s sink.Sink) Task {
	return Task{Sink: s, Job: &sink.Job{Event: &coolify.Event{Event: "test"}, Route: &config.Route{Name: "r"}}}
}

func drain(t *testing.T, q *Queue) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := q.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRetriesTransientErrors(t *testing.T) {
	s := &fakeSink{fail: func(n int32) error {
		if n < 3 {
			return &httpx.StatusError{Code: 429}
		}
		return nil
	}}
	q := newQueue(6)
	if err := q.Enqueue(task(s)); err != nil {
		t.Fatal(err)
	}
	drain(t, q)
	if got := s.calls.Load(); got != 3 {
		t.Errorf("calls = %d, want 3", got)
	}
}

func TestStopsOnPermanentError(t *testing.T) {
	s := &fakeSink{fail: func(int32) error { return &httpx.StatusError{Code: 400} }}
	q := newQueue(6)
	_ = q.Enqueue(task(s))
	drain(t, q)
	if got := s.calls.Load(); got != 1 {
		t.Errorf("calls = %d, want 1", got)
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	s := &fakeSink{fail: func(int32) error { return errors.New("connection refused") }}
	q := newQueue(4)
	_ = q.Enqueue(task(s))
	drain(t, q)
	if got := s.calls.Load(); got != 4 {
		t.Errorf("calls = %d, want 4", got)
	}
}

func TestEnqueueAfterShutdown(t *testing.T) {
	q := newQueue(1)
	drain(t, q)
	if err := q.Enqueue(task(&fakeSink{fail: func(int32) error { return nil }})); err == nil {
		t.Error("expected error after shutdown")
	}
}

func TestBackoffHonoursRetryAfter(t *testing.T) {
	q := &Queue{opts: Options{BaseDelay: time.Second, MaxDelay: time.Minute}}
	if d := q.backoff(1, 10*time.Second); d != 10*time.Second {
		t.Errorf("backoff = %v", d)
	}
	if d := q.backoff(30, 0); d > 72*time.Second {
		t.Errorf("backoff not capped: %v", d)
	}
}

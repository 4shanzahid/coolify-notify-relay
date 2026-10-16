// Package sink defines the delivery targets the queue sends to.
package sink

import (
	"context"

	"github.com/4shanzahid/coolify-notify-relay/internal/config"
	"github.com/4shanzahid/coolify-notify-relay/internal/coolify"
)

// Job is one event delivered via one route.
type Job struct {
	Event *coolify.Event
	Route *config.Route
}

// Sink delivers a job. Errors are classified with httpx.Retryable; wrap
// non-retryable failures with httpx.Permanent.
type Sink interface {
	Name() string
	Send(ctx context.Context, job *Job) error
}

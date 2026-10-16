// Package googlechat posts events to Google Chat incoming webhooks.
package googlechat

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/4shanzahid/coolify-notify-relay/internal/config"
	"github.com/4shanzahid/coolify-notify-relay/internal/coolify"
	"github.com/4shanzahid/coolify-notify-relay/internal/httpx"
	"github.com/4shanzahid/coolify-notify-relay/internal/sink"
)

// spaceInterval is Google Chat's quota: 1 message per second per space, shared
// by every webhook in the space.
const spaceInterval = 1100 * time.Millisecond

// Sink posts to the Google Chat webhook of each job's route.
type Sink struct {
	http    *http.Client
	coolify *coolify.Client // optional enrichment
	log     *slog.Logger
	now     func() time.Time

	mu       sync.Mutex
	limiters map[string]*limiter
	interval time.Duration
}

// New returns a Google Chat sink. cc may be nil; cards then omit commit details.
func New(cc *coolify.Client, log *slog.Logger) *Sink {
	return &Sink{
		http:     &http.Client{Timeout: 15 * time.Second},
		coolify:  cc,
		log:      log,
		now:      time.Now,
		limiters: map[string]*limiter{},
		interval: spaceInterval,
	}
}

func (s *Sink) Name() string { return "googlechat" }

func (s *Sink) Send(ctx context.Context, job *sink.Job) error {
	target := job.Route.GoogleChat
	u, err := url.Parse(target.WebhookURL)
	if err != nil {
		return httpx.Permanent(err)
	}

	msg := Build(job.Event, s.details(ctx, job.Event), s.now())
	if key := threadKey(target.Thread, job.Event, s.now()); key != "" {
		msg.Thread = &Thread{ThreadKey: key}
		q := u.Query()
		q.Set("messageReplyOption", "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD")
		u.RawQuery = q.Encode()
	}

	if err := s.limiter(spaceID(u)).wait(ctx, s.interval); err != nil {
		return err
	}
	_, err = httpx.DoJSON(ctx, s.http, http.MethodPost, u.String(), nil, msg, nil)
	return redact(err, u)
}

// redact strips the webhook key/token that net/http puts in transport errors,
// so they never reach the logs.
func redact(err error, u *url.URL) error {
	if ue, ok := errors.AsType[*url.Error](err); ok {
		ue.URL = "https://chat.googleapis.com/v1/spaces/" + spaceID(u) + "/messages?REDACTED"
	}
	return err
}

// details fetches commit info for deployment events. Failures only drop the
// commit line from the card; they never block the notification.
func (s *Sink) details(ctx context.Context, e *coolify.Event) *Details {
	if s.coolify == nil || !e.IsDeployment() || e.DeploymentUUID == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	d := &Details{}
	if dep, err := s.coolify.GetDeployment(ctx, e.DeploymentUUID); err != nil {
		s.log.Warn("coolify deployment lookup failed", "deployment_uuid", e.DeploymentUUID, "err", err)
	} else {
		d.Commit, d.CommitMessage = dep.Commit, dep.CommitMessage
	}
	if e.ApplicationUUID != "" {
		if app, err := s.coolify.GetApplication(ctx, e.ApplicationUUID); err != nil {
			s.log.Warn("coolify application lookup failed", "application_uuid", e.ApplicationUUID, "err", err)
		} else {
			d.Repo, d.Branch = coolify.RepoSlug(app.GitRepository), app.GitBranch
		}
	}
	if d.Commit == "HEAD" {
		d.Commit = ""
	}
	return d
}

func threadKey(mode string, e *coolify.Event, now time.Time) string {
	scope := e.Project + "/" + e.Environment
	if e.Project == "" {
		scope = "coolify"
	}
	switch mode {
	case config.ThreadNone:
		return ""
	case config.ThreadPerDay:
		return scope + "/" + now.Format("2006-01-02")
	default: // per_app
		if e.ApplicationUUID != "" {
			return scope + "/" + e.ApplicationUUID
		}
		return scope + "/" + e.Event
	}
}

// spaceID extracts SPACE from /v1/spaces/SPACE/messages; it is the rate-limit key.
func spaceID(u *url.URL) string {
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 3 && parts[1] == "spaces" {
		return parts[2]
	}
	return u.Path
}

func (s *Sink) limiter(space string) *limiter {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.limiters[space]
	if l == nil {
		l = &limiter{}
		s.limiters[space] = l
	}
	return l
}

// limiter spaces calls at least interval apart; callers queue rather than fail.
type limiter struct {
	mu   sync.Mutex
	next time.Time
}

func (l *limiter) wait(ctx context.Context, interval time.Duration) error {
	l.mu.Lock()
	now := time.Now()
	at := l.next
	if at.Before(now) {
		at = now
	}
	l.next = at.Add(interval)
	l.mu.Unlock()

	d := time.Until(at)
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

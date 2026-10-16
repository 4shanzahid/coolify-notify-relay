// Package github records Coolify deployments as GitHub Deployments, so each
// repo's Environments panel shows what is live and links back to Coolify.
//
// Coolify emits no "deployment started" event, so a GitHub deployment is
// created and closed (success/failure) in one go when the deploy finishes.
package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/4shanzahid/coolify-notify-relay/internal/coolify"
	"github.com/4shanzahid/coolify-notify-relay/internal/httpx"
	"github.com/4shanzahid/coolify-notify-relay/internal/sink"
	"github.com/4shanzahid/coolify-notify-relay/internal/text"
	"github.com/4shanzahid/coolify-notify-relay/internal/ttl"
)

// Sink records finished Coolify deployments as GitHub Deployments.
type Sink struct {
	api     string
	http    *http.Client
	auth    TokenSource
	coolify *coolify.Client
	log     *slog.Logger

	// created remembers the GitHub deployment made for a Coolify deployment, so
	// a retry after a failed status call does not create a second one.
	created *ttl.Cache[string, int64]
}

// New returns a GitHub Deployments sink. cc is required: it resolves the repo and commit.
func New(apiBase string, auth TokenSource, cc *coolify.Client, log *slog.Logger) *Sink {
	return &Sink{
		api:     strings.TrimRight(apiBase, "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
		auth:    auth,
		coolify: cc,
		log:     log,
		created: ttl.New[string, int64](24 * time.Hour),
	}
}

func (s *Sink) Name() string { return "github" }

func (s *Sink) Send(ctx context.Context, job *sink.Job) error {
	e := job.Event
	if !e.IsDeployment() || e.ApplicationUUID == "" || e.DeploymentUUID == "" {
		return nil
	}

	app, err := s.coolify.GetApplication(ctx, e.ApplicationUUID)
	if err != nil {
		return fmt.Errorf("coolify application: %w", err)
	}
	slug := coolify.RepoSlug(app.GitRepository)
	if slug == "" {
		return httpx.Permanent(fmt.Errorf("cannot derive owner/repo from git_repository %q", app.GitRepository))
	}
	owner, repo, _ := strings.Cut(slug, "/")

	token, err := s.auth.Token(ctx, owner, repo)
	if err != nil {
		return err
	}
	h := appHeaders("Bearer " + token)

	err = s.send(ctx, job, app, owner, repo, h)
	if se, ok := errors.AsType[*httpx.StatusError](err); ok && se.Code == http.StatusUnauthorized {
		s.auth.Invalidate(owner, repo) // next attempt fetches a fresh installation token
	}
	return err
}

func (s *Sink) send(ctx context.Context, job *sink.Job, app *coolify.Application, owner, repo string, h http.Header) error {
	e := job.Event
	base := fmt.Sprintf("%s/repos/%s/%s", s.api, url.PathEscape(owner), url.PathEscape(repo))

	id, ok := s.created.Get(e.DeploymentUUID)
	if !ok {
		sha, err := s.commitSHA(ctx, e, app, base, h)
		if err != nil {
			return err
		}

		env, transient, production := s.environment(job)
		var dep struct {
			ID      int64  `json:"id"`
			Message string `json:"message"`
		}
		code, err := httpx.DoJSON(ctx, s.http, http.MethodPost, base+"/deployments", h, map[string]any{
			"ref":                    sha,
			"environment":            env,
			"auto_merge":             false,
			"required_contexts":      []string{},
			"transient_environment":  transient,
			"production_environment": production,
			"description":            text.Truncate("Coolify: "+e.ShortAppName(), 140),
			"payload": map[string]any{
				"coolify_deployment_uuid":  e.DeploymentUUID,
				"coolify_application_uuid": e.ApplicationUUID,
			},
		}, &dep)
		if err != nil {
			return classify(err)
		}
		if code != http.StatusCreated || dep.ID == 0 {
			// 202 "merged branch" cannot happen with auto_merge=false; treat anything else as fatal.
			return httpx.Permanent(fmt.Errorf("create deployment: unexpected %d %s", code, dep.Message))
		}
		id = dep.ID
		s.created.Set(e.DeploymentUUID, id)
	}

	state, desc := "success", e.Message
	if e.Event == coolify.EventDeploymentFailed {
		state = "failure"
	}
	status := map[string]any{
		"state":         state,
		"description":   text.Truncate(desc, 140),
		"auto_inactive": true,
	}
	if u := e.AppURL(); strings.HasPrefix(u, "http") && state == "success" {
		status["environment_url"] = u
	}
	if strings.HasPrefix(e.DeploymentURL, "http") {
		status["log_url"] = e.DeploymentURL
	}
	if _, err := httpx.DoJSON(ctx, s.http, http.MethodPost, fmt.Sprintf("%s/deployments/%d/statuses", base, id), h, status, nil); err != nil {
		return classify(err)
	}
	return nil
}

// commitSHA uses the commit Coolify recorded; for deploys of "HEAD" it falls
// back to the current tip of the app's branch.
func (s *Sink) commitSHA(ctx context.Context, e *coolify.Event, app *coolify.Application, base string, h http.Header) (string, error) {
	dep, err := s.coolify.GetDeployment(ctx, e.DeploymentUUID)
	if err != nil {
		return "", fmt.Errorf("coolify deployment: %w", err)
	}
	if text.IsSHA(dep.Commit) {
		return dep.Commit, nil
	}
	if app.GitBranch == "" {
		return "", httpx.Permanent(fmt.Errorf("deployment has commit %q and the app has no branch", dep.Commit))
	}
	var c struct {
		SHA string `json:"sha"`
	}
	if _, err := httpx.DoJSON(ctx, s.http, http.MethodGet, base+"/commits/"+url.PathEscape(app.GitBranch), h, nil, &c); err != nil {
		return "", classify(err)
	}
	s.log.Info("coolify commit unknown, using branch head", "deployment_uuid", e.DeploymentUUID, "branch", app.GitBranch, "sha", c.SHA)
	return c.SHA, nil
}

func (s *Sink) environment(job *sink.Job) (name string, transient, production bool) {
	e := job.Event
	name = e.Environment
	if t := job.Route.GitHub; t != nil {
		if t.Environment != "" {
			name = t.Environment
		}
		production = t.Production
	}
	if e.IsPreview() {
		return fmt.Sprintf("%s-pr-%d", name, e.PullRequestID), true, false
	}
	return name, false, production
}

// classify makes 4xx responses permanent, except the ones worth retrying:
// 401 (token refreshed on the next attempt), 403 secondary rate limits, and the
// 408/429/5xx that httpx already treats as transient.
func classify(err error) error {
	se, ok := errors.AsType[*httpx.StatusError](err)
	if !ok {
		return err
	}
	switch {
	case se.Code == http.StatusUnauthorized:
		return httpx.Transient(err)
	case se.Code == http.StatusForbidden && strings.Contains(strings.ToLower(se.Body), "rate limit"):
		return httpx.Transient(err)
	case se.Code >= 400 && se.Code < 500 && se.Code != http.StatusRequestTimeout && se.Code != http.StatusTooManyRequests:
		return httpx.Permanent(err)
	}
	return err
}

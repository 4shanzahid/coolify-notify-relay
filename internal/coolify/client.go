package coolify

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/4shanzahid/coolify-notify-relay/internal/httpx"
	"github.com/4shanzahid/coolify-notify-relay/internal/ttl"
)

// Deployment is the subset of GET /api/v1/deployments/{uuid} the relay uses.
type Deployment struct {
	DeploymentUUID string `json:"deployment_uuid"`
	Status         string `json:"status"`
	Commit         string `json:"commit"`
	CommitMessage  string `json:"commit_message"`
}

// Application is the subset of GET /api/v1/applications/{uuid} the relay uses.
type Application struct {
	UUID          string `json:"uuid"`
	GitRepository string `json:"git_repository"`
	GitBranch     string `json:"git_branch"`
	FQDN          string `json:"fqdn"`
}

// Client is a read-only Coolify API client. Responses are cached because the
// Google Chat and GitHub sinks both look up the same deployment.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
	deps    *ttl.Cache[string, *Deployment]
	apps    *ttl.Cache[string, *Application]
}

func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 10 * time.Second},
		deps:    ttl.New[string, *Deployment](30 * time.Minute),
		apps:    ttl.New[string, *Application](10 * time.Minute),
	}
}

func (c *Client) GetDeployment(ctx context.Context, uuid string) (*Deployment, error) {
	if d, ok := c.deps.Get(uuid); ok {
		return d, nil
	}
	var d Deployment
	if err := c.get(ctx, "/api/v1/deployments/"+url.PathEscape(uuid), &d); err != nil {
		return nil, err
	}
	c.deps.Set(uuid, &d)
	return &d, nil
}

func (c *Client) GetApplication(ctx context.Context, uuid string) (*Application, error) {
	if a, ok := c.apps.Get(uuid); ok {
		return a, nil
	}
	var a Application
	if err := c.get(ctx, "/api/v1/applications/"+url.PathEscape(uuid), &a); err != nil {
		return nil, err
	}
	c.apps.Set(uuid, &a)
	return &a, nil
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	h := http.Header{"Authorization": {"Bearer " + c.token}}
	_, err := httpx.DoJSON(ctx, c.http, http.MethodGet, c.baseURL+path, h, nil, out)
	return err
}

// RepoSlug turns Coolify's git_repository ("owner/repo", an https URL or an
// ssh URL) into "owner/repo". It returns "" when it cannot.
func RepoSlug(repo string) string {
	s := strings.TrimSpace(repo)
	s = strings.TrimSuffix(s, ".git")
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if j := strings.Index(s, "/"); j >= 0 {
			s = s[j+1:]
		}
	} else if strings.HasPrefix(s, "git@") {
		if j := strings.Index(s, ":"); j >= 0 {
			s = s[j+1:]
		}
	}
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

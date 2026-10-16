// Package config loads the routing config. Routing lives in a JSON file;
// secrets (webhook URLs, tokens) are referenced by env var name and resolved here.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"
)

// Config is the relay's routing config.
type Config struct {
	Coolify Coolify      `json:"coolify"`
	GitHub  GitHubGlobal `json:"github"`
	Routes  []Route      `json:"routes"`
}

// Coolify API access. Optional for Google Chat (commit details are skipped
// without it), required for the GitHub sink.
type Coolify struct {
	BaseURLEnv string `json:"base_url_env"`
	TokenEnv   string `json:"token_env"`

	BaseURL string `json:"-"`
	Token   string `json:"-"`
}

func (c Coolify) Enabled() bool { return c.BaseURL != "" && c.Token != "" }

// GitHubGlobal is the kill-switch for the GitHub Deployments sink.
type GitHubGlobal struct {
	Enabled bool `json:"enabled"`
}

// Route sends matching events to its targets.
type Route struct {
	Name       string            `json:"name"`
	Match      Match             `json:"match"`
	GoogleChat *GoogleChatTarget `json:"googlechat,omitempty"`
	GitHub     *GitHubTarget     `json:"github,omitempty"`
}

// Match fields are ANDed; an empty list matches anything; a list matches if any entry does.
type Match struct {
	Events          []string `json:"events"`
	Projects        []string `json:"projects"`
	Environments    []string `json:"environments"`
	Applications    []string `json:"applications"` // path.Match globs on the short or full app name
	IncludePreviews bool     `json:"include_previews"`
}

// Google Chat threading modes.
const (
	ThreadPerApp = "per_app"
	ThreadPerDay = "per_day"
	ThreadNone   = "none"
)

// GoogleChatTarget posts to one Chat space webhook.
type GoogleChatTarget struct {
	WebhookEnv string `json:"webhook_env"`
	Thread     string `json:"thread"`

	WebhookURL string `json:"-"`
}

// GitHubTarget records deployments in GitHub; also gated by GitHubGlobal.
type GitHubTarget struct {
	Enabled     bool   `json:"enabled"`
	Environment string `json:"environment"` // GitHub environment name; defaults to the Coolify environment
	Production  bool   `json:"production"`
}

// GitHubActive reports whether the route should post to GitHub given the global switch.
func (r *Route) GitHubActive(global GitHubGlobal) bool {
	return global.Enabled && r.GitHub != nil && r.GitHub.Enabled
}

// Load reads and validates the config at path, resolving *_env references with getenv.
// It returns non-fatal warnings alongside the config.
func Load(p string, getenv func(string) string) (*Config, []string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, nil, err
	}
	return Parse(b, getenv)
}

// Parse is Load for an in-memory config.
func Parse(b []byte, getenv func(string) string) (*Config, []string, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, nil, fmt.Errorf("parse config: %w", err)
	}
	if dec.More() {
		return nil, nil, errors.New("parse config: unexpected data after the top-level object")
	}
	warnings, err := c.resolve(getenv)
	if err != nil {
		return nil, nil, err
	}
	return &c, warnings, nil
}

func (c *Config) resolve(getenv func(string) string) ([]string, error) {
	var errs []error
	var warnings []string

	if c.Coolify.BaseURLEnv != "" || c.Coolify.TokenEnv != "" {
		c.Coolify.BaseURL = getenv(c.Coolify.BaseURLEnv)
		c.Coolify.Token = getenv(c.Coolify.TokenEnv)
		if c.Coolify.BaseURL == "" || c.Coolify.Token == "" {
			warnings = append(warnings, fmt.Sprintf("coolify: %s/%s not set; commit details and the GitHub sink are unavailable",
				c.Coolify.BaseURLEnv, c.Coolify.TokenEnv))
			c.Coolify.BaseURL, c.Coolify.Token = "", ""
		}
	}

	if len(c.Routes) == 0 {
		errs = append(errs, errors.New("no routes configured"))
	}
	seen := map[string]bool{}
	for i := range c.Routes {
		r := &c.Routes[i]
		label := fmt.Sprintf("route[%d] %q", i, r.Name)
		if r.Name == "" {
			errs = append(errs, fmt.Errorf("%s: name is required", label))
		} else if seen[r.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate name", label))
		}
		seen[r.Name] = true

		for _, g := range r.Match.Applications {
			if _, err := path.Match(g, ""); err != nil {
				errs = append(errs, fmt.Errorf("%s: bad application glob %q: %w", label, g, err))
			}
		}
		if r.GoogleChat == nil && r.GitHub == nil {
			errs = append(errs, fmt.Errorf("%s: needs a googlechat or github target", label))
		}

		if gc := r.GoogleChat; gc != nil {
			switch gc.Thread {
			case "":
				gc.Thread = ThreadPerApp
			case ThreadPerApp, ThreadPerDay, ThreadNone:
			default:
				errs = append(errs, fmt.Errorf("%s: thread must be per_app, per_day or none", label))
			}
			if gc.WebhookEnv == "" {
				errs = append(errs, fmt.Errorf("%s: googlechat.webhook_env is required", label))
			} else if gc.WebhookURL = getenv(gc.WebhookEnv); gc.WebhookURL == "" {
				errs = append(errs, fmt.Errorf("%s: env %s is not set", label, gc.WebhookEnv))
			} else if err := validateChatURL(gc.WebhookURL); err != nil {
				errs = append(errs, fmt.Errorf("%s: env %s: %w", label, gc.WebhookEnv, err))
			}
		}

		if gh := r.GitHub; gh != nil && gh.Enabled {
			if !c.GitHub.Enabled {
				warnings = append(warnings, fmt.Sprintf("%s: github.enabled is true but the global github switch is off", label))
			} else if !c.Coolify.Enabled() {
				errs = append(errs, fmt.Errorf("%s: the github target needs the coolify API (base_url_env/token_env)", label))
			}
		}
	}
	return warnings, errors.Join(errs...)
}

// validateChatURL checks the shape without echoing the secret URL in errors.
func validateChatURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "chat.googleapis.com" || !strings.HasPrefix(u.Path, "/v1/spaces/") {
		return errors.New("not a https://chat.googleapis.com/v1/spaces/... webhook URL")
	}
	if u.Query().Get("key") == "" || u.Query().Get("token") == "" {
		return errors.New("webhook URL is missing key or token")
	}
	return nil
}

// AnyGitHub reports whether any route will post to GitHub.
func (c *Config) AnyGitHub() bool {
	for i := range c.Routes {
		if c.Routes[i].GitHubActive(c.GitHub) {
			return true
		}
	}
	return false
}

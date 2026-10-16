// Package router decides which configured routes an event goes to.
package router

import (
	"path"
	"strings"

	"github.com/4shanzahid/coolify-notify-relay/internal/config"
	"github.com/4shanzahid/coolify-notify-relay/internal/coolify"
)

type Router struct {
	routes []*config.Route
}

func New(routes []config.Route) *Router {
	r := &Router{}
	for i := range routes {
		r.routes = append(r.routes, &routes[i])
	}
	return r
}

// Match returns every route the event matches. Coolify's "Send test
// notification" event goes to each distinct Google Chat webhook once, so the
// test button verifies the whole path.
func (r *Router) Match(e *coolify.Event) []*config.Route {
	var out []*config.Route
	if e.Event == coolify.EventTest {
		seen := map[string]bool{}
		for _, rt := range r.routes {
			if rt.GoogleChat == nil || seen[rt.GoogleChat.WebhookURL] {
				continue
			}
			seen[rt.GoogleChat.WebhookURL] = true
			out = append(out, rt)
		}
		return out
	}
	for _, rt := range r.routes {
		if matches(&rt.Match, e) {
			out = append(out, rt)
		}
	}
	return out
}

func matches(m *config.Match, e *coolify.Event) bool {
	if e.IsPreview() && !m.IncludePreviews {
		return false
	}
	if !anyEqual(m.Events, e.Event) || !anyEqual(m.Projects, e.Project) || !anyEqual(m.Environments, e.Environment) {
		return false
	}
	if len(m.Applications) > 0 {
		short, full := e.ShortAppName(), e.ApplicationName
		if full == "" {
			return false
		}
		ok := false
		for _, g := range m.Applications {
			if a, _ := path.Match(g, short); a {
				ok = true
				break
			}
			if b, _ := path.Match(g, full); b {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

func anyEqual(list []string, v string) bool {
	if len(list) == 0 {
		return true
	}
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

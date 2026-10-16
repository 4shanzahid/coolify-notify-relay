package router

import (
	"os"
	"testing"

	"github.com/4shanzahid/coolify-notify-relay/internal/config"
	"github.com/4shanzahid/coolify-notify-relay/internal/coolify"
)

func event(t *testing.T, name string) *coolify.Event {
	t.Helper()
	b, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	e, err := coolify.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func names(rs []*config.Route) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.Name)
	}
	return out
}

func TestRepoConfigRouting(t *testing.T) {
	cfg, _, err := config.Load("../../config.json", func(string) string {
		return "https://chat.googleapis.com/v1/spaces/S/messages?key=k&token=t"
	})
	if err != nil {
		t.Fatal(err)
	}
	r := New(cfg.Routes)

	cases := []struct {
		fixture string
		want    int
	}{
		{"deployment_success.json", 1},               // ERP backend dev
		{"deployment_failed.json", 1},                // ERP web dev, failures go to the same space
		{"deployment_success_preview.json", 0},       // previews excluded
		{"deployment_success_other_project.json", 0}, // another project
		{"server_unreachable.json", 0},               // not routed yet
		{"test.json", 1},                             // test button reaches the space
	}
	for _, c := range cases {
		if got := r.Match(event(t, c.fixture)); len(got) != c.want {
			t.Errorf("%s: matched %v, want %d", c.fixture, names(got), c.want)
		}
	}
}

func TestMatchRules(t *testing.T) {
	gc := func(url string) *config.GoogleChatTarget { return &config.GoogleChatTarget{WebhookURL: url} }
	routes := []config.Route{
		{Name: "glob", Match: config.Match{Applications: []string{"shop-*"}}, GoogleChat: gc("A")},
		{Name: "full-name", Match: config.Match{Applications: []string{"*:dev-*"}}, GoogleChat: gc("A")},
		{Name: "prod-only", Match: config.Match{Environments: []string{"production"}}, GoogleChat: gc("B")},
		{Name: "failures", Match: config.Match{Events: []string{"deployment_failed"}}, GoogleChat: gc("B")},
		{Name: "previews", Match: config.Match{IncludePreviews: true, Projects: []string{"SHOP"}}},
	}
	r := New(routes)

	cases := map[string][]string{
		"deployment_success.json":         {"glob", "full-name", "previews"}, // include_previews adds previews, it doesn't restrict to them
		"deployment_failed.json":          {"glob", "full-name", "failures", "previews"},
		"deployment_success_preview.json": {"previews"},
		"server_unreachable.json":         nil,
		"test.json":                       {"glob", "prod-only"}, // one per distinct webhook
	}
	for fixture, want := range cases {
		got := names(r.Match(event(t, fixture)))
		if len(got) != len(want) {
			t.Errorf("%s: got %v want %v", fixture, got, want)
			continue
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: got %v want %v", fixture, got, want)
				break
			}
		}
	}
}

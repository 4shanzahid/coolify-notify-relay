package coolify

import (
	"os"
	"testing"
)

func load(t *testing.T, name string) *Event {
	t.Helper()
	b, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	e, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestParseDeployment(t *testing.T) {
	e := load(t, "deployment_success.json")
	if !e.IsDeployment() || e.IsPreview() {
		t.Fatalf("IsDeployment=%v IsPreview=%v", e.IsDeployment(), e.IsPreview())
	}
	if got := e.ShortAppName(); got != "shop-api" {
		t.Errorf("ShortAppName = %q", got)
	}
	if got := e.DedupeKey(); got != "deployment_success|d1e2p3l4o5y6u7u8i9d0x1y2" {
		t.Errorf("DedupeKey = %q", got)
	}
	if e.AppURL() != "https://dev-api.shop.example.com" {
		t.Errorf("AppURL = %q", e.AppURL())
	}
}

func TestParsePreview(t *testing.T) {
	e := load(t, "deployment_success_preview.json")
	if !e.IsPreview() || e.PullRequestID != 31 {
		t.Fatalf("PullRequestID = %d", e.PullRequestID)
	}
	if e.AppURL() != "https://31.dev.shop.example.com" {
		t.Errorf("AppURL = %q", e.AppURL())
	}
}

func TestParseNonDeploymentDedupesByBody(t *testing.T) {
	a := load(t, "server_unreachable.json")
	b := load(t, "server_unreachable.json")
	if a.DedupeKey() != b.DedupeKey() || a.DedupeKey() == "" {
		t.Errorf("keys %q %q", a.DedupeKey(), b.DedupeKey())
	}
	if a.String("server_name") != "localhost" {
		t.Errorf("String(server_name) = %q", a.String("server_name"))
	}
}

func TestParseErrors(t *testing.T) {
	for _, in := range []string{`{}`, `not json`, `{"event":"x","pull_request_id":"abc"}`} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%s) = nil error", in)
		}
	}
	e, err := Parse([]byte(`{"event":"deployment_success","pull_request_id":"7"}`))
	if err != nil || e.PullRequestID != 7 {
		t.Errorf("string pull_request_id: %v %d", err, e.PullRequestID)
	}
}

func TestRepoSlug(t *testing.T) {
	cases := map[string]string{
		"acme/shop-api":                        "acme/shop-api",
		"https://github.com/acme/shop-web.git": "acme/shop-web",
		"git@github.com:acme/repo.git":         "acme/repo",
		"just-a-name":                          "",
		"":                                     "",
	}
	for in, want := range cases {
		if got := RepoSlug(in); got != want {
			t.Errorf("RepoSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

package config

import (
	"strings"
	"testing"
)

const chatURL = "https://chat.googleapis.com/v1/spaces/AAAA/messages?key=k&token=t"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadRepoConfig(t *testing.T) {
	c, warnings, err := Load("../../config.json", env(map[string]string{
		"GCHAT_DEPLOYMENTS": chatURL,
		"COOLIFY_API_URL":   "http://coolify:8080",
		"COOLIFY_API_TOKEN": "tok",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: %v", warnings)
	}
	if !c.Coolify.Enabled() || c.AnyGitHub() {
		t.Errorf("coolify=%v github=%v", c.Coolify.Enabled(), c.AnyGitHub())
	}
	if got := c.Routes[0].GoogleChat.WebhookURL; got != chatURL {
		t.Errorf("webhook url not resolved: %q", got)
	}
}

func TestLoadRepoConfigWithoutCoolifyAPI(t *testing.T) {
	c, warnings, err := Load("../../config.json", env(map[string]string{"GCHAT_DEPLOYMENTS": chatURL}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Coolify.Enabled() || len(warnings) != 1 {
		t.Errorf("coolify=%v warnings=%v", c.Coolify.Enabled(), warnings)
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"missing env":          `{"routes":[{"name":"a","googlechat":{"webhook_env":"NOPE"}}]}`,
		"unknown field":        `{"routes":[{"name":"a","googlechat":{"webhook_env":"G"},"typo":1}]}`,
		"no target":            `{"routes":[{"name":"a"}]}`,
		"no routes":            `{"routes":[]}`,
		"dup name":             `{"routes":[{"name":"a","googlechat":{"webhook_env":"G"}},{"name":"a","googlechat":{"webhook_env":"G"}}]}`,
		"bad glob":             `{"routes":[{"name":"a","match":{"applications":["["]},"googlechat":{"webhook_env":"G"}}]}`,
		"bad thread":           `{"routes":[{"name":"a","googlechat":{"webhook_env":"G","thread":"weekly"}}]}`,
		"bad chat url":         `{"routes":[{"name":"a","googlechat":{"webhook_env":"BAD"}}]}`,
		"trailing data":        `{"routes":[{"name":"a","googlechat":{"webhook_env":"G"}}]} {}`,
		"github needs coolify": `{"github":{"enabled":true},"routes":[{"name":"a","github":{"enabled":true}}]}`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := Parse([]byte(in), env(map[string]string{"G": chatURL, "BAD": "https://example.com/hook"}))
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "token=") {
				t.Errorf("error leaks webhook secret: %v", err)
			}
		})
	}
}

func TestGitHubRouteWithGlobalOffWarns(t *testing.T) {
	c, warnings, err := Parse([]byte(`{"routes":[{"name":"a","github":{"enabled":true}}]}`), env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.AnyGitHub() || len(warnings) != 1 {
		t.Errorf("AnyGitHub=%v warnings=%v", c.AnyGitHub(), warnings)
	}
}

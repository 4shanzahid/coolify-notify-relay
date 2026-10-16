package googlechat

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/4shanzahid/coolify-notify-relay/internal/config"
	"github.com/4shanzahid/coolify-notify-relay/internal/coolify"
	"github.com/4shanzahid/coolify-notify-relay/internal/httpx"
	"github.com/4shanzahid/coolify-notify-relay/internal/sink"
)

var fixedNow = time.Date(2026, 10, 16, 6, 3, 15, 0, time.UTC)

func event(t *testing.T, name string) *coolify.Event {
	t.Helper()
	b, err := os.ReadFile("../../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	e, err := coolify.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestBuildDeploymentSuccess(t *testing.T) {
	d := &Details{
		Commit:        "3f2a9c1e7b4d8a6f0c5e2b9d1a7f4c8e6b3d0a5f",
		CommitMessage: "Merge pull request #30 from acme/feature-checkout\n\nAdd checkout flow: <fix>",
		Repo:          "acme/shop-api",
		Branch:        "dev",
	}
	m := Build(event(t, "deployment_success.json"), d, fixedNow)

	if m.Text != "✅ shop-api deployed (dev) @ 3f2a9c1" {
		t.Errorf("text = %q", m.Text)
	}
	card := m.CardsV2[0].Card
	if card.Header.Title != "✅ shop-api" || card.Header.Subtitle != "shop · dev" {
		t.Errorf("header = %+v", card.Header)
	}
	b, _ := json.Marshal(m)
	s := string(b)
	for _, want := range []string{
		`https://github.com/acme/shop-api/commit/3f2a9c1e7b4d8a6f0c5e2b9d1a7f4c8e6b3d0a5f`,
		`Merge pull request #30 from acme/feature-checkout`,
		`"url":"https://dev-api.shop.example.com"`,
		`Deployment logs`,
		`Succeeded`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("message missing %q\n%s", want, s)
		}
	}
	if strings.Contains(s, "<fix>") {
		t.Error("commit message body should not be included")
	}
}

func TestBuildDeploymentFailedWithoutDetails(t *testing.T) {
	m := Build(event(t, "deployment_failed.json"), nil, fixedNow)
	if m.Text != "❌ shop-web deployment failed (dev)" {
		t.Errorf("text = %q", m.Text)
	}
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), "Failed") || strings.Contains(string(b), "Commit") {
		t.Errorf("unexpected card: %s", b)
	}
}

func TestBuildEscapesHTML(t *testing.T) {
	d := &Details{Commit: "3f2a9c1e7b4d8a6f0c5e2b9d1a7f4c8e6b3d0a5f", CommitMessage: `<script>alert(1)</script>`}
	b, _ := json.Marshal(Build(event(t, "deployment_success.json"), d, fixedNow))
	if strings.Contains(string(b), "<script>") {
		t.Errorf("unescaped HTML: %s", b)
	}
}

func TestBuildGeneric(t *testing.T) {
	m := Build(event(t, "server_unreachable.json"), nil, fixedNow)
	if m.Text != "⚠️ Server unreachable" {
		t.Errorf("text = %q", m.Text)
	}
	b, _ := json.Marshal(m)
	if !strings.Contains(string(b), `"text":"localhost"`) || !strings.Contains(string(b), "Open in Coolify") {
		t.Errorf("card: %s", b)
	}
	if got := Build(event(t, "test.json"), nil, fixedNow).Text; !strings.HasPrefix(got, "🔔 ") {
		t.Errorf("test text = %q", got)
	}
}

func TestThreadKey(t *testing.T) {
	e := event(t, "deployment_success.json")
	if k := threadKey(config.ThreadPerApp, e, fixedNow); k != "shop/dev/a1b2c3d4e5f6a7b8c9d0e1f2" {
		t.Errorf("per_app = %q", k)
	}
	if k := threadKey(config.ThreadPerDay, e, fixedNow); k != "shop/dev/2026-10-16" {
		t.Errorf("per_day = %q", k)
	}
	if k := threadKey(config.ThreadNone, e, fixedNow); k != "" {
		t.Errorf("none = %q", k)
	}
}

func TestSendPostsToWebhookWithThreadAndEnrichment(t *testing.T) {
	coolifyAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer ctok" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/deployments/d1e2p3l4o5y6u7u8i9d0x1y2":
			io.WriteString(w, `{"commit":"3f2a9c1e7b4d8a6f0c5e2b9d1a7f4c8e6b3d0a5f","commit_message":"Merge PR #30","logs":"[...]"}`)
		case "/api/v1/applications/a1b2c3d4e5f6a7b8c9d0e1f2":
			io.WriteString(w, `{"git_repository":"acme/shop-api","git_branch":"dev"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer coolifyAPI.Close()

	var got Message
	var query string
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Error(err)
		}
		io.WriteString(w, `{"name":"spaces/S/messages/1"}`)
	}))
	defer chat.Close()

	s := New(coolify.NewClient(coolifyAPI.URL, "ctok"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.now = func() time.Time { return fixedNow }
	route := &config.Route{Name: "r", GoogleChat: &config.GoogleChatTarget{
		WebhookURL: chat.URL + "/v1/spaces/S/messages?key=k&token=t", Thread: config.ThreadPerApp,
	}}
	if err := s.Send(context.Background(), &sink.Job{Event: event(t, "deployment_success.json"), Route: route}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "messageReplyOption=REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD") || !strings.Contains(query, "token=t") {
		t.Errorf("query = %q", query)
	}
	if got.Thread == nil || got.Thread.ThreadKey != "shop/dev/a1b2c3d4e5f6a7b8c9d0e1f2" {
		t.Errorf("thread = %+v", got.Thread)
	}
	if !strings.HasSuffix(got.Text, "@ 3f2a9c1") {
		t.Errorf("text = %q", got.Text)
	}
}

func TestSendClassifiesErrors(t *testing.T) {
	status := http.StatusTooManyRequests
	chat := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"code":429}}`, status)
	}))
	defer chat.Close()

	s := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.interval = 0
	route := &config.Route{GoogleChat: &config.GoogleChatTarget{WebhookURL: chat.URL + "/v1/spaces/S/messages?key=k&token=secret"}}
	job := &sink.Job{Event: event(t, "test.json"), Route: route}

	err := s.Send(context.Background(), job)
	if !httpx.Retryable(err) {
		t.Errorf("429 should be retryable: %v", err)
	}
	status = http.StatusBadRequest
	if err := s.Send(context.Background(), job); httpx.Retryable(err) {
		t.Errorf("400 should be permanent: %v", err)
	}

	chat.Close()
	err = s.Send(context.Background(), job)
	if err == nil || strings.Contains(err.Error(), "secret") {
		t.Errorf("transport error should be redacted: %v", err)
	}
}

func TestLimiterSpacesCalls(t *testing.T) {
	var l limiter
	start := time.Now()
	for range 3 {
		if err := l.wait(context.Background(), 50*time.Millisecond); err != nil {
			t.Fatal(err)
		}
	}
	if el := time.Since(start); el < 100*time.Millisecond {
		t.Errorf("3 calls took %v, want ≥100ms", el)
	}
}

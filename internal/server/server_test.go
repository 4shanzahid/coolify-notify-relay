package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/4shanzahid/coolify-notify-relay/internal/config"
	"github.com/4shanzahid/coolify-notify-relay/internal/metrics"
	"github.com/4shanzahid/coolify-notify-relay/internal/queue"
	"github.com/4shanzahid/coolify-notify-relay/internal/router"
	"github.com/4shanzahid/coolify-notify-relay/internal/sink"
)

const secret = "0123456789abcdef0123456789abcdef"

type recorder struct{ tasks []queue.Task }

func (r *recorder) Enqueue(t queue.Task) error { r.tasks = append(r.tasks, t); return nil }

type named string

func (n named) Name() string                          { return string(n) }
func (n named) Send(context.Context, *sink.Job) error { return nil }

func setup(t *testing.T, githubOn bool) (http.Handler, *recorder) {
	t.Helper()
	cfg, _, err := config.Load("../../config.json", func(k string) string {
		switch k {
		case "GCHAT_DEPLOYMENTS":
			return "https://chat.googleapis.com/v1/spaces/S/messages?key=k&token=t"
		case "COOLIFY_API_URL":
			return "http://coolify:8080"
		case "COOLIFY_API_TOKEN":
			return "tok"
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if githubOn {
		cfg.GitHub.Enabled = true
		cfg.Routes[0].GitHub.Enabled = true
	}
	rec := &recorder{}
	s := New(Options{
		Secret: secret, Config: cfg, Router: router.New(cfg.Routes), Queue: rec,
		GoogleChat: named("googlechat"), GitHub: named("github"),
		Metrics: metrics.New(), Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	return s, rec
}

func post(t *testing.T, h http.Handler, path, fixture string) *httptest.ResponseRecorder {
	t.Helper()
	b, err := os.ReadFile("../../testdata/" + fixture)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(b))))
	return w
}

func TestHook(t *testing.T) {
	h, rec := setup(t, false)
	ok := "/hooks/coolify/" + secret

	if w := post(t, h, "/hooks/coolify/wrong", "deployment_success.json"); w.Code != http.StatusNotFound {
		t.Errorf("wrong secret: %d", w.Code)
	}
	if w := post(t, h, ok, "deployment_success.json"); w.Code != http.StatusAccepted || !strings.Contains(w.Body.String(), `"jobs":1`) {
		t.Errorf("success: %d %s", w.Code, w.Body)
	}
	if w := post(t, h, ok, "deployment_success.json"); !strings.Contains(w.Body.String(), "duplicate") {
		t.Errorf("duplicate: %s", w.Body)
	}
	if w := post(t, h, ok, "deployment_success_other_project.json"); !strings.Contains(w.Body.String(), "ignored") {
		t.Errorf("other project: %s", w.Body)
	}
	if len(rec.tasks) != 1 || rec.tasks[0].Sink.Name() != "googlechat" {
		t.Errorf("tasks = %+v", rec.tasks)
	}
}

func TestHookGitHubEnabled(t *testing.T) {
	h, rec := setup(t, true)
	post(t, h, "/hooks/coolify/"+secret, "deployment_failed.json")
	post(t, h, "/hooks/coolify/"+secret, "test.json")
	var sinks []string
	for _, tk := range rec.tasks {
		sinks = append(sinks, tk.Sink.Name())
	}
	// deploy → chat + github; test → chat only
	if strings.Join(sinks, ",") != "googlechat,github,googlechat" {
		t.Errorf("sinks = %v", sinks)
	}
}

func TestHookRejectsBadInput(t *testing.T) {
	h, _ := setup(t, false)
	path := "/hooks/coolify/" + secret

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader("not json")))
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad json: %d", w.Code)
	}

	w = httptest.NewRecorder()
	big := `{"event":"x","pad":"` + strings.Repeat("a", maxBody) + `"}`
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(big)))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("big body: %d", w.Code)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", w.Code)
	}
}

func TestHealthAndMetrics(t *testing.T) {
	h, _ := setup(t, false)
	post(t, h, "/hooks/coolify/"+secret, "deployment_success.json")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != 200 {
		t.Errorf("healthz: %d", w.Code)
	}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(w.Body.String(), `relay_events_total{event="deployment_success",matched="true"} 1`) {
		t.Errorf("metrics:\n%s", w.Body)
	}
}

func TestUnknownEventsShareOneMetricLabel(t *testing.T) {
	h, _ := setup(t, false)
	for _, ev := range []string{"made_up_1", "made_up_2"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/hooks/coolify/"+secret, strings.NewReader(`{"event":"`+ev+`"}`)))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(w.Body.String(), `relay_events_total{event="other",matched="false"} 2`) || strings.Contains(w.Body.String(), "made_up") {
		t.Errorf("metrics:\n%s", w.Body)
	}
}

// Package server is the relay's HTTP surface: the Coolify webhook endpoint,
// health check and metrics.
package server

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/4shanzahid/coolify-notify-relay/internal/config"
	"github.com/4shanzahid/coolify-notify-relay/internal/coolify"
	"github.com/4shanzahid/coolify-notify-relay/internal/metrics"
	"github.com/4shanzahid/coolify-notify-relay/internal/queue"
	"github.com/4shanzahid/coolify-notify-relay/internal/router"
	"github.com/4shanzahid/coolify-notify-relay/internal/sink"
	"github.com/4shanzahid/coolify-notify-relay/internal/ttl"
)

const maxBody = 64 << 10

// Enqueuer is satisfied by *queue.Queue.
type Enqueuer interface {
	Enqueue(queue.Task) error
}

// Options configures a Server.
type Options struct {
	Secret     string
	Config     *config.Config
	Router     *router.Router
	Queue      Enqueuer
	GoogleChat sink.Sink // nil disables
	GitHub     sink.Sink // nil disables
	Metrics    *metrics.Registry
	Log        *slog.Logger
}

// Server is the relay's http.Handler.
type Server struct {
	Options
	dedupe *ttl.Cache[string, struct{}]
	mux    *http.ServeMux
}

// New builds a Server; it serves the webhook, /healthz and /metrics.
func New(o Options) *Server {
	s := &Server{Options: o, dedupe: ttl.New[string, struct{}](15 * time.Minute), mux: http.NewServeMux()}
	s.Metrics.Describe("relay_events_total", "Coolify events received, by event and whether they matched a route.")
	s.Metrics.Describe("relay_deliveries_total", "Delivery outcomes per sink.")
	s.Metrics.Describe("relay_retries_total", "Delivery retries per sink.")

	s.mux.HandleFunc("POST /hooks/coolify/{secret}", s.hook)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok\n")
	})
	s.mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		s.Metrics.Write(w)
	})
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// hook accepts a Coolify notification. Coolify never retries on an error
// status, only on connection failures, so every valid event gets a 202 and the
// queue owns delivery from then on.
func (s *Server) hook(w http.ResponseWriter, r *http.Request) {
	if subtle.ConstantTimeCompare([]byte(r.PathValue("secret")), []byte(s.Secret)) != 1 {
		s.Metrics.Inc("relay_rejected_total", "reason", "secret")
		http.NotFound(w, r)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			s.Metrics.Inc("relay_rejected_total", "reason", "too_large")
			http.Error(w, "payload too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	e, err := coolify.Parse(body)
	if err != nil {
		s.Metrics.Inc("relay_rejected_total", "reason", "bad_json")
		s.Log.Warn("rejected webhook", "err", err)
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	log := s.Log.With("event", e.Event, "project", e.Project, "environment", e.Environment,
		"app", e.ShortAppName(), "deployment_uuid", e.DeploymentUUID)

	if !s.dedupe.Add(e.DedupeKey(), struct{}{}) {
		s.Metrics.Inc("relay_events_total", "event", coolify.MetricLabel(e.Event), "matched", "duplicate")
		log.Info("duplicate event ignored")
		writeJSON(w, map[string]any{"status": "duplicate"})
		return
	}

	routes := s.Router.Match(e)
	if len(routes) == 0 {
		s.Metrics.Inc("relay_events_total", "event", coolify.MetricLabel(e.Event), "matched", "false")
		log.Debug("no route matched")
		writeJSON(w, map[string]any{"status": "ignored"})
		return
	}
	s.Metrics.Inc("relay_events_total", "event", coolify.MetricLabel(e.Event), "matched", "true")

	queued := 0
	for _, rt := range routes {
		job := &sink.Job{Event: e, Route: rt}
		if rt.GoogleChat != nil && s.GoogleChat != nil {
			queued += s.enqueue(log, queue.Task{Sink: s.GoogleChat, Job: job})
		}
		if rt.GitHubActive(s.Config.GitHub) && s.GitHub != nil && e.Event != coolify.EventTest {
			queued += s.enqueue(log, queue.Task{Sink: s.GitHub, Job: job})
		}
	}
	log.Info("event accepted", "routes", len(routes), "jobs", queued)
	writeJSON(w, map[string]any{"status": "accepted", "jobs": queued})
}

func (s *Server) enqueue(log *slog.Logger, t queue.Task) int {
	if err := s.Queue.Enqueue(t); err != nil {
		s.Metrics.Inc("relay_deliveries_total", "sink", t.Sink.Name(), "result", "dropped")
		log.Error("job dropped", "sink", t.Sink.Name(), "route", t.Job.Route.Name, "err", err)
		return 0
	}
	return 1
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(v)
}

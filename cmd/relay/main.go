// Command relay receives Coolify webhook notifications and forwards them to
// Google Chat spaces and (optionally) GitHub Deployments, per config.json.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
	_ "time/tzdata" // distroless has no zoneinfo; lets TZ=Asia/Karachi work

	"github.com/4shanzahid/coolify-notify-relay/internal/config"
	"github.com/4shanzahid/coolify-notify-relay/internal/coolify"
	"github.com/4shanzahid/coolify-notify-relay/internal/metrics"
	"github.com/4shanzahid/coolify-notify-relay/internal/queue"
	"github.com/4shanzahid/coolify-notify-relay/internal/router"
	"github.com/4shanzahid/coolify-notify-relay/internal/server"
	"github.com/4shanzahid/coolify-notify-relay/internal/sink/github"
	"github.com/4shanzahid/coolify-notify-relay/internal/sink/googlechat"
)

func main() {
	// "relay healthcheck" is the Dockerfile HEALTHCHECK: the distroless image
	// has no shell, curl or wget for Coolify's default check.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}

	var level slog.Level // zero value is Info; UnmarshalText accepts DEBUG, warn, ERROR+2, ...
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		if err := level.UnmarshalText([]byte(v)); err != nil {
			fmt.Fprintf(os.Stderr, "invalid LOG_LEVEL %q: %v\n", v, err)
			os.Exit(2)
		}
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	if err := run(log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	secret := os.Getenv("RELAY_SECRET")
	if len(secret) < 32 {
		return errors.New("RELAY_SECRET must be at least 32 characters")
	}

	cfgPath := envOr("CONFIG_PATH", "/config/config.json")
	cfg, warnings, err := config.Load(cfgPath, os.Getenv)
	if err != nil {
		return fmt.Errorf("config %s: %w", cfgPath, err)
	}
	for _, w := range warnings {
		log.Warn("config", "warning", w)
	}

	var cc *coolify.Client
	if cfg.Coolify.Enabled() {
		cc = coolify.NewClient(cfg.Coolify.BaseURL, cfg.Coolify.Token)
	}

	reg := metrics.New()
	q := queue.New(queue.Options{
		Workers: envInt("WORKERS", 4),
		Size:    envInt("QUEUE_SIZE", 256),
	}, log, reg)

	opts := server.Options{
		Secret:     secret,
		Config:     cfg,
		Router:     router.New(cfg.Routes),
		Queue:      q,
		GoogleChat: googlechat.New(cc, log),
		Metrics:    reg,
		Log:        log,
	}
	if cfg.AnyGitHub() {
		auth, err := githubAuth()
		if err != nil {
			return err
		}
		opts.GitHub = github.New(envOr("GITHUB_API_URL", "https://api.github.com"), auth, cc, log)
	}

	q.Start()
	httpSrv := &http.Server{
		Addr:              ":" + envOr("PORT", "8080"),
		Handler:           server.New(opts),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.ListenAndServe() }()
	log.Info("relay started", "addr", httpSrv.Addr, "routes", len(cfg.Routes),
		"coolify_api", cc != nil, "github", opts.GitHub != nil)
	// One line per route (no secrets) so the logs show which config is live.
	for i := range cfg.Routes {
		r := &cfg.Routes[i]
		attrs := []any{"name", r.Name, "events", r.Match.Events, "projects", r.Match.Projects,
			"environments", r.Match.Environments, "applications", r.Match.Applications,
			"previews", r.Match.IncludePreviews, "github", r.GitHubActive(cfg.GitHub)}
		if r.GoogleChat != nil {
			attrs = append(attrs, "googlechat_env", r.GoogleChat.WebhookEnv, "thread", r.GoogleChat.Thread)
		}
		log.Info("route loaded", attrs...)
	}

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	stop() // restore default signal handling: a second SIGTERM/SIGINT kills immediately

	log.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(sctx)

	qctx, qcancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer qcancel()
	if err := q.Shutdown(qctx); err != nil {
		log.Warn("queue not fully drained", "err", err)
	}
	return nil
}

// githubAuth prefers a GitHub App (GITHUB_APP_ID + GITHUB_APP_PRIVATE_KEY or
// GITHUB_APP_PRIVATE_KEY_FILE) and falls back to GITHUB_TOKEN.
func githubAuth() (github.TokenSource, error) {
	api := envOr("GITHUB_API_URL", "https://api.github.com")
	if id := os.Getenv("GITHUB_APP_ID"); id != "" {
		key := []byte(os.Getenv("GITHUB_APP_PRIVATE_KEY"))
		if f := os.Getenv("GITHUB_APP_PRIVATE_KEY_FILE"); f != "" {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, fmt.Errorf("read GITHUB_APP_PRIVATE_KEY_FILE: %w", err)
			}
			key = b
		}
		return github.NewAppAuth(id, key, api)
	}
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		return github.StaticToken(t), nil
	}
	return nil, errors.New("a route enables github but neither GITHUB_APP_ID nor GITHUB_TOKEN is set")
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(k)); err == nil && n > 0 {
		return n
	}
	return def
}

func healthcheck() int {
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + envOr("PORT", "8080") + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthz:", resp.Status)
		return 1
	}
	return 0
}

package github

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/4shanzahid/coolify-notify-relay/internal/config"
	"github.com/4shanzahid/coolify-notify-relay/internal/coolify"
	"github.com/4shanzahid/coolify-notify-relay/internal/httpx"
	"github.com/4shanzahid/coolify-notify-relay/internal/sink"
)

const sha = "3f2a9c1e7b4d8a6f0c5e2b9d1a7f4c8e6b3d0a5f"

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

func fakeCoolify(t *testing.T, commit string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v1/deployments/"):
			fmt.Fprintf(w, `{"commit":%q}`, commit)
		case strings.HasPrefix(r.URL.Path, "/api/v1/applications/"):
			io.WriteString(w, `{"git_repository":"acme/shop-api","git_branch":"dev"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type ghRecorder struct {
	mu          sync.Mutex
	deployments []map[string]any
	statuses    []map[string]any
	failStatusN int
	statusCalls int
}

func (g *ghRecorder) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		base := "/repos/acme/shop-api"
		switch {
		case r.Method == http.MethodGet && r.URL.Path == base+"/commits/dev":
			fmt.Fprintf(w, `{"sha":%q}`, sha)
		case r.Method == http.MethodPost && r.URL.Path == base+"/deployments":
			g.deployments = append(g.deployments, body)
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":42}`)
		case r.Method == http.MethodPost && r.URL.Path == base+"/deployments/42/statuses":
			g.statusCalls++
			if g.statusCalls <= g.failStatusN {
				http.Error(w, "boom", http.StatusBadGateway)
				return
			}
			g.statuses = append(g.statuses, body)
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}
}

func newSink(t *testing.T, ghURL, coolifyURL string) *Sink {
	return New(ghURL, StaticToken("pat"), coolify.NewClient(coolifyURL, "ctok"), slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func route() *config.Route {
	return &config.Route{Name: "r", GitHub: &config.GitHubTarget{Enabled: true, Environment: "dev"}}
}

func TestCreatesDeploymentAndStatus(t *testing.T) {
	rec := &ghRecorder{}
	gh := httptest.NewServer(rec.handler(t))
	defer gh.Close()
	s := newSink(t, gh.URL, fakeCoolify(t, sha).URL)

	if err := s.Send(context.Background(), &sink.Job{Event: event(t, "deployment_success.json"), Route: route()}); err != nil {
		t.Fatal(err)
	}
	if len(rec.deployments) != 1 || len(rec.statuses) != 1 {
		t.Fatalf("deployments=%d statuses=%d", len(rec.deployments), len(rec.statuses))
	}
	d := rec.deployments[0]
	if d["ref"] != sha || d["environment"] != "dev" || d["auto_merge"] != false || d["production_environment"] != false {
		t.Errorf("deployment body = %v", d)
	}
	if rc, ok := d["required_contexts"].([]any); !ok || len(rc) != 0 {
		t.Errorf("required_contexts = %v", d["required_contexts"])
	}
	st := rec.statuses[0]
	if st["state"] != "success" || st["environment_url"] != "https://dev-api.shop.example.com" || !strings.HasPrefix(st["log_url"].(string), "https://coolify.example.com/") {
		t.Errorf("status body = %v", st)
	}
}

func TestFailureAndHEADFallback(t *testing.T) {
	rec := &ghRecorder{}
	gh := httptest.NewServer(rec.handler(t))
	defer gh.Close()
	s := newSink(t, gh.URL, fakeCoolify(t, "HEAD").URL)

	e := event(t, "deployment_success.json")
	e.Event = coolify.EventDeploymentFailed
	if err := s.Send(context.Background(), &sink.Job{Event: e, Route: route()}); err != nil {
		t.Fatal(err)
	}
	if rec.deployments[0]["ref"] != sha {
		t.Errorf("ref = %v, want branch head", rec.deployments[0]["ref"])
	}
	if rec.statuses[0]["state"] != "failure" {
		t.Errorf("state = %v", rec.statuses[0]["state"])
	}
	if _, ok := rec.statuses[0]["environment_url"]; ok {
		t.Error("failed deploy should not set environment_url")
	}
}

func TestRetryDoesNotDuplicateDeployment(t *testing.T) {
	rec := &ghRecorder{failStatusN: 1}
	gh := httptest.NewServer(rec.handler(t))
	defer gh.Close()
	s := newSink(t, gh.URL, fakeCoolify(t, sha).URL)
	job := &sink.Job{Event: event(t, "deployment_success.json"), Route: route()}

	err := s.Send(context.Background(), job)
	if !httpx.Retryable(err) {
		t.Fatalf("502 on status should be retryable: %v", err)
	}
	if err := s.Send(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if len(rec.deployments) != 1 || len(rec.statuses) != 1 {
		t.Errorf("deployments=%d statuses=%d", len(rec.deployments), len(rec.statuses))
	}
}

func TestPreviewEnvironment(t *testing.T) {
	rec := &ghRecorder{}
	gh := httptest.NewServer(rec.handler(t))
	defer gh.Close()
	s := newSink(t, gh.URL, fakeCoolify(t, sha).URL)
	e := event(t, "deployment_success_preview.json")
	// Point the preview at the backend repo the fake GitHub serves.
	e.ApplicationUUID = "a1b2c3d4e5f6a7b8c9d0e1f2"
	if err := s.Send(context.Background(), &sink.Job{Event: e, Route: route()}); err != nil {
		t.Fatal(err)
	}
	d := rec.deployments[0]
	if d["environment"] != "dev-pr-31" || d["transient_environment"] != true {
		t.Errorf("deployment = %v", d)
	}
	if rec.statuses[0]["environment_url"] != "https://31.dev.shop.example.com" {
		t.Errorf("status = %v", rec.statuses[0])
	}
}

func TestSkipsNonDeploymentEvents(t *testing.T) {
	s := New("http://unused", StaticToken("x"), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s.Send(context.Background(), &sink.Job{Event: event(t, "test.json"), Route: route()}); err != nil {
		t.Fatal(err)
	}
}

func TestClassify(t *testing.T) {
	cases := map[int]bool{401: true, 404: false, 409: false, 422: false, 429: true, 500: true}
	for code, retry := range cases {
		if got := httpx.Retryable(classify(&httpx.StatusError{Code: code})); got != retry {
			t.Errorf("%d: retryable=%v want %v", code, got, retry)
		}
	}
	if !httpx.Retryable(classify(&httpx.StatusError{Code: 403, Body: "You have exceeded a secondary rate limit"})) {
		t.Error("403 secondary rate limit should be retryable")
	}
}

func TestAppAuth(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	var tokenCalls int
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		verifyJWT(t, r.Header.Get("Authorization"), &key.PublicKey)
		switch r.URL.Path {
		case "/repos/o/r/installation":
			io.WriteString(w, `{"id":7}`)
		case "/app/installations/7/access_tokens":
			tokenCalls++
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"token":"ghs_abc","expires_at":"2099-01-01T00:00:00Z"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()

	// Env vars often carry the PEM with literal \n sequences.
	flat := strings.ReplaceAll(string(pemKey), "\n", `\n`)
	a, err := NewAppAuth("12345", []byte(flat), gh.URL)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		tok, err := a.Token(context.Background(), "o", "r")
		if err != nil || tok != "ghs_abc" {
			t.Fatalf("Token = %q, %v", tok, err)
		}
	}
	if tokenCalls != 1 {
		t.Errorf("token minted %d times, want 1 (cached)", tokenCalls)
	}
	a.Invalidate("o", "r")
	if _, err := a.Token(context.Background(), "o", "r"); err != nil || tokenCalls != 2 {
		t.Errorf("after Invalidate: calls=%d err=%v", tokenCalls, err)
	}
}

func verifyJWT(t *testing.T, auth string, pub *rsa.PublicKey) {
	t.Helper()
	jwt, ok := strings.CutPrefix(auth, "Bearer ")
	parts := strings.Split(jwt, ".")
	if !ok || len(parts) != 3 {
		t.Fatalf("bad auth header %q", auth)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("jwt signature: %v", err)
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}
	_ = json.Unmarshal(claims, &c)
	if c.Iss != "12345" || c.Exp-c.Iat > 600 {
		t.Errorf("claims = %+v", c)
	}
}

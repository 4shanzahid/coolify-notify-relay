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
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/4shanzahid/coolify-notify-relay/internal/httpx"
)

// TokenSource returns a token that may create deployments in owner/repo.
type TokenSource interface {
	Token(ctx context.Context, owner, repo string) (string, error)
	// Invalidate drops a cached token after GitHub rejected it.
	Invalidate(owner, repo string)
}

// StaticToken is a fine-grained PAT (Deployments: read & write) or a classic
// PAT with the repo_deployment scope.
type StaticToken string

func (t StaticToken) Token(context.Context, string, string) (string, error) { return string(t), nil }
func (t StaticToken) Invalidate(string, string)                             {}

// AppAuth authenticates as a GitHub App installation.
type AppAuth struct {
	appID string
	key   *rsa.PrivateKey
	api   string
	http  *http.Client
	now   func() time.Time

	mu            sync.Mutex
	installations map[string]int64 // "owner/repo" → installation id
	tokens        map[int64]appToken
}

type appToken struct {
	token   string
	expires time.Time
}

// NewAppAuth authenticates as GitHub App appID using its PEM private key.
func NewAppAuth(appID string, privateKeyPEM []byte, apiBase string) (*AppAuth, error) {
	key, err := parsePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, err
	}
	return &AppAuth{
		appID:         appID,
		key:           key,
		api:           strings.TrimRight(apiBase, "/"),
		http:          &http.Client{Timeout: 15 * time.Second},
		now:           time.Now,
		installations: map[string]int64{},
		tokens:        map[int64]appToken{},
	}, nil
}

func (a *AppAuth) Token(ctx context.Context, owner, repo string) (string, error) {
	slug := owner + "/" + repo
	a.mu.Lock()
	id, ok := a.installations[slug]
	if ok {
		if t, ok := a.tokens[id]; ok && a.now().Before(t.expires.Add(-5*time.Minute)) {
			a.mu.Unlock()
			return t.token, nil
		}
	}
	a.mu.Unlock()

	jwt, err := a.jwt()
	if err != nil {
		return "", httpx.Permanent(err)
	}
	h := appHeaders("Bearer " + jwt)

	if !ok {
		var inst struct {
			ID int64 `json:"id"`
		}
		if _, err := httpx.DoJSON(ctx, a.http, http.MethodGet, fmt.Sprintf("%s/repos/%s/installation", a.api, slug), h, nil, &inst); err != nil {
			return "", fmt.Errorf("find app installation for %s: %w", slug, err)
		}
		id = inst.ID
	}

	var tok struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if _, err := httpx.DoJSON(ctx, a.http, http.MethodPost, fmt.Sprintf("%s/app/installations/%d/access_tokens", a.api, id), h, nil, &tok); err != nil {
		return "", fmt.Errorf("create installation token: %w", err)
	}

	a.mu.Lock()
	a.installations[slug] = id
	a.tokens[id] = appToken{token: tok.Token, expires: tok.ExpiresAt}
	a.mu.Unlock()
	return tok.Token, nil
}

func (a *AppAuth) Invalidate(owner, repo string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if id, ok := a.installations[owner+"/"+repo]; ok {
		delete(a.tokens, id)
	}
	delete(a.installations, owner+"/"+repo)
}

// jwt signs the app JWT (RS256). iat is backdated 60s for clock drift; GitHub
// caps exp at 10 minutes.
func (a *AppAuth) jwt() (string, error) {
	now := a.now()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iat": now.Add(-60 * time.Second).Unix(),
		"exp": now.Add(9 * time.Minute).Unix(),
		"iss": a.appID,
	})
	if err != nil {
		return "", err
	}
	signing := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// parsePrivateKey accepts the PKCS#1 PEM GitHub issues, PKCS#8, and keys whose
// newlines were flattened to literal "\n" when pasted into an env var.
func parsePrivateKey(b []byte) (*rsa.PrivateKey, error) {
	b = []byte(strings.ReplaceAll(string(b), `\n`, "\n"))
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("github app private key: no PEM block found")
	}
	if k, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("github app private key: %w", err)
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("github app private key: not an RSA key")
	}
	return rk, nil
}

func appHeaders(auth string) http.Header {
	return http.Header{
		"Authorization":        {auth},
		"Accept":               {"application/vnd.github+json"},
		"X-GitHub-Api-Version": {"2022-11-28"},
		"User-Agent":           {"coolify-notify-relay"},
	}
}

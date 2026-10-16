package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"network", errors.New("connection refused"), true},
		{"canceled", fmt.Errorf("send: %w", context.Canceled), false},
		{"408", &StatusError{Code: 408}, true},
		{"429", &StatusError{Code: 429}, true},
		{"500", &StatusError{Code: 500}, true},
		{"400", &StatusError{Code: 400}, false},
		{"permanent wins", Permanent(&StatusError{Code: 503}), false},
		{"transient wins", Transient(&StatusError{Code: 401}), true},
		{"wrapped", fmt.Errorf("ctx: %w", &StatusError{Code: 502}), true},
	}
	for _, c := range cases {
		if got := Retryable(c.err); got != c.want {
			t.Errorf("%s: Retryable = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	if d := parseRetryAfter("7"); d != 7*time.Second {
		t.Errorf("seconds: %v", d)
	}
	future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
	if d := parseRetryAfter(future); d < 25*time.Second || d > 31*time.Second {
		t.Errorf("http-date: %v", d)
	}
	for _, v := range []string{"", "-1", "soon"} {
		if d := parseRetryAfter(v); d != 0 {
			t.Errorf("%q: %v", v, d)
		}
	}
}

func TestDoJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			if r.Header.Get("Content-Type") != "application/json; charset=UTF-8" || r.Header.Get("Accept") != "application/vnd.test" {
				t.Errorf("headers: %v", r.Header)
			}
			b, _ := io.ReadAll(r.Body)
			fmt.Fprintf(w, `{"echo":%s}`, b)
		case "/limited":
			w.Header().Set("Retry-After", "3")
			http.Error(w, "{\n  \"error\": \"slow down\"\n}", http.StatusTooManyRequests)
		case "/garbage":
			io.WriteString(w, "not json")
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	var out struct {
		Echo map[string]int `json:"echo"`
	}
	code, err := DoJSON(ctx, srv.Client(), http.MethodPost, srv.URL+"/ok", http.Header{"Accept": {"application/vnd.test"}}, map[string]int{"a": 1}, &out)
	if err != nil || code != 200 || out.Echo["a"] != 1 {
		t.Fatalf("ok: %d %v %+v", code, err, out)
	}

	_, err = DoJSON(ctx, srv.Client(), http.MethodGet, srv.URL+"/limited", nil, nil, nil)
	se, ok := errors.AsType[*StatusError](err)
	if !ok || se.Code != 429 || se.RetryAfter != 3*time.Second || strings.Contains(se.Body, "\n") {
		t.Errorf("limited: %#v", err)
	}

	_, err = DoJSON(ctx, srv.Client(), http.MethodGet, srv.URL+"/garbage", nil, nil, &out)
	if err == nil || Retryable(err) {
		t.Errorf("garbage body should be a permanent error: %v", err)
	}

	if _, err := DoJSON(ctx, srv.Client(), http.MethodPost, srv.URL+"/ok", nil, func() {}, nil); err == nil || Retryable(err) {
		t.Errorf("unencodable body should be a permanent error: %v", err)
	}
}

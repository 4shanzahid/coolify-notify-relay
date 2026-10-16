// Package httpx holds the shared HTTP helpers: JSON requests, status errors
// and the retryable/permanent error classification used by the queue.
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// maxResponseBody caps decoded responses. Coolify's GET /deployments/{uuid}
// embeds the full build log, so this is generous.
const maxResponseBody = 32 << 20

// StatusError is returned for any non-2xx response.
type StatusError struct {
	Code       int
	Body       string
	RetryAfter time.Duration
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("http %d: %s", e.Code, e.Body)
}

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// Permanent marks err as not worth retrying.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err}
}

type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// Transient marks err as worth retrying even if its status says otherwise
// (e.g. a 401 after which a fresh token is fetched).
func Transient(err error) error {
	if err == nil {
		return nil
	}
	return &transientError{err}
}

// Retryable reports whether a failed delivery should be attempted again:
// 408, 429 and 5xx responses and transport errors are; everything else is not.
func Retryable(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := errors.AsType[*permanentError](err); ok {
		return false
	}
	if _, ok := errors.AsType[*transientError](err); ok {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if se, ok := errors.AsType[*StatusError](err); ok {
		return se.Code == http.StatusRequestTimeout || se.Code == http.StatusTooManyRequests || se.Code >= 500
	}
	return true
}

// RetryAfter returns the server-requested delay carried by err, if any.
func RetryAfter(err error) time.Duration {
	if se, ok := errors.AsType[*StatusError](err); ok {
		return se.RetryAfter
	}
	return 0
}

// DoJSON sends body (if non-nil) as JSON and decodes a 2xx response into out
// (if non-nil). Non-2xx responses become *StatusError.
func DoJSON(ctx context.Context, client *http.Client, method, rawURL string, headers http.Header, body, out any) (int, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, Permanent(fmt.Errorf("encode request: %w", err))
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return 0, Permanent(err)
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json")
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	// Drain before closing so the transport can reuse the connection (see http.Client.Do).
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return resp.StatusCode, &StatusError{
			Code:       resp.StatusCode,
			Body:       strings.Join(strings.Fields(string(b)), " "),
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
		}
	}
	if out == nil {
		return resp.StatusCode, nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBody)).Decode(out); err != nil {
		return resp.StatusCode, Permanent(fmt.Errorf("decode response: %w", err))
	}
	return resp.StatusCode, nil
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}

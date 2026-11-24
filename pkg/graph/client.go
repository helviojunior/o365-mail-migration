package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/helviojunior/mail_migration/pkg/log"
)

// BaseURL is the Microsoft Graph v1.0 endpoint.
const BaseURL = "https://graph.microsoft.com/v1.0"

// maxRetries bounds throttling/transient retries per request.
const maxRetries = 6

// Client is a small Graph REST client with token injection and throttling
// aware retries.
type Client struct {
	auth *Authenticator
	http *http.Client
}

// NewClient builds a Graph client that authenticates with auth.
func NewClient(auth *Authenticator) *Client {
	return &Client{
		auth: auth,
		http: &http.Client{Timeout: 5 * time.Minute},
	}
}

// APIError describes a non-2xx Graph response.
type APIError struct {
	Status int
	Body   string
	Method string
	URL    string
}

func (e *APIError) Error() string {
	if code, msg, ok := parseGraphError(e.Body); ok {
		if code != "" {
			return fmt.Sprintf("graph %d %s: %s", e.Status, code, msg)
		}
		return fmt.Sprintf("graph %d: %s", e.Status, msg)
	}
	return fmt.Sprintf("graph %s %s -> %d: %s", e.Method, e.URL, e.Status, e.Body)
}

// parseGraphError extracts code/message from the standard Graph error envelope
// {"error":{"code":"...","message":"..."}}. ok is false when body is not that
// shape.
func parseGraphError(body string) (code, message string, ok bool) {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		return "", "", false
	}
	if env.Error.Code == "" && env.Error.Message == "" {
		return "", "", false
	}
	return env.Error.Code, env.Error.Message, true
}

// do performs a request with retries on 429/503/504 (honouring Retry-After)
// and returns the response status and body.
func (c *Client) do(ctx context.Context, method, url, contentType string, body []byte) (int, []byte, error) {
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		token, err := c.auth.Token(ctx)
		if err != nil {
			return 0, nil, err
		}

		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, reader)
		if err != nil {
			return 0, nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/json")
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			if sleepBackoff(ctx, attempt, 0) {
				continue
			}
			return 0, nil, err
		}

		data, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			if sleepBackoff(ctx, attempt, 0) {
				continue
			}
			return 0, nil, readErr
		}

		switch resp.StatusCode {
		case http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
			log.Debugf("throttled (%d) on %s; retry in %s (attempt %d)", resp.StatusCode, url, retryAfter, attempt+1)
			if sleepBackoff(ctx, attempt, retryAfter) {
				continue
			}
			return resp.StatusCode, data, &APIError{Status: resp.StatusCode, Body: string(data), Method: method, URL: url}
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return resp.StatusCode, data, &APIError{Status: resp.StatusCode, Body: string(data), Method: method, URL: url}
		}
		return resp.StatusCode, data, nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("exhausted retries for %s %s", method, url)
	}
	return 0, nil, lastErr
}

// getJSON GETs path (absolute URL or path relative to BaseURL) into out.
func (c *Client) getJSON(ctx context.Context, url string, out interface{}) error {
	url = c.abs(url)
	_, data, err := c.do(ctx, http.MethodGet, url, "", nil)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// getRaw GETs raw bytes (used for MIME $value).
func (c *Client) getRaw(ctx context.Context, url string) ([]byte, error) {
	_, data, err := c.do(ctx, http.MethodGet, c.abs(url), "", nil)
	return data, err
}

// postRaw POSTs a raw body with an explicit content type and returns the body.
func (c *Client) postRaw(ctx context.Context, url, contentType string, body []byte) ([]byte, error) {
	_, data, err := c.do(ctx, http.MethodPost, c.abs(url), contentType, body)
	return data, err
}

// postJSON POSTs an encoded body and decodes the response into out.
func (c *Client) postJSON(ctx context.Context, url string, in, out interface{}) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, data, err := c.do(ctx, http.MethodPost, c.abs(url), "application/json", payload)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// patchJSON PATCHes an encoded body.
func (c *Client) patchJSON(ctx context.Context, url string, in interface{}) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	_, _, err = c.do(ctx, http.MethodPatch, c.abs(url), "application/json", payload)
	return err
}

// deleteURL issues a DELETE against path (absolute or relative to BaseURL).
func (c *Client) deleteURL(ctx context.Context, url string) error {
	_, _, err := c.do(ctx, http.MethodDelete, c.abs(url), "", nil)
	return err
}

// jsonUnmarshal is a thin wrapper so other files in the package need not import
// encoding/json directly.
func jsonUnmarshal(data []byte, out interface{}) error { return json.Unmarshal(data, out) }

func (c *Client) abs(url string) string {
	if len(url) >= 4 && url[:4] == "http" {
		return url
	}
	return BaseURL + url
}

// parseRetryAfter reads a Retry-After header (seconds). 0 means unspecified.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d > 0 {
			return d
		}
	}
	return 0
}

// sleepBackoff waits before a retry. It uses explicit when (from Retry-After)
// if given, otherwise exponential backoff. Returns false if retries should stop
// (ctx cancelled or attempts exhausted).
func sleepBackoff(ctx context.Context, attempt int, when time.Duration) bool {
	if attempt >= maxRetries {
		return false
	}
	if when <= 0 {
		when = time.Duration(1<<attempt) * time.Second
		if when > 60*time.Second {
			when = 60 * time.Second
		}
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(when):
		return true
	}
}

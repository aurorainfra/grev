package jev

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is the public API root; TYPESAFE_BASE_URL overrides it.
const DefaultBaseURL = "https://api.typesafe.ai"

// Retry policy, mirroring the official SDK defaults.
const (
	MaxRetries     = 3
	backoffInitial = 500 * time.Millisecond
	backoffMax     = 5 * time.Second
	backoffJitter  = 0.25
	maxRetryAfter  = 60 * time.Second
	RequestTimeout = 60 * time.Second
)

// Client talks to one API root with one key.
type Client struct {
	BaseURL   string
	Key       string
	UserAgent string
	HTTP      *http.Client

	// OnRetry, if set, is called before sleeping for a retry. throttled is
	// true for 429/529, which the scheduler treats as a signal to slow down.
	OnRetry func(status int, throttled bool, wait time.Duration)
}

// NewClient returns a client for key, honoring TYPESAFE_BASE_URL.
func NewClient(key, userAgent string) *Client {
	base := strings.TrimRight(os.Getenv("TYPESAFE_BASE_URL"), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	return &Client{BaseURL: base, Key: key, UserAgent: userAgent, HTTP: &http.Client{}}
}

// APIError is a non-2xx answer from the API.
type APIError struct {
	Status    int
	Body      string
	RequestID string
}

func (e *APIError) Error() string {
	msg := strings.TrimSpace(e.Body)
	var parsed struct {
		Detail any `json:"detail"`
		Error  any `json:"error"`
	}
	if json.Unmarshal([]byte(msg), &parsed) == nil {
		if parsed.Detail != nil {
			b, _ := json.Marshal(parsed.Detail)
			msg = string(b)
		} else if parsed.Error != nil {
			b, _ := json.Marshal(parsed.Error)
			msg = string(b)
		}
	}
	if len(msg) > 400 {
		msg = msg[:400] + "…"
	}
	s := fmt.Sprintf("API %d %s", e.Status, http.StatusText(e.Status))
	if msg != "" {
		s += ": " + msg
	}
	if e.RequestID != "" {
		s += " (request-id " + e.RequestID + ")"
	}
	if e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden {
		s += "; check credentials with `jev key status`"
	}
	return s
}

func retryable(status int) bool {
	switch status {
	case 429, 500, 502, 503, 504, 529:
		return true
	}
	return false
}

// debugRequests (GREV_DEBUG=requests) prints request and response bodies.
var debugRequests = slices.Contains(strings.Split(os.Getenv("GREV_DEBUG"), ","), "requests")

// Do sends one System One request, retrying transient failures.
func (c *Client) Do(ctx context.Context, req *Request) (*Response, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	if debugRequests {
		fmt.Fprintf(os.Stderr, "jev request: %s\n", body)
	}
	out, err := c.call(ctx, http.MethodPost, "/v1/systemone", body)
	if err != nil {
		return nil, err
	}
	if debugRequests {
		fmt.Fprintf(os.Stderr, "jev response: %s\n", out)
	}
	var resp Response
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &resp, nil
}

// Models lists the model names the account can use. It accepts the System One
// shape ({"models":[…]}), a bare array, and the OpenAI-compatible shape
// ({"data":[…]}) served by gateways such as OpenRouter. From an OpenAI-shaped
// list only the Jev models are kept: those are the ones the System One
// endpoint can answer with, and a gateway's full list is mostly chat models
// that would only mislead.
func (c *Client) Models(ctx context.Context) ([]ModelCard, error) {
	out, err := c.call(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Models []ModelCard `json:"models"`
	}
	if err := json.Unmarshal(out, &resp); err == nil && resp.Models != nil {
		return resp.Models, nil
	}
	var arr []ModelCard
	if json.Unmarshal(out, &arr) == nil {
		return arr, nil
	}
	var openai struct {
		Data []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Created     int64  `json:"created"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &openai); err != nil || openai.Data == nil {
		return nil, fmt.Errorf("decoding models: unrecognized response shape")
	}
	var cards []ModelCard
	for _, m := range openai.Data {
		if !knownJevModel(m.ID) {
			continue
		}
		card := ModelCard{Name: m.ID, Description: m.Name}
		if m.Description != "" {
			card.Description = m.Description
		}
		if m.Created > 0 {
			card.ReleaseDate = time.Unix(m.Created, 0).UTC().Format("2006-01-02")
		}
		cards = append(cards, card)
	}
	return cards, nil
}

// knownJevModel reports whether id names a Jev model the price table knows,
// which is what makes it addressable through System One. An OpenAI-shaped
// list belongs to the gateway's chat surface instead: OpenRouter lists
// "typesafe/jev-router" there, and posting that to /v1/systemone answers
// "Model typesafe/jev-router does not exist".
func knownJevModel(id string) bool {
	for _, m := range modelIDs(id) {
		if _, ok := priceOf(m); ok {
			return true
		}
	}
	return false
}

// Raw sends body to path and returns the raw response body.
func (c *Client) Raw(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	return c.call(ctx, method, path, body)
}

func (c *Client) call(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		out, status, wait, err := c.once(ctx, method, path, body)
		if err == nil {
			return out, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempt >= MaxRetries || (status != 0 && !retryable(status)) {
			return nil, err
		}
		if wait <= 0 {
			wait = backoff(attempt)
		}
		if c.OnRetry != nil {
			c.OnRetry(status, status == 429 || status == 529, wait)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// once performs a single attempt. status is 0 for transport errors (which are
// retried); wait is the server-requested delay, if any.
func (c *Client) once(ctx context.Context, method, path string, body []byte) ([]byte, int, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	hr, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return nil, -1, 0, err
	}
	hr.Header.Set("Authorization", "Bearer "+c.Key)
	if body != nil {
		hr.Header.Set("Content-Type", "application/json")
	}
	if c.UserAgent != "" {
		hr.Header.Set("User-Agent", c.UserAgent)
	}
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		// Transport errors (including our per-attempt timeout) are retried;
		// the caller checks the parent context first.
		return nil, 0, 0, err
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, 0, err
	}
	if resp.StatusCode/100 == 2 {
		return out, resp.StatusCode, 0, nil
	}
	return nil, resp.StatusCode, retryAfter(resp.Header), &APIError{
		Status:    resp.StatusCode,
		Body:      string(out),
		RequestID: resp.Header.Get("x-typesafe-request-id"),
	}
}

func backoff(attempt int) time.Duration {
	d := backoffInitial << attempt
	if d > backoffMax || d <= 0 {
		d = backoffMax
	}
	return d - time.Duration(rand.Float64()*backoffJitter*float64(d))
}

// retryAfter reads retry-after-ms or Retry-After (seconds or HTTP date).
func retryAfter(h http.Header) time.Duration {
	var d time.Duration
	if v := h.Get("retry-after-ms"); v != "" {
		if ms, err := strconv.ParseFloat(v, 64); err == nil {
			d = time.Duration(ms * float64(time.Millisecond))
		}
	} else if v := h.Get("Retry-After"); v != "" {
		if s, err := strconv.ParseFloat(v, 64); err == nil {
			d = time.Duration(s * float64(time.Second))
		} else if t, err := http.ParseTime(v); err == nil {
			d = time.Until(t)
		}
	}
	if d < 0 {
		d = 0
	}
	if d > maxRetryAfter {
		d = 0 // longer than we are willing to honor: fall back to backoff
	}
	return d
}

// Package memory pushes AI coding agent conversation turns to AnonaMemory.
package memory

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// DefaultBaseURL is AnonaMemory's data-plane host. The docs also list
// https://memory.anonalabs.com as a legacy alternative.
const DefaultBaseURL = "https://api.anonalabs.com"

// spacesPath needs its trailing slash. The deployed API answers
// /v1/spaces with 307 -> http://api.anonalabs.com/v1/spaces/ , which both
// downgrades to plaintext and loses the POST body (the create then fails
// with "Field required"). Addressing the canonical path avoids the redirect
// entirely. /v1/record/batch is served directly and takes no trailing slash.
const spacesPath = "/v1/spaces/"

// maxBatchItems is the API's hard limit on /v1/record/batch.
const maxBatchItems = 100

// maxAttempts caps retries of a single request, including the first try.
const maxAttempts = 5

func itoa(n int) string { return strconv.Itoa(n) }

type Space struct {
	SpaceID   string `json:"space_id"`
	Name      string `json:"name"`
	CreatedAt string `json:"created_at"`
}

// RecordItem is one element of a /v1/record/batch payload. The batch
// endpoint accepts only these four fields -- session_id, agent_id, and tags
// exist on the single-record endpoint only, so those travel in Metadata.
type RecordItem struct {
	Content   string                 `json:"content"`
	Context   string                 `json:"context,omitempty"`
	Timestamp string                 `json:"timestamp,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// APIError is AnonaMemory's error envelope. RequestID is what their support
// asks for, so it must survive to the user's terminal.
//
// The live API nests the fields under an "error" key --
// {"error":{"code":...,"message":...,"request_id":...}} -- even though the
// published docs describe them at the top level. Both shapes are decoded, so
// a flat envelope keeps working if the API is ever changed to match its docs.
type APIError struct {
	Status    int    `json:"-"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// errorEnvelope is the nested shape the deployed API actually returns.
type errorEnvelope struct {
	Error *APIError `json:"error"`
}

// decodeAPIError fills e from body, accepting either the nested or the flat
// envelope. A body that is neither (a proxy error page, say) leaves the
// fields empty rather than masking the status code.
func decodeAPIError(body []byte, e *APIError) {
	var nested errorEnvelope
	if err := json.Unmarshal(body, &nested); err == nil && nested.Error != nil {
		e.Code = nested.Error.Code
		e.Message = nested.Error.Message
		e.RequestID = nested.Error.RequestID
		return
	}
	_ = json.Unmarshal(body, e)
}

func (e *APIError) Error() string {
	return fmt.Sprintf("anonamemory: HTTP %d %s: %s (request_id %s)", e.Status, e.Code, e.Message, e.RequestID)
}

// retryable reports whether a status should be retried. The API docs are
// explicit that no other 4xx is worth retrying -- it fails identically.
func retryable(status int) bool {
	return status == http.StatusTooManyRequests ||
		status == http.StatusInternalServerError ||
		status == http.StatusServiceUnavailable
}

type Client struct {
	BaseURL string
	APIKey  string
	HTTP    *http.Client
	// Sleep is injectable so tests don't actually wait out the backoff.
	Sleep func(time.Duration)
}

func NewClient(apiKey string) *Client {
	return &Client{
		BaseURL: DefaultBaseURL,
		APIKey:  apiKey,
		HTTP: &http.Client{
			Timeout:       30 * time.Second,
			CheckRedirect: refuseInsecureRedirect,
		},
		Sleep: time.Sleep,
	}
}

// refuseInsecureRedirect blocks any redirect that would downgrade an HTTPS
// request to plaintext HTTP. The deployed API answers a slashless collection
// URL with 307 -> http://... , and following that would put the bearer token
// on the wire unencrypted. Requests are made against the exact paths the API
// serves so this should never fire; it exists so a future redirect cannot
// silently leak the key.
func refuseInsecureRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return fmt.Errorf(
			"refusing redirect from %s to %s: it would send the API key over plaintext HTTP",
			via[0].URL.Scheme, req.URL.Scheme,
		)
	}
	if len(via) >= 10 {
		return fmt.Errorf("stopped after 10 redirects")
	}
	return nil
}

// do issues one request with exponential backoff plus jitter on retryable
// statuses, decoding either the success body or the error envelope into out.
func (c *Client) do(method, path string, payload interface{}, out interface{}) error {
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(1<<uint(attempt-1)) * time.Second
			jitter := time.Duration(rand.Int63n(int64(time.Second)))
			c.Sleep(backoff + jitter)
		}

		var body io.Reader
		if payload != nil {
			encoded, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			body = bytes.NewReader(encoded)
		}

		req, err := http.NewRequest(method, c.BaseURL+path, body)
		if err != nil {
			return err
		}
		// The scheme is required: X-API-Key and a bare Authorization value
		// are both rejected by the API.
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}

		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}

		if resp.StatusCode >= 400 {
			apiErr := &APIError{Status: resp.StatusCode}
			decodeAPIError(respBody, apiErr)
			if !retryable(resp.StatusCode) {
				return apiErr
			}
			lastErr = apiErr
			continue
		}

		if out != nil {
			return json.Unmarshal(respBody, out)
		}
		return nil
	}

	return lastErr
}

func (c *Client) ListSpaces() ([]Space, error) {
	var out struct {
		Spaces []Space `json:"spaces"`
		Total  int     `json:"total"`
	}
	if err := c.do(http.MethodGet, spacesPath, nil, &out); err != nil {
		return nil, err
	}
	return out.Spaces, nil
}

func (c *Client) CreateSpace(name, description string) (Space, error) {
	payload := map[string]string{"name": name}
	if description != "" {
		payload["description"] = description
	}
	var space Space
	if err := c.do(http.MethodPost, spacesPath, payload, &space); err != nil {
		return Space{}, err
	}
	return space, nil
}

// RecordBatch writes items in chunks of maxBatchItems and returns how many
// the API accepted in total. Writes are async server-side: the response
// carries a job_id, not a memory_id.
func (c *Client) RecordBatch(spaceID string, items []RecordItem) (int, error) {
	accepted := 0

	for start := 0; start < len(items); start += maxBatchItems {
		end := start + maxBatchItems
		if end > len(items) {
			end = len(items)
		}

		payload := map[string]interface{}{
			"space_id": spaceID,
			"items":    items[start:end],
		}
		var out struct {
			JobID    string `json:"job_id"`
			Status   string `json:"status"`
			Accepted int    `json:"accepted"`
		}
		if err := c.do(http.MethodPost, "/v1/record/batch", payload, &out); err != nil {
			// Return what landed so the caller can advance its watermark
			// only over the chunks that actually succeeded.
			return accepted, err
		}
		accepted += out.Accepted
	}

	return accepted, nil
}

package memory

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func testClient(srv *httptest.Server) *Client {
	c := NewClient("anona_live_testkey")
	c.BaseURL = srv.URL
	c.Sleep = func(time.Duration) {}
	return c
}

func TestListSpacesSendsBearerHeader(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"spaces":[{"space_id":"spc_a1","name":"work","created_at":"2026-08-01T00:00:00Z"}],"total":1}`)
	}))
	defer srv.Close()

	spaces, err := testClient(srv).ListSpaces()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer anona_live_testkey" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer anona_live_testkey")
	}
	// The trailing slash is required: the deployed API answers the slashless
	// path with a 307 to plaintext http:// that also loses a POST body.
	if gotPath != "/v1/spaces/" {
		t.Errorf("path = %q, want /v1/spaces/", gotPath)
	}
	if len(spaces) != 1 || spaces[0].SpaceID != "spc_a1" || spaces[0].Name != "work" {
		t.Fatalf("spaces = %+v", spaces)
	}
}

func TestCreateSpacePostsName(t *testing.T) {
	var body map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"space_id":"spc_new","name":"agentobs","created_at":"2026-08-06T00:00:00Z"}`)
	}))
	defer srv.Close()

	space, err := testClient(srv).CreateSpace("agentobs", "coding agent memory")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if body["name"] != "agentobs" {
		t.Errorf("name = %v, want agentobs", body["name"])
	}
	if body["description"] != "coding agent memory" {
		t.Errorf("description = %v", body["description"])
	}
	if space.SpaceID != "spc_new" {
		t.Errorf("space_id = %q, want spc_new", space.SpaceID)
	}
}

func TestRecordBatchChunksAtHundred(t *testing.T) {
	var batchSizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			SpaceID string       `json:"space_id"`
			Items   []RecordItem `json:"items"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.SpaceID != "spc_a1" {
			t.Errorf("space_id = %q, want spc_a1", body.SpaceID)
		}
		batchSizes = append(batchSizes, len(body.Items))
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"job_id":"job_1","status":"processing","accepted":`+itoa(len(body.Items))+`}`)
	}))
	defer srv.Close()

	items := make([]RecordItem, 250)
	for i := range items {
		items[i] = RecordItem{Content: "turn"}
	}

	accepted, err := testClient(srv).RecordBatch("spc_a1", items)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if accepted != 250 {
		t.Errorf("accepted = %d, want 250", accepted)
	}
	want := []int{100, 100, 50}
	if len(batchSizes) != len(want) {
		t.Fatalf("batches = %v, want %v", batchSizes, want)
	}
	for i := range want {
		if batchSizes[i] != want[i] {
			t.Fatalf("batches = %v, want %v", batchSizes, want)
		}
	}
}

func TestRecordBatchRetriesOn429ThenSucceeds(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"code":"rate_limited","message":"slow down","request_id":"req_1"}`)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		io.WriteString(w, `{"job_id":"job_1","status":"processing","accepted":1}`)
	}))
	defer srv.Close()

	accepted, err := testClient(srv).RecordBatch("spc_a1", []RecordItem{{Content: "turn"}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	if accepted != 1 {
		t.Errorf("accepted = %d, want 1", accepted)
	}
}

func TestRecordBatchDoesNotRetryOn400(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"code":"bad_request","message":"content required","request_id":"req_7"}`)
	}))
	defer srv.Close()

	_, err := testClient(srv).RecordBatch("spc_a1", []RecordItem{{Content: ""}})
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (must not retry 400)", calls)
	}
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("expected *APIError, got %T: %v", err, err)
	}
	if apiErr.Code != "bad_request" || apiErr.RequestID != "req_7" || apiErr.Status != 400 {
		t.Errorf("apiErr = %+v", apiErr)
	}
}

func TestRecordBatchEmptyIsNoop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request expected for an empty batch")
	}))
	defer srv.Close()

	accepted, err := testClient(srv).RecordBatch("spc_a1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if accepted != 0 {
		t.Errorf("accepted = %d, want 0", accepted)
	}
}

// The deployed API nests its error fields under an "error" key, unlike the
// published docs which show them at the top level. Both must decode, or the
// user sees an empty "HTTP 422 : (request_id )".
func TestAPIErrorDecodesNestedAndFlatEnvelopes(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"nested (what the live API returns)", `{"error":{"code":"validation_error","message":"Field required","request_id":"req_9"}}`},
		{"flat (what the docs describe)", `{"code":"validation_error","message":"Field required","request_id":"req_9"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnprocessableEntity)
				io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			_, err := testClient(srv).CreateSpace("x", "")
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			apiErr, ok := err.(*APIError)
			if !ok {
				t.Fatalf("expected *APIError, got %T: %v", err, err)
			}
			if apiErr.Code != "validation_error" || apiErr.Message != "Field required" || apiErr.RequestID != "req_9" {
				t.Errorf("decoded = %+v, want code/message/request_id populated", apiErr)
			}
		})
	}
}

// A redirect downgrading https -> http would put the bearer token on the wire
// in plaintext. The live API emits exactly such a redirect for a slashless
// collection path, so it must be refused rather than followed.
func TestRefusesHTTPSToHTTPDowngradeRedirect(t *testing.T) {
	https := &http.Request{URL: mustURL(t, "https://api.example.com/v1/spaces")}

	plain := &http.Request{URL: mustURL(t, "http://api.example.com/v1/spaces/")}
	if err := refuseInsecureRedirect(plain, []*http.Request{https}); err == nil {
		t.Error("expected https -> http redirect to be refused")
	}

	secure := &http.Request{URL: mustURL(t, "https://api.example.com/v1/spaces/")}
	if err := refuseInsecureRedirect(secure, []*http.Request{https}); err != nil {
		t.Errorf("https -> https redirect should be allowed, got %v", err)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parsing %q: %v", raw, err)
	}
	return u
}

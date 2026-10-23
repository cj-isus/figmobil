//go:build windows

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"figmobil/internal/channel"
	"figmobil/internal/transferlimit"
)

// These tests exercise the real protected channel store and HTTP handler using
// synthetic local files. No listener, Figma, clipboard or external service runs.
func channelHTTPFixture(t *testing.T) (*Handler, string) {
	t.Helper()
	dir := t.TempDir()
	manager, err := channel.New(dir, transferlimit.New())
	if err != nil {
		t.Fatal(err)
	}
	h := testHandler(t)
	h.EnableChannel(manager)
	return h, dir
}

func channelHTTPPost(t *testing.T, h *Handler, value any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(http.MethodPost, "/api/channel", string(data)))
	return w
}

func channelHTTPError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) channel.Error {
	t.Helper()
	var body struct {
		Error channel.Error `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid error response: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if w.Code != status || body.Error.Code != code || len(fields) != 1 || fields["error"] == nil {
		t.Fatalf("wanted error-only %d/%s; got %d %s", status, code, w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "FIGMOBIL/1") {
		t.Fatal("error response disclosed a protocol frame")
	}
	return body.Error
}

func TestChannelHTTPAuthenticationAndInputRejectionsDoNotCreateProfiles(t *testing.T) {
	h, dir := channelHTTPFixture(t)
	root := t.TempDir()
	data, err := json.Marshal(channel.Request{Action: "create", Name: "Synthetic", DeviceName: "Test laptop", Root: root})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, body string
		mutate     func(*http.Request)
		status     int
		code       string
	}{
		{"foreign host", string(data), func(r *http.Request) { r.Host = "evil.example:8765" }, 403, "ORIGIN_REJECTED"},
		{"foreign origin", string(data), func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, 403, "ORIGIN_REJECTED"},
		{"missing origin", string(data), func(r *http.Request) { r.Header.Del("Origin") }, 403, "SESSION_REJECTED"},
		{"missing token", string(data), func(r *http.Request) { r.Header.Del("X-Figmobil-Token") }, 403, "SESSION_REJECTED"},
		{"wrong token", string(data), func(r *http.Request) { r.Header.Set("X-Figmobil-Token", "wrong") }, 403, "SESSION_REJECTED"},
		{"wrong content type", string(data), func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415, "CONTENT_TYPE"},
		{"unknown field", `{"action":"state","execute":"anything"}`, nil, 400, "INVALID_REQUEST"},
		{"trailing JSON", string(data) + ` {}`, nil, 400, "INVALID_REQUEST"},
		{"malformed JSON", `{`, nil, 400, "INVALID_REQUEST"},
		{"oversized body", `{"action":"state","root":"` + strings.Repeat("a", MaxChannelRequestBytes) + `"}`, nil, 413, "REQUEST_TOO_LARGE"},
		{"GET", string(data), func(r *http.Request) { r.Method = http.MethodGet }, 405, "METHOD_NOT_ALLOWED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := request(http.MethodPost, "/api/channel", tc.body)
			if tc.mutate != nil {
				tc.mutate(r)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			channelHTTPError(t, w, tc.status, tc.code)
			if tc.name == "GET" && w.Header().Get("Allow") != "POST" {
				t.Fatal("GET did not advertise the required explicit POST")
			}
		})
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("rejected request changed protected store: %d entries", len(entries))
	}
	w := channelHTTPPost(t, h, channel.Request{Action: "state"})
	var state channel.Response
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || state.Profiles == nil || len(state.Profiles) != 0 || state.Profile != nil || state.Text != "" || state.Plan != nil {
		t.Fatalf("rejected request left a profile or output: %d %s", w.Code, w.Body.String())
	}
}

func TestChannelHTTPStateCreateAndStartupRateLimitNeverDiscloseUngatedText(t *testing.T) {
	h, dir := channelHTTPFixture(t)
	root := t.TempDir()
	source := []byte("SYNTHETIC_ONLY_http_file_body\r\n")
	path := filepath.Join(root, "hello.txt")
	if err := os.WriteFile(path, source, 0600); err != nil {
		t.Fatal(err)
	}
	created := channelHTTPPost(t, h, channel.Request{Action: "create", Name: "HTTP synthetic", DeviceName: "Laptop A", Root: root})
	var body channel.Response
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if created.Code != 200 || body.Profile == nil || body.Profile.Outgoing == nil || body.Profile.Outgoing.Kind != "invite" || body.Text != "" || body.Plan != nil {
		t.Fatalf("create must queue, not issue, an invitation: %d %s", created.Code, created.Body.String())
	}
	id := body.Profile.ID
	for _, action := range []string{"state", "state"} {
		w := channelHTTPPost(t, h, channel.Request{Action: action, ProfileID: id})
		var response channel.Response
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || response.Profile == nil || response.Profile.Outgoing == nil || response.Profile.Outgoing.Next != 0 || fields["text"] != nil || fields["plan"] != nil {
			t.Fatalf("readonly state issued output or advanced outbox: %d %s", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "SYNTHETIC_ONLY_http_file_body") || strings.Contains(w.Body.String(), "FIGMOBIL/1") || strings.Contains(w.Body.String(), `"private"`) {
			t.Fatal("state response disclosed file bytes, frame or private key field")
		}
	}
	// Reopen the real store with a fresh startup gate after key generation and
	// snapshot work, so the assertion does not depend on their execution time.
	manager, err := channel.New(dir, transferlimit.New())
	if err != nil {
		t.Fatal(err)
	}
	h = testHandler(t)
	h.EnableChannel(manager)
	next := channelHTTPPost(t, h, channel.Request{Action: "next", ProfileID: id})
	problem := channelHTTPError(t, next, 429, "RATE_LIMIT")
	if problem.RetryAfterMs <= 0 || problem.RetryAfterMs > 3001 {
		t.Fatalf("invalid advisory startup delay: %d", problem.RetryAfterMs)
	}
	unchanged := channelHTTPPost(t, h, channel.Request{Action: "state", ProfileID: id})
	var afterRate channel.Response
	if err := json.Unmarshal(unchanged.Body.Bytes(), &afterRate); err != nil {
		t.Fatal(err)
	}
	if unchanged.Code != 200 || afterRate.Profile == nil || afterRate.Profile.Outgoing == nil || afterRate.Profile.Outgoing.Next != 0 || afterRate.Text != "" {
		t.Fatal("rate rejection advanced the outbox or disclosed text")
	}
	current, err := os.ReadFile(path)
	if err != nil || string(current) != string(source) {
		t.Fatal("profile creation or rate rejection changed synthetic project file")
	}
}

func TestChannelHTTPAssetsAreExplicitlyAllowedAndTestsRemainPrivate(t *testing.T) {
	h, _ := channelHTTPFixture(t)
	for _, path := range []string{"/channel.mjs", "/channel-model.mjs"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			w := httptest.NewRecorder()
			h.ServeHTTP(w, request(method, path, ""))
			if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/javascript") || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("asset %s %s: %d", method, path, w.Code)
			}
			if method == http.MethodHead && w.Body.Len() != 0 || method == http.MethodGet && w.Body.Len() == 0 {
				t.Fatalf("unexpected %s body for %s", method, path)
			}
		}
	}
	for _, path := range []string{"/channel.test.mjs", "/channel-model.test.mjs", "/internal/channel/store.go", "/profile.fmstate", "/journal.json"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request(http.MethodGet, path, ""))
		if w.Code != 404 {
			t.Fatalf("non-asset path exposed: %s (%d)", path, w.Code)
		}
	}
}

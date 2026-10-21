package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"figmobil/internal/compare"
)

const testAuthority = "127.0.0.1:8765"
const testToken = "test-session-token-with-at-least-32-characters"

func testHandler(t *testing.T) *Handler {
	t.Helper()
	h, err := New(testAuthority, testToken, compare.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func request(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, "http://"+testAuthority+path, strings.NewReader(body))
	r.Header.Set("Origin", "http://"+testAuthority)
	r.Header.Set("X-Figmobil-Token", testToken)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestListenerAndTokenConfiguration(t *testing.T) {
	for _, authority := range []string{"0.0.0.0:8765", "localhost:8765", "127.0.0.1:0", "127.0.0.1:65536", "[::]:8765", "127.0.0.1:bad"} {
		if _, err := New(authority, testToken, compare.DefaultLimits()); err == nil {
			t.Errorf("accepted %s", authority)
		}
	}
	if _, err := New(testAuthority, "short", compare.DefaultLimits()); err == nil {
		t.Fatal("accepted short token")
	}
}

func TestIndexAndAssetConfinement(t *testing.T) {
	h := testHandler(t)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request(method, "/", ""))
		if w.Code != 200 {
			t.Fatalf("index: %d %s", w.Code, w.Body.String())
		}
		if method == http.MethodGet && (!strings.Contains(w.Body.String(), testToken) || strings.Contains(w.Body.String(), "__FIGMOBIL_TOKEN__")) {
			t.Fatal("token not injected")
		}
		if method == http.MethodHead && w.Body.Len() != 0 {
			t.Fatal("HEAD has body")
		}
		for _, key := range []string{"Content-Security-Policy", "X-Frame-Options", "X-Content-Type-Options", "Cache-Control"} {
			if w.Header().Get(key) == "" {
				t.Errorf("missing %s", key)
			}
		}
	}
	for _, path := range []string{"/go.mod", "/.git/config", "/../README.md", "/api/export", "/view-model.test.mjs"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, request(http.MethodGet, path, ""))
		if w.Code != 404 {
			t.Errorf("exposed path %s: %d", path, w.Code)
		}
	}
}

func TestRejectedRequestsNeverStartComparison(t *testing.T) {
	h := testHandler(t)
	var calls atomic.Int32
	h.run = func(context.Context, compare.Request, compare.Limits) (compare.Report, error) {
		calls.Add(1)
		return compare.Report{}, nil
	}
	cases := []struct {
		name, body string
		mutate     func(*http.Request)
		status     int
		code       string
	}{
		{"foreign host", `{}`, func(r *http.Request) { r.Host = "evil.example:8765" }, 403, "ORIGIN_REJECTED"},
		{"foreign origin", `{}`, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, 403, "ORIGIN_REJECTED"},
		{"missing origin", `{}`, func(r *http.Request) { r.Header.Del("Origin") }, 403, "SESSION_REJECTED"},
		{"missing token", `{}`, func(r *http.Request) { r.Header.Del("X-Figmobil-Token") }, 403, "SESSION_REJECTED"},
		{"wrong token", `{}`, func(r *http.Request) { r.Header.Set("X-Figmobil-Token", "wrong") }, 403, "SESSION_REJECTED"},
		{"wrong type", `{}`, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, 415, "CONTENT_TYPE"},
		{"unknown field", `{"command":"run"}`, nil, 400, "INVALID_REQUEST"},
		{"trailing JSON", `{} {}`, nil, 400, "INVALID_REQUEST"},
		{"malformed", `{`, nil, 400, "INVALID_REQUEST"},
		{"oversized", `{"localRoot":"` + strings.Repeat("a", MaxRequestBytes) + `"}`, nil, 413, "REQUEST_TOO_LARGE"},
		{"GET", `{}`, func(r *http.Request) { r.Method = http.MethodGet }, 405, "METHOD_NOT_ALLOWED"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := request(http.MethodPost, "/api/compare", tc.body)
			if tc.mutate != nil {
				tc.mutate(r)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			var body errorBody
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status || body.Error.Code != tc.code {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("unauthorized comparison called %d times", calls.Load())
	}
}

func TestJobCancellationReleasesDuplicateClickGuard(t *testing.T) {
	h := testHandler(t)
	started := make(chan struct{})
	h.run = func(ctx context.Context, _ compare.Request, _ compare.Limits) (compare.Report, error) {
		close(started)
		<-ctx.Done()
		return compare.Report{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	first := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(first, request(http.MethodPost, "/api/compare", `{}`).WithContext(ctx))
	}()
	<-started
	second := httptest.NewRecorder()
	h.ServeHTTP(second, request(http.MethodPost, "/api/compare", `{}`))
	if second.Code != http.StatusConflict {
		t.Fatalf("duplicate: %d", second.Code)
	}
	cancel()
	<-done
	if first.Code != http.StatusRequestTimeout {
		t.Fatalf("cancelled: %d", first.Code)
	}
	h.run = func(context.Context, compare.Request, compare.Limits) (compare.Report, error) {
		return compare.Report{}, nil
	}
	third := httptest.NewRecorder()
	h.ServeHTTP(third, request(http.MethodPost, "/api/compare", `{}`))
	if third.Code != 200 {
		t.Fatalf("guard not released: %d", third.Code)
	}
}

func TestUnexpectedFailureDoesNotExposePanicDetails(t *testing.T) {
	h := testHandler(t)
	h.run = func(context.Context, compare.Request, compare.Limits) (compare.Report, error) {
		panic("private internal details")
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(http.MethodPost, "/api/compare", `{}`))
	if w.Code != 500 || strings.Contains(w.Body.String(), "private internal details") {
		t.Fatalf("unsafe failure: %d %s", w.Code, w.Body.String())
	}
}

func TestDomainErrorCodeIsPreserved(t *testing.T) {
	h := testHandler(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(http.MethodPost, "/api/compare", `{"localRoot":"relative","incomingRoot":"relative"}`))
	var body errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusUnprocessableEntity || body.Error.Code != "INVALID_ROOT" {
		t.Fatalf("lost domain error: %d %s", w.Code, w.Body.String())
	}
}

func TestMetadataBudgetFailureReturnsOnlyDomainError(t *testing.T) {
	local, incoming := t.TempDir(), t.TempDir()
	for _, root := range []string{local, incoming} {
		if err := os.WriteFile(filepath.Join(root, ".env.local"), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	h := testHandler(t)
	h.limits.MaxMetadataBytes = 5000
	data, err := json.Marshal(compare.Request{LocalRoot: local, IncomingRoot: incoming})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, request(http.MethodPost, "/api/compare", string(data)))
	var body errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusUnprocessableEntity || body.Error.Code != "REPORT_SIZE_LIMIT" || len(fields) != 1 || fields["error"] == nil {
		t.Fatalf("expected domain error without a partial report: %d %s", w.Code, w.Body.String())
	}
}

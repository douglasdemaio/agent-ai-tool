package live

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const goodAgents = `{"agents":[{"id":"a1","card":{"name":"alpha","description":"d","url":"https://example.com","publicKey":"k"},"status":"active","createdAt":"2026-09-01T00:00:00Z","updatedAt":"2026-09-02T00:00:00Z"}]}`

const goodMetrics = `{"generatedAt":"2026-09-27T00:00:00Z","asOf":"2026-09-20T00:00:00Z","totals":{"delivered":9,"disputed":1,"cancelled":2,"consumers":3,"services":2},"agents":[{"agentId":"a1","delivered":9,"disputed":1,"cancelled":2}]}`

func serve(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestLoadUsesAFreshFetch(t *testing.T) {
	srv := serve(t, http.StatusOK, goodAgents)
	result := Load(context.Background(), srv.URL, "/v1/agents", filepath.Join(t.TempDir(), "absent.json"), srv.Client(), ValidateAgents)

	if !result.Available() {
		t.Fatal("a 200 with a valid body should be available")
	}
	if result.FromCache {
		t.Error("a live fetch must not be reported as cache")
	}
	if result.FetchErr != nil {
		t.Errorf("FetchErr = %v, want nil", result.FetchErr)
	}
}

func TestLoadFallsBackToCacheOnServerError(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "live.json")
	at := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	if err := WriteSnapshot(cache, []byte(goodMetrics), at); err != nil {
		t.Fatal(err)
	}
	srv := serve(t, http.StatusInternalServerError, `{"error":"boom"}`)

	result := Load(context.Background(), srv.URL, "/v1/metrics", cache, srv.Client(), ValidateMetrics)
	if !result.Available() {
		t.Fatal("a 500 should fall back to the committed snapshot")
	}
	if !result.FromCache {
		t.Error("the fallback must be flagged as cache")
	}
	if result.FetchErr == nil {
		t.Error("a fallback must record why the live fetch failed")
	}
}

func TestLoadFallsBackToCacheOnMalformedBody(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "live.json")
	if err := WriteSnapshot(cache, []byte(goodMetrics), time.Now()); err != nil {
		t.Fatal(err)
	}
	// A 200 carrying the wrong shape must not be trusted.
	srv := serve(t, http.StatusOK, `{"error":"not a feed"}`)

	result := Load(context.Background(), srv.URL, "/v1/metrics", cache, srv.Client(), ValidateMetrics)
	if !result.FromCache {
		t.Fatal("a 200 with the wrong shape should be rejected in favour of the cache")
	}
}

func TestLoadFallsBackToCacheOnEmptyBody(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "live.json")
	if err := WriteSnapshot(cache, []byte(goodMetrics), time.Now()); err != nil {
		t.Fatal(err)
	}
	srv := serve(t, http.StatusOK, "   ")

	if !Load(context.Background(), srv.URL, "/v1/metrics", cache, srv.Client(), ValidateMetrics).FromCache {
		t.Fatal("an empty body should not count as a successful fetch")
	}
}

func TestLoadWithNoServiceAndNoCacheIsUnavailable(t *testing.T) {
	srv := serve(t, http.StatusServiceUnavailable, "")
	result := Load(context.Background(), srv.URL, "/v1/agents", filepath.Join(t.TempDir(), "absent.json"), srv.Client(), ValidateAgents)

	if result.Available() {
		t.Fatal("with no service and no cache the feed must be absent, not invented")
	}
	if result.FetchErr == nil {
		t.Error("FetchErr should explain the absence")
	}
}

func TestLoadWithNoBaseURLUsesCacheOnly(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "live.json")
	if err := WriteSnapshot(cache, []byte(goodAgents), time.Now()); err != nil {
		t.Fatal(err)
	}

	result := Load(context.Background(), "", "/v1/agents", cache, nil, ValidateAgents)
	if !result.FromCache || !result.Available() {
		t.Fatal("an empty base URL should read the cache and not attempt a request")
	}
}

func TestCacheAgeSurvivesAFreshClone(t *testing.T) {
	// Age must come from the recorded fetch time, not the file's mtime, or a
	// fresh clone would make a year-old snapshot look minutes old.
	dir := t.TempDir()
	cache := filepath.Join(dir, "live.json")
	old := time.Now().Add(-30 * 24 * time.Hour).Truncate(time.Second)
	if err := WriteSnapshot(cache, []byte(goodMetrics), old); err != nil {
		t.Fatal(err)
	}
	// Simulate a checkout: mtime is now, but the recorded fetch time is not.
	if err := os.Chtimes(cache, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}

	snapshot, err := readCache(cache)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.FetchedAt.Equal(old) {
		t.Errorf("recorded fetch time = %v, want %v", snapshot.FetchedAt, old)
	}
	result := Result{Response: snapshot.Response, FromCache: true, FetchedAt: snapshot.FetchedAt}
	if !result.Stale(time.Now()) {
		t.Error("a 30-day-old snapshot must read as stale even though its mtime is now")
	}
}

func TestFreshFetchIsNeverStale(t *testing.T) {
	result := Result{Response: []byte(goodMetrics)}
	if result.Stale(time.Now()) {
		t.Error("a live fetch has no age and must never be marked stale")
	}
}

func TestCorruptCacheIsReportedNotIgnored(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "live.json")
	if err := os.WriteFile(cache, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCache(cache); err == nil {
		t.Fatal("a corrupt snapshot should surface an error, not be read as empty")
	}
}

func TestRefreshRefusesToWriteWithoutABaseURL(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "live.json")
	err := Refresh(context.Background(), "", "/v1/agents", cache, http.DefaultClient, ValidateAgents)
	if err == nil {
		t.Fatal("refreshing with no service should fail rather than cache an empty snapshot")
	}
}

func TestRefreshWritesASnapshot(t *testing.T) {
	srv := serve(t, http.StatusOK, goodMetrics)
	cache := filepath.Join(t.TempDir(), "live.json")

	if err := Refresh(context.Background(), srv.URL, "/v1/metrics", cache, srv.Client(), ValidateMetrics); err != nil {
		t.Fatal(err)
	}
	snapshot, err := readCache(cache)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Response) == 0 {
		t.Error("the snapshot recorded no response")
	}
	if snapshot.FetchedAt.IsZero() {
		t.Error("the snapshot recorded no fetch time")
	}
}

func TestRefreshRefusesToCacheAMalformedBody(t *testing.T) {
	srv := serve(t, http.StatusOK, `{"nope":true}`)
	cache := filepath.Join(t.TempDir(), "live.json")

	if err := Refresh(context.Background(), srv.URL, "/v1/metrics", cache, srv.Client(), ValidateMetrics); err == nil {
		t.Fatal("a malformed body should never be committed as a snapshot")
	}
	if _, err := os.Stat(cache); !errors.Is(err, os.ErrNotExist) {
		t.Error("a rejected refresh should not leave a snapshot behind")
	}
}

func TestValidatorsRejectAFeedMissingItsKey(t *testing.T) {
	if err := ValidateAgents([]byte(`{"error":"nope"}`)); err == nil {
		t.Error("an agents feed with no agents key should be rejected")
	}
	if err := ValidateMetrics([]byte(`{"generatedAt":"2026-09-27T00:00:00Z"}`)); err == nil {
		t.Error("a metrics feed with no totals should be rejected")
	}
}

func TestNormalizeTurnsNullAgentListsIntoEmptySlices(t *testing.T) {
	if got := (AgentsResponse{}).Normalized().Agents; got == nil {
		t.Error("agents should serialise as [] not null")
	}
	if got := (MetricsResponse{}).Normalized().Agents; got == nil {
		t.Error("agents should serialise as [] not null")
	}
}

func TestUpdatedAtReportsTheNewestAgent(t *testing.T) {
	var response AgentsResponse
	raw := `{"agents":[
	  {"id":"a","updatedAt":"2026-09-01T00:00:00Z"},
	  {"id":"b","updatedAt":"2026-09-20T00:00:00Z"},
	  {"id":"c","updatedAt":"2026-09-10T00:00:00Z"}]}`
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		t.Fatal(err)
	}
	if got := response.UpdatedAt(); got.Format("2006-01-02") != "2026-09-20" {
		t.Errorf("UpdatedAt = %v, want 2026-09-20", got)
	}
}

func TestUpdatedAtIsZeroForAnEmptyFeed(t *testing.T) {
	var response AgentsResponse
	if err := json.Unmarshal([]byte(`{"agents":[]}`), &response); err != nil {
		t.Fatal(err)
	}
	if !response.UpdatedAt().IsZero() {
		t.Error("an empty feed has no update time to report")
	}
}

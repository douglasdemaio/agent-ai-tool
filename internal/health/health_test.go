package health

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/douglasdemaio/agent-ai-tool/internal/content"
)

func ptr(s string) *string { return &s }

func entry(slug, endpoint string) content.Entry {
	e := content.Entry{Slug: slug, Name: slug, URL: "https://example.com/" + slug}
	if endpoint != "" {
		e.MCPEndpointURL = ptr(endpoint)
	}
	return e
}

func TestOnlyAdvertisedEndpointsAreProbed(t *testing.T) {
	targets := Targets([]content.Entry{
		{Slug: "a", MCPEndpointURL: ptr("https://a.example/mcp")},
		{Slug: "b", AgentCardURL: ptr("https://b.example/card")},
		{Slug: "c", URL: "https://c.example"},
		{Slug: "d", MCPEndpointURL: ptr("https://d.example/mcp"), AgentCardURL: ptr("https://d.example/card")},
	})
	if len(targets["a"]) != 1 || len(targets["b"]) != 1 {
		t.Fatalf("endpoint targets = %v", targets)
	}
	if _, ok := targets["c"]; ok {
		t.Error("a bare homepage should not be probed; it is a human destination")
	}
	if len(targets["d"]) != 2 {
		t.Errorf("expected both the endpoint and the card probed, got %v", targets["d"])
	}
}

// A single failed probe must not demote. One of these services answers 50% of
// the time, and a check that flapped the directory nightly on a single sample
// would be worse than no check.
func TestOneFailedProbeOutOfThreeDoesNotDemote(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	report := Check(context.Background(), []content.Entry{entry("a", server.URL)}, server.Client(), Options{Attempts: 3, Interval: time.Millisecond}, time.Now())
	got := report.Endpoints["a "+server.URL]
	if !got.Alive {
		t.Errorf("two of three probes succeeded, so the endpoint should count as alive: %+v", got)
	}
	if len(got.Attempts) != 3 {
		t.Errorf("attempts = %v, want all three recorded", got.Attempts)
	}
}

func TestAMajorityOfFailuresDemotes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	report := Check(context.Background(), []content.Entry{entry("a", server.URL)}, server.Client(), Options{Attempts: 3, Interval: time.Millisecond}, time.Now())
	got := report.Endpoints["a "+server.URL]
	if got.Alive {
		t.Error("a 404 on every probe should demote the endpoint")
	}
	if got.Detail == "" {
		t.Error("a demotion should carry the reason so the page can explain it")
	}
}

func TestATransportErrorDemotes(t *testing.T) {
	// Port 1 on loopback refuses connections, so this exercises the dial error
	// rather than any status code.
	report := Check(context.Background(), []content.Entry{entry("a", "http://127.0.0.1:1/mcp")},
		&http.Client{Timeout: time.Second}, Options{Attempts: 1, Interval: time.Millisecond}, time.Now())
	if report.Endpoints["a http://127.0.0.1:1/mcp"].Alive {
		t.Error("an endpoint that cannot be reached should demote")
	}
}

// A 429 means the service is there and declining this client, which is a
// different fact from the endpoint being gone.
func TestRateLimitingCountsAsAlive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	report := Check(context.Background(), []content.Entry{entry("a", server.URL)}, server.Client(), Options{Attempts: 1, Interval: time.Millisecond}, time.Now())
	if !report.Endpoints["a "+server.URL].Alive {
		t.Error("429 should count as alive; the service answered")
	}
}

// The whole point of the report: an absent or stale report must demote nothing,
// or the site would keep suppressing a service on the strength of a check it no
// longer trusts.
func TestAnAbsentReportDemotesNothing(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	if got := (Report{}).Unreachable(now); got != nil {
		t.Errorf("an absent report should demote nothing, got %v", got)
	}
}

func TestAStaleReportDemotesNothing(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	report := Report{
		CheckedAt: now.Add(-StaleAfter - time.Hour),
		Endpoints: map[string]Endpoint{"a https://a.example/mcp": {URL: "https://a.example/mcp"}},
	}
	if got := report.Unreachable(now); got != nil {
		t.Errorf("a report older than %s should demote nothing, got %v", StaleAfter, got)
	}
}

func TestAFreshReportDemotesOnlyWhatItProbedAndFoundDead(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	report := Report{
		CheckedAt: now.Add(-time.Hour),
		Endpoints: map[string]Endpoint{
			"dead https://dead.example/mcp": {URL: "https://dead.example/mcp", Alive: false, Detail: "GET = 404"},
			"live https://live.example/mcp": {URL: "https://live.example/mcp", Alive: true},
			"gone https://gone.example/mcp": {URL: "https://gone.example/mcp", Alive: true},
		},
	}
	got := report.Unreachable(now)
	if len(got) != 1 || !got["dead"] {
		t.Fatalf("unreachable = %v, want only the dead endpoint", got)
	}
}

func TestDetailsExplainsOnlyFailures(t *testing.T) {
	report := Report{Endpoints: map[string]Endpoint{
		"dead https://dead.example/mcp": {Alive: false, Detail: "GET https://dead.example/mcp = 404"},
		"live https://live.example/mcp": {Alive: true, Detail: ""},
	}}
	got := report.Details()
	if got["dead"] != "GET https://dead.example/mcp = 404" {
		t.Errorf("details = %v", got)
	}
	if _, ok := got["live"]; ok {
		t.Error("a live endpoint should carry no failure reason")
	}
}

func TestReportRoundTrips(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	original := Report{
		CheckedAt: now,
		Endpoints: map[string]Endpoint{
			"a https://a.example/mcp": {URL: "https://a.example/mcp", Alive: true, Attempts: []int{0, 0, 0}},
			"b https://b.example/mcp": {URL: "https://b.example/mcp", Alive: false, Attempts: []int{2, 2, 2}, Detail: "= 500"},
		},
	}
	path := filepath.Join(t.TempDir(), "health.json")
	if err := original.Write(path); err != nil {
		t.Fatal(err)
	}
	read := Read(path)
	if !read.CheckedAt.Equal(now) {
		t.Errorf("checkedAt = %v, want %v", read.CheckedAt, now)
	}
	if len(read.Endpoints) != 2 {
		t.Fatalf("endpoints = %d, want 2", len(read.Endpoints))
	}
	if got := read.Unreachable(now); len(got) != 1 || !got["b"] {
		t.Errorf("unreachable = %v", got)
	}
}

func TestReadingAMissingOrCorruptReportIsEmptyNotAnError(t *testing.T) {
	if got := Read(filepath.Join(t.TempDir(), "absent.json")); !got.CheckedAt.IsZero() {
		t.Error("a missing report should read as empty so the site still publishes")
	}
	corrupt := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Read(corrupt); !got.CheckedAt.IsZero() {
		t.Error("a corrupt report should read as empty, not panic or fail the build")
	}
}

// The report is committed, so its shape is part of the contract.
func TestReportSerialisesWithoutEmptyNoise(t *testing.T) {
	body, err := json.Marshal(Endpoint{URL: "https://a.example/mcp", Alive: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(body); got != `{"url":"https://a.example/mcp","alive":true,"attempts":null}` {
		t.Errorf("report shape changed: %s", got)
	}
}

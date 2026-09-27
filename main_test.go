package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seed mirrors the shapes vtessera actually returns, taken from
// internal/httpapi/server.go and internal/domain in the service repo.
const seedAgents = `{"agents":[
  {"id":"summarizer","card":{"name":"summarizer","description":"Summarises long documents.",
   "version":"0.1.0","url":"https://agents.example/summarizer","publicKey":"ed25519:abc",
   "capabilities":["text"]},"status":"active",
   "createdAt":"2026-08-01T00:00:00Z","updatedAt":"2026-09-20T00:00:00Z"},
  {"id":"translator","card":{"name":"translator","description":"Translates between 40 languages.",
   "version":"0.2.0","url":"https://agents.example/translator","publicKey":"ed25519:def",
   "capabilities":["text"]},"status":"active",
   "createdAt":"2026-08-10T00:00:00Z","updatedAt":"2026-09-25T00:00:00Z"}]}`

const seedMetrics = `{"generatedAt":"2026-09-27T00:00:00Z","asOf":"2026-09-20T00:00:00Z",
  "totals":{"delivered":9,"disputed":1,"cancelled":2,"consumers":3,"services":2},
  "agents":[{"agentId":"summarizer","delivered":9,"disputed":1,"cancelled":2},
            {"agentId":"translator","delivered":2,"disputed":0,"cancelled":0}]}`

func stubService(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/agents":
			_, _ = w.Write([]byte(seedAgents))
		case "/v1/metrics":
			_, _ = w.Write([]byte(seedMetrics))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// workspace builds a throwaway tree with one curated entry and no snapshots.
func workspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	entries := filepath.Join(dir, "content", "entries")
	if err := os.MkdirAll(entries, 0o755); err != nil {
		t.Fatal(err)
	}
	curated := `{
	  "slug": "vtessera",
	  "name": "vtessera",
	  "summary": "A2A agent marketplace.",
	  "url": "https://vtessera.example",
	  "source": "curated",
	  "last_verified": "2026-09-20T00:00:00Z"
	}`
	if err := os.WriteFile(filepath.Join(entries, "vtessera.json"), []byte(curated), 0o644); err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(dir, "assets")
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "styles.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func generate(t *testing.T, dir string, client *http.Client, baseURL string, extra ...string) error {
	t.Helper()
	cfg := config{
		baseURL:    baseURL,
		domain:     "agent-ai-tool.com",
		contentDir: filepath.Join(dir, "content", "entries"),
		cacheDir:   filepath.Join(dir, "content"),
		outDir:     filepath.Join(dir, "public"),
		assetsDir:  filepath.Join(dir, "assets"),
		timeout:    5 * time.Second,
		now:        time.Date(2026, 9, 27, 22, 30, 0, 0, time.UTC),
	}
	for _, arg := range extra {
		if arg == "-refresh" {
			cfg.refresh = true
		}
	}
	if client == nil {
		client = &http.Client{Timeout: cfg.timeout}
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	return cfg.run(ctx, client)
}

func TestEndToEndWithNoLiveServiceStillPublishes(t *testing.T) {
	dir := workspace(t)
	if err := generate(t, dir, nil, ""); err != nil {
		t.Fatalf("generate: %v", err)
	}
	for _, rel := range []string{"index.html", "agents.json", "robots.txt", "sitemap.xml", "assets/styles.css"} {
		if _, err := os.Stat(filepath.Join(dir, "public", rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
	home, err := os.ReadFile(filepath.Join(dir, "public", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(home), `class="metrics`) {
		t.Error("no metrics snapshot exists, so the banner must stay hidden")
	}
}

func TestEndToEndWithLiveServiceRendersTheBannerAndAgents(t *testing.T) {
	dir := workspace(t)
	srv := stubService(t)
	if err := generate(t, dir, srv.Client(), srv.URL); err != nil {
		t.Fatalf("generate: %v", err)
	}

	home, err := os.ReadFile(filepath.Join(dir, "public", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	// The home page carries the banner and the directory entry; the registered
	// agents belong to the vtessera page.
	for _, want := range []string{`class="metrics`, "vtessera", "source-live"} {
		if !strings.Contains(string(home), want) {
			t.Errorf("home page missing %q", want)
		}
	}

	var payload struct {
		Agents []struct {
			Slug      string `json:"slug"`
			Source    string `json:"source"`
			URL       string `json:"url"`
			Delivered *int   `json:"delivered"`
		} `json:"agents"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "public", "agents.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Agents) != 1 {
		t.Fatalf("got %d entries", len(payload.Agents))
	}
	got := payload.Agents[0]
	if got.Source != "live" {
		t.Errorf("source = %q, want live", got.Source)
	}
	// The service listing keeps its own curated endpoint; the feed's agents
	// are its tenants, not the service.
	if got.URL != "https://vtessera.example" {
		t.Errorf("url = %q, want the curated service endpoint", got.URL)
	}
	// The directory entry is the service, not an agent, so it carries no
	// per-agent total. Usage is keyed by agent ID and shows on the agent rows.
	if got.Delivered != nil {
		t.Errorf("the vtessera entry should carry no per-agent total, got %d", *got.Delivered)
	}

	page, err := os.ReadFile(filepath.Join(dir, "public", "vtessera", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"summarizer", "translator", "9 delivered"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("vtessera page missing %q", want)
		}
	}
	// translator is below the badge floor, so it gets no badge.
	if n := strings.Count(string(page), `class="badge"`); n != 1 {
		t.Errorf("want exactly one badge, the agent at or above the floor; got %d", n)
	}
}

func TestRefreshWritesSnapshotsThatTheBuildThenConsumes(t *testing.T) {
	dir := workspace(t)
	srv := stubService(t)
	if err := generate(t, dir, srv.Client(), srv.URL, "-refresh"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	for _, rel := range []string{"content/live-vtessera.json", "content/live-metrics.json"} {
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Fatalf("refresh did not write %s: %v", rel, err)
		}
	}

	// Now build with the service gone: the committed snapshots must carry it.
	if err := generate(t, dir, nil, ""); err != nil {
		t.Fatalf("generate from cache: %v", err)
	}
	home, err := os.ReadFile(filepath.Join(dir, "public", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(home), `class="metrics`) {
		t.Error("a committed snapshot should keep the banner alive without a service")
	}
}

func TestCachedSnapshotAgeIsCarriedIntoThePage(t *testing.T) {
	dir := workspace(t)
	srv := stubService(t)
	if err := generate(t, dir, srv.Client(), srv.URL, "-refresh"); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	// Age the recorded fetch time to 30 days ago, then rebuild offline.
	snapshot := filepath.Join(dir, "content", "live-metrics.json")
	raw, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		FetchedAt time.Time       `json:"fetchedAt"`
		Response  json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc.FetchedAt = time.Date(2026, 8, 28, 0, 0, 0, 0, time.UTC)
	patched, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshot, patched, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := generate(t, dir, nil, ""); err != nil {
		t.Fatalf("generate: %v", err)
	}
	home, err := os.ReadFile(filepath.Join(dir, "public", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(home), "metrics degraded") {
		t.Error("a 30-day-old snapshot should render the banner degraded")
	}
	if !strings.Contains(string(home), "30 days ago") {
		t.Error("the page should report the recorded age")
	}
}

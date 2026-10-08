package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemaio/agent-ai-tool/internal/health"
)

// captureStdout runs fn with stdout redirected, for the modes whose whole
// contract is what they print. Their output is what the scheduled workflow
// parses, so it is worth asserting rather than leaving to inspection.
func captureStdout(t *testing.T, fn func() error) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	runErr := fn()
	writer.Close()
	os.Stdout = original
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if runErr != nil {
		t.Fatalf("run: %v", runErr)
	}
	return string(body)
}

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

// seedHealth carries the key every signature on the site is checked against. The
// stub signs nothing, so this is a real key that nothing has signed with: the
// pages it builds must say unsigned rather than verified, and a test that passed
// with verified here would be asserting the opposite of the point.
const seedHealth = `{"status":"ok","version":"0.0.0-test",
  "verificationKey":"FETM34yAtPJhazZKnGu5N4bRHgugrydTBV9BAvzd7F8a","sandbox":true}`

// seedAttestation is a marketplace that recorded the card but never signed it,
// which is what a deployment without attestations looks like.
const seedAttestation = `{"agentId":"%s","recorded":false,"canonicalForm":"vtessera/attest/v1",
  "marketplace":{"attested":false},"agent":{"attested":false}}`

func stubService(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/agents":
			_, _ = w.Write([]byte(seedAgents))
		case "/v1/metrics":
			_, _ = w.Write([]byte(seedMetrics))
		case "/healthz":
			_, _ = w.Write([]byte(seedHealth))
		case "/v1/agents/summarizer/attestation", "/v1/agents/translator/attestation":
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/agents/"), "/attestation")
			_, _ = w.Write([]byte(fmt.Sprintf(seedAttestation, id)))
		case "/v1/agents/summarizer/capabilities", "/v1/agents/translator/capabilities":
			// A 404 with a code is how a marketplace says nobody has probed this
			// agent, and it must not be treated as a failure to fetch.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NOT_PROBED","error":"this agent has never been probed"}`))
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
	cfg := testConfig(t, dir)
	cfg.baseURL = baseURL
	for _, arg := range extra {
		switch arg {
		case "-refresh":
			cfg.refresh = true
		case "-check":
			cfg.check = true
		case "-review":
			cfg.review = true
		case "-commit-plan":
			cfg.commitPlan = true
		}
	}
	if client == nil {
		client = &http.Client{Timeout: cfg.timeout}
	}
	return cfg.run(client)
}

// testConfig is the single description of a test workspace, so the modes that
// need to run extra steps of their own cannot drift from the ones generate
// covers.
func testConfig(t *testing.T, dir string) config {
	t.Helper()
	return config{
		domain:      "agent-ai-tool.com",
		contentDir:  filepath.Join(dir, "content", "entries"),
		cacheDir:    filepath.Join(dir, "content"),
		outDir:      filepath.Join(dir, "public"),
		assetsDir:   filepath.Join(dir, "assets"),
		healthPath:  filepath.Join(dir, "content", "health.json"),
		changesPath: filepath.Join(dir, "content", "changes.json"),
		attempts:    1,
		timeout:     5 * time.Second,
		now:         time.Date(2026, 9, 27, 22, 30, 0, 0, time.UTC),
	}
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

// withEndpoint adds a machine endpoint to the workspace's curated entry and
// returns the URL, so a test can point it at a server that answers or not.
func withEndpoint(t *testing.T, dir, endpoint string) {
	t.Helper()
	path := filepath.Join(dir, "content", "entries", "vtessera.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	patched := strings.Replace(string(raw), `"url": "https://vtessera.example",`,
		`"url": "https://vtessera.example", "mcp_endpoint_url": "`+endpoint+`",`, 1)
	if patched == string(raw) {
		t.Fatal("could not patch the curated entry with an endpoint")
	}
	if err := os.WriteFile(path, []byte(patched), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestCheckWritesAReportAndABuildWithholdsWhatItFoundDead(t *testing.T) {
	dir := workspace(t)
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer dead.Close()
	withEndpoint(t, dir, dead.URL+"/mcp")

	// A check must not fail the build even when the endpoint is gone.
	if err := generate(t, dir, dead.Client(), "", "-check"); err != nil {
		t.Fatalf("check should exit zero with a dead endpoint: %v", err)
	}
	report := readFile(t, dir, filepath.Join("content", "health.json"))
	if !strings.Contains(report, `"alive": false`) {
		t.Errorf("report should record the endpoint as dead: %s", report)
	}

	// A later build publishes the entry without its endpoint.
	if err := generate(t, dir, nil, ""); err != nil {
		t.Fatal(err)
	}
	agents := readFile(t, dir, filepath.Join("public", "agents.json"))
	var payload struct {
		Agents []struct {
			MCPEndpointURL *string `json:"mcp_endpoint_url"`
			EndpointDown   bool    `json:"endpoint_unreachable"`
			Status         string  `json:"status"`
			ResponseMs     *int    `json:"response_ms"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(agents), &payload); err != nil {
		t.Fatal(err)
	}
	// The URL survives inside the quoted failure reason; what must not survive
	// is the mcp_endpoint_url an agent would call.
	if payload.Agents[0].MCPEndpointURL != nil {
		t.Errorf("a dead endpoint was published as callable: %q", *payload.Agents[0].MCPEndpointURL)
	}
	if !strings.Contains(agents, `"endpoint_unreachable": true`) {
		t.Errorf("agents.json should mark the entry as unreachable: %s", agents)
	}
	// Withholding is the page staying quiet; status is the same verdict said
	// out loud, so an agent that filters on it skips the entry without having
	// to notice an absent field.
	if payload.Agents[0].Status != "down" {
		t.Errorf("status = %q, want %q, the verdict the check just reached", payload.Agents[0].Status, "down")
	}
	// The 404 was an answer, so the sweep measured how long it took to get one.
	// The timing has to survive the report file on its way to agents.json or
	// the field only ever works inside a single process.
	if payload.Agents[0].ResponseMs == nil {
		t.Error("response_ms should survive the report file: a 404 is still an answer, and a measured one")
	}
}

func TestCheckThenRecoveryRepublishesTheEndpoint(t *testing.T) {
	dir := workspace(t)
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"servers":[]}`))
	}))
	defer healthy.Close()
	withEndpoint(t, dir, healthy.URL+"/mcp")

	if err := generate(t, dir, healthy.Client(), "", "-check"); err != nil {
		t.Fatal(err)
	}
	if err := generate(t, dir, nil, ""); err != nil {
		t.Fatal(err)
	}
	if agents := readFile(t, dir, filepath.Join("public", "agents.json")); !strings.Contains(agents, healthy.URL) {
		t.Error("a reachable endpoint should be published")
	}
}

// With no report committed, everything is published: the site must not suppress
// services on the strength of a check that has never run. The status says the
// same thing in the affirmative — unknown, not up — so a reader filtering on
// status is not promised a liveness nobody observed either.
func TestNoReportMeansNoWithholding(t *testing.T) {
	dir := workspace(t)
	endpoint := "https://never-checked.example/mcp"
	withEndpoint(t, dir, endpoint)
	if err := generate(t, dir, nil, ""); err != nil {
		t.Fatal(err)
	}
	if agents := readFile(t, dir, filepath.Join("public", "agents.json")); !strings.Contains(agents, endpoint) {
		t.Error("an unchecked endpoint should still be published")
	}
	agents := readFile(t, dir, filepath.Join("public", "agents.json"))
	if !strings.Contains(agents, `"status": "unknown"`) {
		t.Errorf("an unchecked entry should say its status is unknown: %s", agents)
	}
}

// The workflow parses this output, so the shape is a contract: one entry per
// line, slug and date separated by a tab, nothing at all when nothing is due.
func TestReviewListsOverdueEntriesAndNothingElse(t *testing.T) {
	dir := workspace(t)
	aged := `{
	  "slug": "aged",
	  "name": "Aged",
	  "summary": "Confirmed a long time ago.",
	  "url": "https://aged.example",
	  "source": "curated",
	  "last_verified": "2024-01-01T00:00:00Z"
	}`
	entryPath := filepath.Join(dir, "content", "entries", "aged.json")
	if err := os.WriteFile(entryPath, []byte(aged), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() error {
		return generate(t, dir, nil, "", "-review")
	})

	if !strings.Contains(out, "aged\t2024-01-01") {
		t.Errorf("the overdue entry should be listed as slug<TAB>date, got: %q", out)
	}
	// The workspace entry was confirmed a week before the build clock.
	if strings.Contains(out, "vtessera") {
		t.Errorf("an entry inside the review window should not be listed, got: %q", out)
	}
}

func TestReviewPrintsNothingWhenEverythingIsConfirmed(t *testing.T) {
	dir := workspace(t)
	out := captureStdout(t, func() error {
		return generate(t, dir, nil, "", "-review")
	})
	if strings.TrimSpace(out) != "" {
		t.Errorf("nothing is due, so nothing should be printed, got: %q", out)
	}
}

// The workflow's entire contract with this program is one word on stdout. It
// used to get that word from shell arithmetic over a JSON file, which is how a
// typo could survive six scheduled runs: nothing in the suite exercised the
// step, because the step was not Go.
func commitPlan(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(captureStdout(t, func() error {
		return generate(t, dir, nil, "", "-commit-plan")
	}))
}

func TestCommitPlanSaysSkipWhenNothingChangedAndTheReportIsFresh(t *testing.T) {
	dir := workspace(t)
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"servers":[]}`))
	}))
	defer healthy.Close()
	withEndpoint(t, dir, healthy.URL+"/mcp")

	// First check: nothing is committed yet, so it has to commit.
	if err := generate(t, dir, healthy.Client(), "", "-check"); err != nil {
		t.Fatal(err)
	}
	if got := commitPlan(t, dir); got != "commit" {
		t.Fatalf("the first report must be committed, got %q", got)
	}

	// The workflow then commits it and re-runs against the new HEAD, so the
	// previous report is the file as it stands in git.
	previous := filepath.Join(dir, "committed-health.json")
	copyFile(t, filepath.Join(dir, "content", "health.json"), previous)
	if err := generate(t, dir, healthy.Client(), "", "-check"); err != nil {
		t.Fatal(err)
	}
	if got := planWith(t, dir, previous); got != "skip" {
		t.Errorf("verdicts are unchanged and the committed report is minutes old, so this should skip; got %q", got)
	}
}

func TestCommitPlanSaysCommitWhenAnEndpointDies(t *testing.T) {
	dir := workspace(t)
	var dead bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !dead {
			_, _ = w.Write([]byte(`{"servers":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	withEndpoint(t, dir, server.URL+"/mcp")

	if err := generate(t, dir, server.Client(), "", "-check"); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(dir, "committed-health.json")
	copyFile(t, filepath.Join(dir, "content", "health.json"), previous)

	dead = true
	if err := generate(t, dir, server.Client(), "", "-check"); err != nil {
		t.Fatal(err)
	}
	if got := planWith(t, dir, previous); got != "commit" {
		t.Errorf("a demotion is the fact the site publishes, so it must commit; got %q", got)
	}
}

// Without this the report creeps past StaleAfter while nothing is wrong, and
// withholding quietly switches itself off.
func TestCommitPlanCommitsAnUnchangedReportOnceItIsOld(t *testing.T) {
	dir := workspace(t)
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"servers":[]}`))
	}))
	defer healthy.Close()
	withEndpoint(t, dir, healthy.URL+"/mcp")

	if err := generate(t, dir, healthy.Client(), "", "-check"); err != nil {
		t.Fatal(err)
	}
	previous := filepath.Join(dir, "committed-health.json")
	copyFile(t, filepath.Join(dir, "content", "health.json"), previous)

	// Age the committed copy past the refresh point without touching verdicts.
	ageReport(t, previous, time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC))

	if got := planWith(t, dir, previous); got != "commit" {
		t.Errorf("an unchanged verdict still has to be committed once the report ages, or checkedAt stops moving; got %q", got)
	}
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	body, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func ageReport(t *testing.T, path string, at time.Time) {
	t.Helper()
	var doc struct {
		CheckedAt time.Time                  `json:"checkedAt"`
		Endpoints map[string]health.Endpoint `json:"endpoints"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	doc.CheckedAt = at
	patched, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(patched, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// planWith runs the decision against a specific previous report, which is how
// the workflow supplies the copy it restored from git.
func planWith(t *testing.T, dir, previous string) string {
	t.Helper()
	return strings.TrimSpace(captureStdout(t, func() error {
		cfg := testConfig(t, dir)
		cfg.commitPlan = true
		cfg.previous = previous
		return cfg.run(&http.Client{Timeout: cfg.timeout})
	}))
}

// The change feed's contract, through the same path a contributor's
// `make generate` takes: two consecutive builds with a real difference produce
// one correct record, and the committed file a reader fetches carries it so
// the next build starts from here instead of from nothing.
func TestTwoBuildsWithARealDifferenceProduceAFeedEntry(t *testing.T) {
	dir := workspace(t)
	// A second entry, so the one record can name all three kinds of
	// difference at once: something arriving, something leaving, and something
	// that stayed but now publishes a different value.
	leaving := `{
	  "slug": "registry",
	  "name": "registry",
	  "summary": "A registry.",
	  "url": "https://registry.example",
	  "source": "curated",
	  "last_verified": "2026-09-20T00:00:00Z"
	}`
	if err := os.WriteFile(filepath.Join(dir, "content", "entries", "registry.json"), []byte(leaving), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := generate(t, dir, nil, ""); err != nil {
		t.Fatal(err)
	}

	curated := filepath.Join(dir, "content", "entries", "vtessera.json")
	raw, err := os.ReadFile(curated)
	if err != nil {
		t.Fatal(err)
	}
	reworded := strings.Replace(string(raw),
		`"summary": "A2A agent marketplace."`,
		`"summary": "A2A agent marketplace, reworded."`, 1)
	if reworded == string(raw) {
		t.Fatal("could not reword the curated entry")
	}
	if err := os.WriteFile(curated, []byte(reworded), 0o644); err != nil {
		t.Fatal(err)
	}
	arriving := `{
	  "slug": "catalog",
	  "name": "catalog",
	  "summary": "A catalog.",
	  "url": "https://catalog.example",
	  "source": "curated",
	  "last_verified": "2026-09-20T00:00:00Z"
	}`
	if err := os.WriteFile(filepath.Join(dir, "content", "entries", "catalog.json"), []byte(arriving), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "content", "entries", "registry.json")); err != nil {
		t.Fatal(err)
	}

	if err := generate(t, dir, nil, ""); err != nil {
		t.Fatal(err)
	}

	var feed struct {
		SchemaVersion int `json:"schemaVersion"`
		Records       []struct {
			Added   []string `json:"added"`
			Removed []string `json:"removed"`
			Changed []struct {
				Slug   string   `json:"slug"`
				Fields []string `json:"fields"`
			} `json:"changed"`
		} `json:"records"`
	}
	if err := json.Unmarshal([]byte(readFile(t, dir, filepath.Join("public", "changes.json"))), &feed); err != nil {
		t.Fatal(err)
	}
	if feed.SchemaVersion != 1 {
		t.Errorf("schemaVersion = %d, want 1", feed.SchemaVersion)
	}
	if len(feed.Records) != 2 {
		t.Fatalf("published %d records, want the bootstrap record and this build's", len(feed.Records))
	}
	rec := feed.Records[0]
	if strings.Join(rec.Added, ",") != "catalog" {
		t.Errorf("added = %v, want the entry that arrived", rec.Added)
	}
	if strings.Join(rec.Removed, ",") != "registry" {
		t.Errorf("removed = %v, want the entry that left", rec.Removed)
	}
	if len(rec.Changed) != 1 || rec.Changed[0].Slug != "vtessera" ||
		strings.Join(rec.Changed[0].Fields, ",") != "summary" {
		t.Errorf("changed = %+v, want vtessera's summary", rec.Changed)
	}
	if boot := feed.Records[1].Added; strings.Join(boot, ",") != "registry,vtessera" {
		t.Errorf("bootstrap added = %v, want both entries that existed then", boot)
	}

	committed := readFile(t, dir, filepath.Join("content", "changes.json"))
	if !strings.Contains(committed, `"catalog"`) || !strings.Contains(committed, `"registry"`) {
		t.Error("the committed feed does not carry the record the reader fetches")
	}
}

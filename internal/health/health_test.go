package health

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
		{Slug: "e", APIURL: ptr("https://e.example/api.json")},
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
	// A plain API is as callable as an MCP endpoint and as worth probing: it is
	// what the directory tells an agent to call, so a JSON API nobody checks is
	// a URL the site would keep publishing after it stopped answering.
	if len(targets["e"]) != 1 || targets["e"][0] != "https://e.example/api.json" {
		t.Errorf("api_url targets = %v, want the plain API probed", targets["e"])
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

	report := Check(context.Background(), []content.Entry{entry("a", server.URL)}, Report{}, server.Client(), Options{Attempts: 3, Interval: time.Millisecond}, time.Now())
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

	report := Check(context.Background(), []content.Entry{entry("a", server.URL)}, Report{}, server.Client(), Options{Attempts: 3, Interval: time.Millisecond}, time.Now())
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
	report := Check(context.Background(), []content.Entry{entry("a", "http://127.0.0.1:1/mcp")}, Report{},
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

	report := Check(context.Background(), []content.Entry{entry("a", server.URL)}, Report{}, server.Client(), Options{Attempts: 1, Interval: time.Millisecond}, time.Now())
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

func verdicts(alive ...string) Report {
	r := Report{Endpoints: map[string]Endpoint{}}
	for _, key := range alive {
		r.Endpoints[key] = Endpoint{URL: key, Alive: true}
	}
	return r
}

func TestAFirstReportIsAlwaysCommitted(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	if got := Decide(Report{}, verdicts("a https://a.example/mcp"), now); got != Commit {
		t.Errorf("with nothing committed there is nothing to be unchanged from, so the first report must commit; got %q", got)
	}
}

// This is the whole point of the decision: checkedAt moves every run, so
// committing unconditionally rebuilds the site four times a day to say nothing.
func TestAnUnchangedVerdictOnAFreshReportIsSkipped(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	committed := verdicts("a https://a.example/mcp", "b https://b.example/mcp")
	committed.CheckedAt = now.Add(-time.Hour)

	fresh := verdicts("a https://a.example/mcp", "b https://b.example/mcp")
	fresh.CheckedAt = now

	if got := Decide(committed, fresh, now); got != Skip {
		t.Errorf("nothing changed and the committed report is an hour old, so this should be skipped; got %q", got)
	}
}

func TestAnEndpointGoingDeadIsCommitted(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	committed := verdicts("a https://a.example/mcp", "b https://b.example/mcp")
	committed.CheckedAt = now.Add(-time.Hour)

	fresh := verdicts("a https://a.example/mcp")
	fresh.Endpoints["b https://b.example/mcp"] = Endpoint{URL: "b https://b.example/mcp", Alive: false}

	if got := Decide(committed, fresh, now); got != Commit {
		t.Errorf("a demotion is the fact the directory publishes, so it must commit; got %q", got)
	}
}

func TestARecoveredEndpointIsCommitted(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	committed := Report{Endpoints: map[string]Endpoint{
		"a https://a.example/mcp": {URL: "a https://a.example/mcp", Alive: false},
	}}
	committed.CheckedAt = now.Add(-time.Hour)

	if got := Decide(committed, verdicts("a https://a.example/mcp"), now); got != Commit {
		t.Errorf("restoring a withheld endpoint must commit, or the directory would stay wrong; got %q", got)
	}
}

// Without this, a directory that is quietly healthy drifts past StaleAfter and
// withholding switches itself off while every endpoint stays up, so the check
// that is still running stops being the check that is being read.
func TestAnUnchangedVerdictIsCommittedOnceTheReportAges(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	committed := verdicts("a https://a.example/mcp")
	committed.CheckedAt = now.Add(-RefreshAfter - time.Minute)

	if got := Decide(committed, verdicts("a https://a.example/mcp"), now); got != Commit {
		t.Errorf("a report older than %s must be committed to keep checkedAt moving; got %q", RefreshAfter, got)
	}
}

func TestAReportStampedInTheFutureIsReplaced(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	committed := verdicts("a https://a.example/mcp")
	committed.CheckedAt = now.Add(time.Hour)

	if got := Decide(committed, verdicts("a https://a.example/mcp"), now); got != Commit {
		t.Errorf("a future timestamp cannot be aged, and trusting it would postpone the next real check; got %q", got)
	}
}

// One probe of three failing still leaves the endpoint alive, so a re-run that
// happened to lose the same probe has not changed anything the site publishes.
func TestAttemptNoiseIsNotAVerdictChange(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	committed := Report{Endpoints: map[string]Endpoint{
		"a https://a.example/mcp": {URL: "a https://a.example/mcp", Alive: true, Attempts: []int{0, 0, 0}},
	}}
	committed.CheckedAt = now.Add(-time.Hour)

	fresh := Report{Endpoints: map[string]Endpoint{
		"a https://a.example/mcp": {URL: "a https://a.example/mcp", Alive: true, Attempts: []int{0, 2, 0}},
	}}

	if got := Decide(committed, fresh, now); got != Skip {
		t.Errorf("the same verdict with different attempt codes is not a change worth a rebuild; got %q", got)
	}
}

func TestANewlyCuratedEndpointIsCommitted(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	committed := verdicts("a https://a.example/mcp")
	committed.CheckedAt = now.Add(-time.Hour)

	fresh := verdicts("a https://a.example/mcp", "new https://new.example/mcp")
	if got := Decide(committed, fresh, now); got != Commit {
		t.Errorf("a newly advertised endpoint has never been reported on, so it must commit; got %q", got)
	}
}

// The explanation is what an operator reads in the log, so it has to name the
// rule rather than restate the verdict.
func TestEveryDecisionRuleIsExplained(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	fresh := verdicts("a https://a.example/mcp")

	none := Report{}
	aged := verdicts("a https://a.example/mcp")
	aged.CheckedAt = now.Add(-RefreshAfter - time.Hour)
	future := verdicts("a https://a.example/mcp")
	future.CheckedAt = now.Add(time.Hour)
	unchanged := verdicts("a https://a.example/mcp")
	unchanged.CheckedAt = now.Add(-time.Hour)

	cases := []struct {
		name      string
		committed Report
		want      string
	}{
		{"no committed report", none, "establishes one"},
		{"aged report", aged, "keep checkedAt moving"},
		{"future report", future, "cannot be trusted"},
		{"unchanged", unchanged, "would rebuild the site"},
	}
	for _, tc := range cases {
		got := Explain(tc.committed, Decide(tc.committed, fresh, now), now)
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: explanation %q does not mention %q", tc.name, got, tc.want)
		}
	}
}

// A budget that expires mid-endpoint must not throw away what the probes
// already proved. The early return this replaced recorded two successful
// probes and then reported the endpoint dead anyway, which is how a live
// service came off the directory.
func TestAnEndpointThatAnsweredBeforeTheBudgetRanOutIsAlive(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// The second attempt waits out its interval, so cancelling well before
	// that interval ends means the budget is unambiguously spent when the
	// select runs rather than a coin flip between two ready cases.
	time.AfterFunc(50*time.Millisecond, cancel)

	report := Check(ctx, []content.Entry{entry("a", server.URL)}, Report{}, server.Client(),
		Options{Attempts: 3, Interval: 500 * time.Millisecond, Timeout: 2 * time.Second}, time.Now())

	got := report.Endpoints["a "+server.URL]
	if !got.Alive {
		t.Errorf("alive = false after %d successful probe(s); the partial verdict was discarded", calls)
	}
	if len(got.Attempts) != 1 {
		t.Errorf("attempts = %v, want the one probe that completed before the budget expired", got.Attempts)
	}
}

// One endpoint running out of time is not evidence about the next one. The
// budget is the sum of the per-endpoint budgets, because the sweep runs them
// one after another and each has to get the attempts it was configured for.
func TestTheSweepBudgetGivesEveryEndpointItsOwnAttempts(t *testing.T) {
	targets := map[string][]string{"a": {"https://a.example/mcp", "https://a.example/card"}, "b": {"https://b.example/mcp"}}
	opts := Options{Attempts: 3, Timeout: 20 * time.Second, Interval: 2 * time.Second}
	perEndpoint := time.Duration(opts.Attempts)*(opts.Timeout+opts.Interval) + opts.Interval

	if got, want := Budget(targets, opts), 3*perEndpoint+opts.Interval; got != want {
		t.Errorf("budget = %s, want %s for three endpoints", got, want)
	}
	// The defect this replaces: one 20s deadline for the whole sweep, which
	// the interval sleeps alone could not fit inside.
	if got := Budget(targets, opts); got <= time.Duration(3)*opts.Timeout {
		t.Errorf("budget = %s, want more than one shared request timeout for three endpoints", got)
	}
	if got := Budget(map[string][]string{}, opts); got != perEndpoint {
		t.Errorf("a sweep with nothing to probe budgets %s, want one endpoint's worth", got)
	}
}

func TestAWithheldEndpointReportsWhenItWasLastAlive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	previousAt := time.Date(2026, 10, 5, 14, 54, 0, 0, time.UTC)
	previous := Report{CheckedAt: previousAt, Endpoints: map[string]Endpoint{
		"a " + server.URL: {URL: server.URL, Alive: true},
	}}

	report := Check(context.Background(), []content.Entry{entry("a", server.URL)}, previous, server.Client(),
		Options{Attempts: 1, Interval: time.Millisecond}, time.Now())

	got := report.Endpoints["a "+server.URL]
	if got.Alive {
		t.Fatal("an endpoint answering 404 is not alive")
	}
	if got.LastAliveAt == nil || !got.LastAliveAt.Equal(previousAt) {
		t.Errorf("last_alive_at = %v, want %s, the report that last saw it up", got.LastAliveAt, previousAt)
	}
}

func TestAnOutageThatContinuesDoesNotMoveTheDateItBegan(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	outageStart := time.Date(2026, 10, 5, 14, 54, 0, 0, time.UTC)
	previous := Report{CheckedAt: outageStart, Endpoints: map[string]Endpoint{
		"a " + server.URL: {URL: server.URL, Alive: false, LastAliveAt: &outageStart},
	}}

	report := Check(context.Background(), []content.Entry{entry("a", server.URL)}, previous, server.Client(),
		Options{Attempts: 1, Interval: time.Millisecond}, time.Now())

	got := report.Endpoints["a "+server.URL]
	if got.LastAliveAt == nil || !got.LastAliveAt.Equal(outageStart) {
		t.Errorf("last_alive_at = %v, want %s, the date the outage began rather than this run", got.LastAliveAt, outageStart)
	}
}

func TestLastAliveNamesOnlyEndpointsThatHaveAnswered(t *testing.T) {
	seen := time.Date(2026, 10, 5, 14, 54, 0, 0, time.UTC)
	report := Report{CheckedAt: time.Now(), Endpoints: map[string]Endpoint{
		"a https://a.example/mcp": {Alive: true},
		"b https://b.example/mcp": {Alive: false, LastAliveAt: &seen},
		"c https://c.example/mcp": {Alive: false},
	}}

	got := report.LastAlive()
	if len(got) != 1 {
		t.Fatalf("LastAlive() = %v, want only the endpoint with a record", got)
	}
	if !got["b"].Equal(seen) {
		t.Errorf("LastAlive()[b] = %v, want %s", got["b"], seen)
	}
	if _, ok := got["c"]; ok {
		t.Error("an endpoint that has never answered should have no date rather than a zero one")
	}
}

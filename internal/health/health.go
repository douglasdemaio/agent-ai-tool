// Package health checks that the endpoints a curated entry advertises still
// answer.
//
// The directory's whole claim is that these are endpoints an agent can call. A
// URL that has rotted is worse than no entry, because the site keeps telling an
// agent to call it. Nothing else in the build notices: content validation only
// proves a URL is well formed, never that it resolves.
//
// Three properties are deliberate. The check never fails the build, because a
// third party's outage must not stop the site publishing what is still true. A
// single failed probe does not demote an entry either, since one of these
// services answering 50% of the time is a real condition and demoting on it
// would flap the directory nightly. And it only ever demotes: a recovered
// endpoint is restored, never left suppressed.
package health

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/douglasdemaio/agent-ai-tool/internal/content"
)

const (
	// DefaultAttempts is how many times one endpoint is probed before it is
	// judged. Three is the smallest number where a majority can outvote one
	// transient failure.
	DefaultAttempts = 3
	// DefaultInterval separates probes of a single endpoint.
	DefaultInterval = 2 * time.Second
	// StaleAfter is how long a report stays trusted. Past this the directory
	// would be demoting on memory of a check rather than on evidence.
	StaleAfter = 48 * time.Hour
	// RefreshAfter is how old a committed report may get before an unchanged
	// verdict is committed again anyway, so that checkedAt keeps moving and the
	// report never drifts past StaleAfter while nothing is actually wrong. It is
	// half StaleAfter, so one missed run cannot push a healthy directory over
	// the line where it stops withholding dead endpoints.
	RefreshAfter = 24 * time.Hour
)

// Report is the committed result of a check. It records reachability only, so
// it stays small and its diff is readable in a commit.
type Report struct {
	CheckedAt time.Time           `json:"checkedAt"`
	Endpoints map[string]Endpoint `json:"endpoints"`
}

type Endpoint struct {
	URL string `json:"url"`
	// Alive is the majority verdict across this entry's attempts.
	Alive bool `json:"alive"`
	// Attempts records each probe so a single unlucky run is visible rather
	// than hidden behind a boolean.
	Attempts []int `json:"attempts"`
	// Detail is the last failure reason, kept so a demotion can be explained
	// without re-running the check.
	Detail string `json:"detail,omitempty"`
	// LastAliveAt is when this endpoint was last judged alive, and it is
	// written only while the endpoint is down. An alive endpoint's answer is
	// checkedAt already, so recording it again would be noise; what has no
	// other source is how long a service has been withheld, which is the fact
	// a reader actually wants next to the verdict.
	//
	// It is carried forward from the previous report rather than taken from
	// this run, so the date stays put while the outage continues instead of
	// resetting to the check that keeps noticing it.
	LastAliveAt *time.Time `json:"last_alive_at,omitempty"`
	// ResponseMs is how long the service took to answer, in milliseconds: the
	// median of this run's attempts that got an HTTP response, so one unlucky
	// probe does not stand for the service and neither does the luckiest. It
	// is absent when no attempt got an answer, because a timeout is not a
	// response time and reporting one would make a dead endpoint look slow
	// rather than gone.
	ResponseMs *int `json:"response_ms,omitempty"`
}

// Status is one probe's result. A 429 counts as alive: the service is there and
// is declining this client, which is not the same as the endpoint being gone.
type Status int

const (
	StatusAlive Status = iota
	StatusRateLimited
	StatusFailed
)

func (s Status) alive() bool { return s == StatusAlive || s == StatusRateLimited }

func (s Status) code() int {
	switch s {
	case StatusAlive:
		return 0
	case StatusRateLimited:
		return 1
	default:
		return 2
	}
}

type Options struct {
	Attempts int
	Interval time.Duration
	Timeout  time.Duration
}

func (o Options) withDefaults() Options {
	if o.Attempts < 1 {
		o.Attempts = DefaultAttempts
	}
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.Timeout <= 0 {
		o.Timeout = 20 * time.Second
	}
	return o
}

// budget is how long one endpoint may take: every attempt, every interval
// between them, and a slack of one interval so the last attempt never starts
// on the edge of the deadline it is measured against.
func (o Options) budget() time.Duration {
	return time.Duration(o.Attempts)*(o.Timeout+o.Interval) + o.Interval
}

// Budget is how long an entire sweep may take. It is the sum of the per
// endpoint budgets, because the sweep runs them one after another and the only
// thing a total deadline is for is refusing to run forever if the per-request
// timeouts stop applying.
//
// It must never be sized to one request. The defect this replaces was exactly
// that: the whole program shared a single deadline equal to the per-request
// timeout, the earlier endpoints spent it, and the last endpoint in the sweep
// was reported dead for running out of time it was never given.
func Budget(targets map[string][]string, opts Options) time.Duration {
	opts = opts.withDefaults()
	n := 0
	for _, urls := range targets {
		n += len(urls)
	}
	if n == 0 {
		return opts.budget()
	}
	return time.Duration(n)*opts.budget() + opts.Interval
}

// Targets returns the endpoints worth probing, keyed by slug. A curated entry
// may carry both a machine endpoint and an agent card, and both are things an
// agent is told to fetch.
//
// The entry's homepage is deliberately not probed. It is a human destination
// that an agent has no reason to call, and a site that 403s a bare user agent
// would demote itself for serving exactly the right page.
func Targets(entries []content.Entry) map[string][]string {
	targets := make(map[string][]string, len(entries))
	for _, e := range entries {
		var urls []string
		if e.MCPEndpointURL != nil {
			urls = append(urls, *e.MCPEndpointURL)
		}
		if e.APIURL != nil {
			urls = append(urls, *e.APIURL)
		}
		if e.AgentCardURL != nil {
			urls = append(urls, *e.AgentCardURL)
		}
		if len(urls) > 0 {
			targets[e.Slug] = urls
		}
	}
	return targets
}

// Check probes every target and returns the report. It always returns a report
// even when every probe failed: the absence of a signal is not evidence that
// the directory is broken, and a check that could not reach anything must not
// be mistaken for a directory of dead links.
//
// previous is the report already committed, used only to carry forward when
// each endpoint was last alive. It is not consulted for verdicts: a verdict
// always comes from this run's probes, and reading one out of the previous
// report would make a report that can never contradict itself.
//
// at is the time to stamp the report, supplied by the caller rather than read
// from the clock so a build with a pinned generation time stays internally
// consistent with the report it reads back.
func Check(ctx context.Context, entries []content.Entry, previous Report, client *http.Client, opts Options, at time.Time) Report {
	opts = opts.withDefaults()
	report := Report{
		CheckedAt: at.UTC(),
		Endpoints: make(map[string]Endpoint),
	}

	targets := Targets(entries)
	slugs := make([]string, 0, len(targets))
	for slug := range targets {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	for _, slug := range slugs {
		for _, raw := range targets[slug] {
			key := slug + " " + raw
			result := probe(ctx, client, raw, opts)
			if !result.Alive {
				result.LastAliveAt = previous.lastAlive(key)
			}
			report.Endpoints[key] = result
		}
	}
	return report
}

// lastAlive answers "when was this endpoint last seen up?", from the report the
// caller already holds. An endpoint that was alive in the previous report was
// last alive at that report's checkedAt; one that was already down has the
// date the outage began, carried through every check since; one with no
// previous record has never answered, which is a different statement and is
// left nil so the page can say it.
func (r Report) lastAlive(key string) *time.Time {
	prev, ok := r.Endpoints[key]
	if !ok {
		return nil
	}
	if prev.Alive {
		when := r.CheckedAt
		return &when
	}
	return prev.LastAliveAt
}

// LastAlive returns when each withheld endpoint was last judged alive, keyed by
// slug. Entries absent from the map have never answered a check, which is what
// the page says instead of showing an empty date.
func (r Report) LastAlive() map[string]time.Time {
	var out map[string]time.Time
	for key, e := range r.Endpoints {
		if e.Alive || e.LastAliveAt == nil {
			continue
		}
		slug, _, found := splitKey(key)
		if !found {
			continue
		}
		if out == nil {
			out = make(map[string]time.Time)
		}
		out[slug] = *e.LastAliveAt
	}
	return out
}

// probe judges one endpoint. It runs under its own deadline, covering its own
// attempts and intervals, so how much time the earlier endpoints of the sweep
// took has no bearing on this one.
func probe(ctx context.Context, client *http.Client, url string, opts Options) Endpoint {
	result := Endpoint{URL: url}
	epCtx, cancel := context.WithTimeout(ctx, opts.budget())
	defer cancel()
	var answeredTimes []int

loop:
	for i := 0; i < opts.Attempts; i++ {
		if i > 0 {
			select {
			case <-epCtx.Done():
				// The budget ran out between attempts. What has already been
				// probed still stands: falling through to the majority below
				// keeps a partial run honest, and the early return this
				// replaced discarded two successful probes as though they
				// had never happened.
				if result.Detail == "" {
					result.Detail = "the endpoint's probe budget ran out"
				}
				break loop
			case <-time.After(opts.Interval):
			}
		}
		start := time.Now()
		status, detail, answered := attempt(epCtx, client, url, opts.Timeout)
		if answered {
			took := int(time.Since(start) / time.Millisecond)
			answeredTimes = append(answeredTimes, took)
		}
		result.Attempts = append(result.Attempts, status.code())
		if detail != "" {
			result.Detail = detail
		}
	}
	if len(answeredTimes) > 0 {
		sort.Ints(answeredTimes)
		median := answeredTimes[len(answeredTimes)/2]
		result.ResponseMs = &median
	}
	alive := 0
	for _, code := range result.Attempts {
		if code != StatusFailed.code() {
			alive++
		}
	}
	// Zero attempts counts as dead: nothing was proven either way, and this
	// endpoint has to be reported by something.
	result.Alive = len(result.Attempts) > 0 && alive*2 > len(result.Attempts)
	if result.Alive {
		result.Detail = ""
	}
	return result
}

// attempt probes once. The bool reports whether an HTTP response arrived at
// all, which is a different fact from whether it was a good one: a 404 is an
// answer and counts for the response time, a connection refused is not.
func attempt(ctx context.Context, client *http.Client, url string, timeout time.Duration) (Status, string, bool) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return StatusFailed, err.Error(), false
	}
	req.Header.Set("Accept", "application/json")
	// Identify the check honestly so an operator reading upstream logs knows
	// who is calling and why.
	req.Header.Set("User-Agent", "agent-ai-tool-health/1.0 (+https://agent-ai-tool.com)")

	resp, err := client.Do(req)
	if err != nil {
		return StatusFailed, err.Error(), false
	}
	defer resp.Body.Close()
	// The body is drained and closed so the connection can be reused; its
	// contents are irrelevant, only the status line is being judged.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return StatusAlive, "", true
	case resp.StatusCode == http.StatusTooManyRequests:
		return StatusRateLimited, fmt.Sprintf("GET %s = 429", url), true
	default:
		return StatusFailed, fmt.Sprintf("GET %s = %d", url, resp.StatusCode), true
	}
}

// Write persists the report, creating the directory if needed.
func (r Report) Write(path string) error {
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o644)
}

// Decision is the answer to "is a freshly written report worth committing?".
type Decision string

const (
	// Commit means the report should be committed: a verdict changed, or the
	// committed one has aged enough that checkedAt needs to move.
	Commit Decision = "commit"
	// Skip means the verdicts are unchanged and the committed report is still
	// fresh, so committing would rebuild the site to record a new timestamp and
	// nothing else.
	Skip Decision = "skip"
)

// Decide reports whether a fresh check result should replace a committed one.
//
// checkedAt advances on every run, so a workflow that committed unconditionally
// would rebuild the site four times a day to say nothing. What carries meaning
// is the verdict per endpoint. A changed verdict is always worth a commit,
// because that is the fact the directory publishes. An unchanged verdict is
// still committed once the committed report is older than RefreshAfter, so a
// directory that is quietly healthy does not eventually stop being checked at
// all: the report would otherwise creep past StaleAfter and withholding would
// silently switch itself off while every endpoint stayed up.
//
// A committed report stamped in the future is treated as absent. Its age cannot
// be trusted, and trusting it would let a clock-skewed report postpone the next
// real check indefinitely.
func Decide(committed, fresh Report, now time.Time) Decision {
	age := now.Sub(committed.CheckedAt)
	if committed.CheckedAt.IsZero() || age < 0 || age > RefreshAfter {
		return Commit
	}
	if !sameVerdict(committed, fresh) {
		return Commit
	}
	return Skip
}

// Explain renders the reasoning behind a decision, for the build log. The
// workflow acts on the Decision itself; this is so a run that decides to commit
// or skip says which rule applied rather than leaving an operator to infer it.
func Explain(committed Report, decision Decision, now time.Time) string {
	age := now.Sub(committed.CheckedAt)
	switch {
	case committed.CheckedAt.IsZero():
		return "there is no committed report, so this run establishes one"
	case age < 0:
		return fmt.Sprintf("the committed report is stamped %s in the future, so its age cannot be trusted", -age)
	case age > RefreshAfter:
		return fmt.Sprintf("the committed report is %s old, past the %s refresh point, so it is committed to keep checkedAt moving", age.Round(time.Hour), RefreshAfter)
	case decision == Skip:
		return fmt.Sprintf("every verdict is unchanged and the committed report is only %s old, so committing would rebuild the site to record a timestamp", age.Round(time.Minute))
	default:
		return "at least one endpoint changed verdict"
	}
}

// sameVerdict compares only whether each endpoint was judged alive. Attempt
// counts and failure detail are deliberately excluded: a service that failed
// one probe of three is alive either way, and a new reason string for an
// endpoint whose verdict did not move is not a change the site acts on.
func sameVerdict(a, b Report) bool {
	if len(a.Endpoints) != len(b.Endpoints) {
		return false
	}
	for key, left := range a.Endpoints {
		right, found := b.Endpoints[key]
		if !found || left.Alive != right.Alive {
			return false
		}
	}
	return true
}

// Read loads a committed report. A missing or corrupt file is not an error the
// caller must handle: it means no check has run, and the correct response is to
// publish everything rather than to demote the whole directory.
func Read(path string) Report {
	var report Report
	raw, err := os.ReadFile(path)
	if err != nil {
		return Report{}
	}
	if err := json.Unmarshal(raw, &report); err != nil {
		return Report{}
	}
	return report
}

// Fresh reports whether the report is still trusted as evidence about the
// present: stamped in the past and younger than StaleAfter.
//
// Everything that publishes a verdict — which endpoints are withheld, when an
// entry was last checked, what status it carries — asks this first. A report
// that fails it is history: it still says what was true when it was written,
// and must not keep speaking for now.
func (r Report) Fresh(now time.Time) bool {
	// A report stamped in the future is not evidence about the present. This
	// happens for real, not only in tests: the report is written by one build
	// and read by another whose clock differs, or by a run using a pinned
	// generation time. Treating it as fresh would let a report be trusted
	// indefinitely, so it is discarded like a stale one.
	age := now.Sub(r.CheckedAt)
	return !r.CheckedAt.IsZero() && age >= 0 && age <= StaleAfter
}

// Unreachable lists the entries whose advertised endpoints are currently judged
// dead. Only endpoints proven dead by a report that is still Fresh count: a
// stale or absent report demotes nothing, because the site should not keep
// suppressing a service on the strength of a check it no longer trusts.
func (r Report) Unreachable(now time.Time) map[string]bool {
	if !r.Fresh(now) {
		return nil
	}
	out := make(map[string]bool)
	for key, e := range r.Endpoints {
		if e.Alive {
			continue
		}
		if slug, _, found := splitKey(key); found {
			out[slug] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ResponseTimes reads how long each entry's machine endpoint took to answer,
// keyed by slug. The machine endpoint is the one an agent would call — an MCP
// endpoint when the entry publishes one, otherwise the plain API — and the
// agent card is deliberately not a candidate: how fast a card answers says
// nothing about how fast the service does.
//
// Entries absent from the map published no measurable answer: no endpoint, no
// probe, or one that never got a response. That is the same statement the
// absence of the field makes in agents.json.
func (r Report) ResponseTimes(entries []content.Entry) map[string]int {
	var out map[string]int
	for _, e := range entries {
		u := e.MachineEndpoint()
		if u == nil {
			continue
		}
		endpoint, ok := r.Endpoints[e.Slug+" "+*u]
		if !ok || endpoint.ResponseMs == nil {
			continue
		}
		if out == nil {
			out = make(map[string]int)
		}
		out[e.Slug] = *endpoint.ResponseMs
	}
	return out
}

// Details returns the last failure reason per slug, so a withheld endpoint can
// be explained on the page that withholds it.
func (r Report) Details() map[string]string {
	var out map[string]string
	for key, e := range r.Endpoints {
		if e.Alive || e.Detail == "" {
			continue
		}
		slug, _, found := splitKey(key)
		if !found {
			continue
		}
		if out == nil {
			out = make(map[string]string)
		}
		// A slug with two dead endpoints keeps the first reason rather than an
		// arbitrary one, so the page does not imply the pair is interchangeable.
		if _, seen := out[slug]; !seen {
			out[slug] = e.Detail
		}
	}
	return out
}

func splitKey(key string) (slug, url string, found bool) {
	for i := 0; i < len(key); i++ {
		if key[i] == ' ' {
			return key[:i], key[i+1:], true
		}
	}
	return key, "", false
}

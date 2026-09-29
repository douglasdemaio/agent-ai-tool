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
// at is the time to stamp the report, supplied by the caller rather than read
// from the clock so a build with a pinned generation time stays internally
// consistent with the report it reads back.
func Check(ctx context.Context, entries []content.Entry, client *http.Client, opts Options, at time.Time) Report {
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
			report.Endpoints[key] = probe(ctx, client, raw, opts)
		}
	}
	return report
}

func probe(ctx context.Context, client *http.Client, url string, opts Options) Endpoint {
	result := Endpoint{URL: url}
	for i := 0; i < opts.Attempts; i++ {
		if i > 0 {
			select {
			case <-ctx.Done():
				result.Detail = "context cancelled between attempts"
				result.Attempts = append(result.Attempts, StatusFailed.code())
				return result
			case <-time.After(opts.Interval):
			}
		}
		status, detail := attempt(ctx, client, url, opts.Timeout)
		result.Attempts = append(result.Attempts, status.code())
		if detail != "" {
			result.Detail = detail
		}
	}
	alive := 0
	for _, code := range result.Attempts {
		if code != StatusFailed.code() {
			alive++
		}
	}
	result.Alive = alive*2 > len(result.Attempts)
	if result.Alive {
		result.Detail = ""
	}
	return result
}

func attempt(ctx context.Context, client *http.Client, url string, timeout time.Duration) (Status, string) {
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return StatusFailed, err.Error()
	}
	req.Header.Set("Accept", "application/json")
	// Identify the check honestly so an operator reading upstream logs knows
	// who is calling and why.
	req.Header.Set("User-Agent", "agent-ai-tool-health/1.0 (+https://agent-ai-tool.com)")

	resp, err := client.Do(req)
	if err != nil {
		return StatusFailed, err.Error()
	}
	defer resp.Body.Close()
	// The body is drained and closed so the connection can be reused; its
	// contents are irrelevant, only the status line is being judged.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return StatusAlive, ""
	case resp.StatusCode == http.StatusTooManyRequests:
		return StatusRateLimited, fmt.Sprintf("GET %s = 429", url)
	default:
		return StatusFailed, fmt.Sprintf("GET %s = %d", url, resp.StatusCode)
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

// Unreachable lists the entries whose advertised endpoints are currently judged
// dead. Only endpoints proven dead by a report newer than StaleAfter count; a
// stale or absent report demotes nothing, because the site should not keep
// suppressing a service on the strength of a check it no longer trusts.
func (r Report) Unreachable(now time.Time) map[string]bool {
	// A report stamped in the future is not evidence about the present. This
	// happens for real, not only in tests: the report is written by one build
	// and read by another whose clock differs, or by a run using a pinned
	// generation time. Treating it as fresh would let a report be trusted
	// indefinitely, so it is discarded like a stale one.
	age := now.Sub(r.CheckedAt)
	if r.CheckedAt.IsZero() || age < 0 || age > StaleAfter {
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

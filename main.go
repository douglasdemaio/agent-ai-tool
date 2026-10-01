// Command agent-ai-tool generates the static site for agent-ai-tool.com.
//
// It loads curated entries from content/entries, optionally merges live vtessera
// data fetched at build time, and renders the result into public/. It depends on
// nothing outside the standard library and requires no runtime service: when
// vtessera is unreachable the site falls back to the committed snapshot and
// still publishes.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/douglasdemaio/agent-ai-tool/internal/content"
	"github.com/douglasdemaio/agent-ai-tool/internal/health"
	"github.com/douglasdemaio/agent-ai-tool/internal/live"
	"github.com/douglasdemaio/agent-ai-tool/internal/render"
)

const defaultDomain = "agent-ai-tool.com"

type config struct {
	baseURL    string
	domain     string
	contentDir string
	cacheDir   string
	outDir     string
	assetsDir  string
	refresh    bool
	check      bool
	review     bool
	commitPlan bool
	healthPath string
	previous   string
	attempts   int
	timeout    time.Duration
	now        time.Time
}

func main() {
	if err := run(); err != nil {
		log.Fatalf("agent-ai-tool: %v", err)
	}
}

func run() error {
	var (
		baseURL    = flag.String("base-url", os.Getenv("VTESSERA_BASE_URL"), "vtessera base URL; empty means the live section is absent")
		domain     = flag.String("domain", defaultDomain, "apex domain for canonical URLs and CNAME")
		contentDir = flag.String("content", "content/entries", "directory of curated entry JSON")
		cacheDir   = flag.String("cache", "content", "directory holding the committed live snapshots")
		outDir     = flag.String("out", "public", "output directory")
		assetsDir  = flag.String("assets", "assets", "directory of static assets to copy")
		refresh    = flag.Bool("refresh", false, "fetch the live feeds, write the snapshots, and exit")
		check      = flag.Bool("check", false, "probe every curated entry endpoint, write the health report, and exit")
		review     = flag.Bool("review", false, "list curated entries overdue for human review, then exit")
		commitPlan = flag.Bool("commit-plan", false, "print commit or skip for the checked report against the committed one, then exit")
		healthPath = flag.String("health-report", "content/health.json", "committed endpoint health report")
		previous   = flag.String("previous-health-report", "", "the report currently in git, for -commit-plan; empty means there is none")
		attempts   = flag.Int("attempts", health.DefaultAttempts, "probe attempts per endpoint before judging it")
		timeout    = flag.Duration("timeout", live.DefaultTimeout, "per-request timeout for live fetches")
		nowFlag    = flag.String("now", "", "override the generation time (RFC3339); for reproducible builds")
	)
	flag.Parse()

	now := time.Now().UTC()
	if *nowFlag != "" {
		parsed, err := time.Parse(time.RFC3339, *nowFlag)
		if err != nil {
			return fmt.Errorf("-now %q: %w", *nowFlag, err)
		}
		now = parsed.UTC()
	}

	cfg := config{
		baseURL:    *baseURL,
		domain:     *domain,
		contentDir: *contentDir,
		cacheDir:   *cacheDir,
		outDir:     *outDir,
		assetsDir:  *assetsDir,
		refresh:    *refresh,
		check:      *check,
		review:     *review,
		commitPlan: *commitPlan,
		healthPath: *healthPath,
		previous:   *previous,
		attempts:   *attempts,
		timeout:    *timeout,
		now:        now,
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.timeout)
	defer cancel()
	return cfg.run(ctx, &http.Client{Timeout: cfg.timeout})
}

func (c config) run(ctx context.Context, client *http.Client) error {
	entries, err := content.Load(c.contentDir)
	if err != nil {
		return err
	}
	log.Printf("loaded %d curated entries from %s", len(entries), c.contentDir)

	agentsCache := filepath.Join(c.cacheDir, "live-vtessera.json")
	metricsCache := filepath.Join(c.cacheDir, "live-metrics.json")

	if c.refresh {
		return c.refreshSnapshots(ctx, client, agentsCache, metricsCache)
	}

	if c.check {
		return c.checkEndpoints(ctx, entries, client)
	}

	if c.review {
		return c.reviewDue(entries)
	}

	if c.commitPlan {
		return c.planCommit()
	}

	agents := live.Load(ctx, c.baseURL, "/v1/agents", agentsCache, client, live.ValidateAgents)
	report(agents, "agents")

	metrics := live.Load(ctx, c.baseURL, "/v1/metrics", metricsCache, client, live.ValidateMetrics)
	report(metrics, "metrics")

	// A missing or stale report demotes nothing, so the site publishes every
	// entry it has until a check says otherwise. Withholding endpoints on the
	// strength of an old report would be worse than publishing a URL that
	// happens to be up again.
	report := health.Read(c.healthPath)
	unreachable := report.Unreachable(c.now)

	site := render.Site{
		Domain:           c.domain,
		Entries:          entries,
		MetricsFromCache: metrics.FromCache,
		MetricsAge:       metrics.Age(c.now),
		MetricsErr:       metrics.FetchErr,
		GeneratedAt:      c.now,
		AssetsDir:        c.assetsDir,
		Unreachable:      unreachable,
		EndpointDetails:  report.Details(),
	}
	if !report.CheckedAt.IsZero() {
		checked := report.CheckedAt
		site.HealthCheckedAt = &checked
	}
	if len(unreachable) > 0 {
		log.Printf("health: WARNING withholding the endpoint for %d entry(s) that did not answer the last check: %v",
			len(unreachable), keys(unreachable))
	} else if report.CheckedAt.IsZero() {
		log.Printf("health: no committed report at %s; every endpoint is published unchecked", c.healthPath)
	}

	if agents.Available() {
		var response live.AgentsResponse
		if err := json.Unmarshal(agents.Response, &response); err != nil {
			return fmt.Errorf("decoding the agent feed: %w", err)
		}
		merged := response.Normalized()
		site.LiveAgents = &merged
		site.LiveAgentsFetchedAt = agents.FetchedAt
	}
	if metrics.Available() {
		var response live.MetricsResponse
		if err := json.Unmarshal(metrics.Response, &response); err != nil {
			return fmt.Errorf("decoding the metrics feed: %w", err)
		}
		merged := response.Normalized()
		site.Metrics = &merged
	}

	if err := site.Render(c.outDir); err != nil {
		return err
	}
	log.Printf("wrote %s", c.outDir)
	return nil
}

// checkEndpoints probes every advertised endpoint and commits the verdict.
//
// It exits zero even when endpoints are down. A dead third-party endpoint is a
// fact to publish, not a reason to refuse to publish, and a check that failed
// the build on every upstream hiccup would be a check nobody runs.
func (c config) checkEndpoints(ctx context.Context, entries []content.Entry, client *http.Client) error {
	report := health.Check(ctx, entries, client, health.Options{
		Attempts: c.attempts,
		Interval: health.DefaultInterval,
		Timeout:  c.timeout,
	}, c.now)
	down := 0
	for _, endpoint := range report.Endpoints {
		if !endpoint.Alive {
			down++
			log.Printf("health: %s is unreachable: %s", endpoint.URL, endpoint.Detail)
		}
	}
	log.Printf("health: %d endpoint(s) probed, %d unreachable", len(report.Endpoints), down)
	if err := report.Write(c.healthPath); err != nil {
		return fmt.Errorf("writing %s: %w", c.healthPath, err)
	}
	log.Printf("wrote %s", c.healthPath)
	return nil
}

// reviewDue lists curated entries that have gone longer than
// content.ReviewWindow without a human confirming them.
//
// It exits zero whether or not anything is due. A stale entry is a reminder,
// and a scheduled job that goes red every time an entry ages is a job that gets
// muted; the output is the signal, not the exit status. The lines are
// tab-separated so the caller can parse them without guessing.
func (c config) reviewDue(entries []content.Entry) error {
	var due []content.Entry
	for _, e := range entries {
		if e.ReviewDue(c.now) {
			due = append(due, e)
		}
	}
	if len(due) == 0 {
		log.Printf("review: all %d curated entries are confirmed within the review window", len(entries))
		return nil
	}
	for _, e := range due {
		fmt.Printf("%s\t%s\n", e.Slug, e.LastVerified.UTC().Format("2006-01-02"))
	}
	log.Printf("review: %d of %d curated entries are overdue; each needs its summary, endpoint and terms confirmed, then last_verified bumped",
		len(due), len(entries))
	return nil
}

// planCommit prints whether the report that -check just wrote is worth
// committing over the one already in the repository.
//
// The scheduled workflow reads this single word and does the git work, so the
// decision about what counts as a change lives here with the rest of the
// health rules and is covered by tests. It used to be decided by shell
// arithmetic on a JSON file, which is the kind of logic that looks like it
// works and then fails every scheduled run without anyone reading the log.
//
// -check overwrites the report in place, so the committed copy has to come from
// somewhere else: previous names the file the caller restored from git.
func (c config) planCommit() error {
	previous := health.Report{}
	if c.previous != "" {
		previous = health.Read(c.previous)
	}
	decision := health.Decide(previous, health.Read(c.healthPath), c.now)
	log.Printf("commit-plan: %s", health.Explain(previous, decision, c.now))
	fmt.Println(decision)
	return nil
}

func (c config) refreshSnapshots(ctx context.Context, client *http.Client, agentsCache, metricsCache string) error {
	if c.baseURL == "" {
		return fmt.Errorf("-refresh needs -base-url or VTESSERA_BASE_URL; refusing to write empty snapshots")
	}
	if err := live.Refresh(ctx, c.baseURL, "/v1/agents", agentsCache, client, live.ValidateAgents); err != nil {
		return fmt.Errorf("refreshing %s: %w", agentsCache, err)
	}
	log.Printf("wrote %s", agentsCache)
	if err := live.Refresh(ctx, c.baseURL, "/v1/metrics", metricsCache, client, live.ValidateMetrics); err != nil {
		return fmt.Errorf("refreshing %s: %w", metricsCache, err)
	}
	log.Printf("wrote %s", metricsCache)
	return nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// report makes the degradation path loud in the build log, so a silent
// fallback is never mistaken for live data.
func report(result live.Result, label string) {
	switch {
	case result.FetchErr == nil && !result.FromCache:
		log.Printf("%s: live from the network", label)
	case result.FetchErr == nil:
		log.Printf("%s: no live fetch attempted, using the committed snapshot", label)
	case result.Available():
		log.Printf("%s: WARNING live fetch failed (%v); using the cached snapshot", label, result.FetchErr)
	default:
		log.Printf("%s: WARNING live fetch failed (%v) and no snapshot exists; omitting the section", label, result.FetchErr)
	}
}

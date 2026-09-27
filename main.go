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
	"time"

	"github.com/douglasdemaio/agent-ai-tool/internal/content"
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

	agents := live.Load(ctx, c.baseURL, "/v1/agents", agentsCache, client, live.ValidateAgents)
	report(agents, "agents")

	metrics := live.Load(ctx, c.baseURL, "/v1/metrics", metricsCache, client, live.ValidateMetrics)
	report(metrics, "metrics")

	site := render.Site{
		Domain:           c.domain,
		Entries:          entries,
		MetricsFromCache: metrics.FromCache,
		MetricsAge:       metrics.Age(c.now),
		MetricsErr:       metrics.FetchErr,
		GeneratedAt:      c.now,
		AssetsDir:        c.assetsDir,
	}

	if agents.Available() {
		var response live.AgentsResponse
		if err := json.Unmarshal(agents.Response, &response); err != nil {
			return fmt.Errorf("decoding the agent feed: %w", err)
		}
		merged := response.Normalized()
		site.LiveAgents = &merged
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

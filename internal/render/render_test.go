package render

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemaio/agent-ai-tool/internal/content"
	"github.com/douglasdemaio/agent-ai-tool/internal/live"
)

func site(t *testing.T, entries ...content.Entry) Site {
	t.Helper()
	return Site{
		Domain:      "agent-ai-tool.com",
		Entries:     entries,
		GeneratedAt: time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC),
		AssetsDir:   "",
	}
}

func entry(slug, summary string) content.Entry {
	return content.Entry{
		Slug:         slug,
		Name:         slug,
		Summary:      summary,
		URL:          "https://example.com/" + slug,
		Source:       content.SourceCurated,
		LastVerified: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC),
	}
}

func renderTo(t *testing.T, s Site) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "public")
	if err := s.Render(out); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return out
}

func read(t *testing.T, out, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(out, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(body)
}

func TestRenderProducesTheDiscoverySurface(t *testing.T) {
	out := renderTo(t, site(t, entry("vtessera", "A marketplace.")))

	for _, rel := range []string{
		"index.html", "404.html", "robots.txt", "sitemap.xml", "llms.txt",
		"agents.json", "CNAME", ".well-known/agent-card.json", "vtessera/index.html",
	} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Errorf("missing %s: %v", rel, err)
		}
	}
}

func TestCnameCarriesTheApexDomain(t *testing.T) {
	out := renderTo(t, site(t, entry("vtessera", "A marketplace.")))
	if got := strings.TrimSpace(read(t, out, "CNAME")); got != "agent-ai-tool.com" {
		t.Errorf("CNAME = %q, want agent-ai-tool.com", got)
	}
}

func TestSummaryIsEscapedNotInjected(t *testing.T) {
	hostile := `<script>alert("xss")</script>`
	out := renderTo(t, site(t, entry("evil", hostile)))
	page := read(t, out, "evil/index.html")

	if strings.Contains(page, "<script>alert") {
		t.Error("a hostile summary reached the page as executable markup")
	}
	if !strings.Contains(page, "&lt;script&gt;") {
		t.Error("the hostile summary should still be visible, escaped")
	}
}

func TestHostileNameCannotCloseTheJSONLDScriptBlock(t *testing.T) {
	// encoding/json does not escape < or >, so an unhandled "</script>" in
	// curated content would terminate the block and let the rest become markup.
	hostile := `</script><img src=x onerror=alert(1)>`
	e := entry("evil", "summary")
	e.Name = hostile
	e.Slug = "evil"

	out := renderTo(t, site(t, e))
	page := read(t, out, "evil/index.html")

	block := page[strings.Index(page, `type="application/ld+json"`):]
	end := strings.Index(block, "</script>")
	if end < 0 {
		t.Fatal("no closing script tag found")
	}
	segment := block[:end]
	if strings.Contains(segment, "<img") {
		t.Error("content escaped the JSON-LD block")
	}
	if !strings.Contains(page, `</script>`) {
		t.Error("the JSON-LD block was not closed")
	}
}

func TestJSONLDIsValidJSON(t *testing.T) {
	out := renderTo(t, site(t, entry("vtessera", "A marketplace.")))
	page := read(t, out, "vtessera/index.html")

	start := strings.Index(page, `{"@context"`)
	if start < 0 {
		t.Fatal("no JSON-LD found")
	}
	end := strings.Index(page[start:], "</script>")
	blob := page[start : start+end]

	var node map[string]any
	if err := json.Unmarshal([]byte(blob), &node); err != nil {
		t.Fatalf("JSON-LD does not parse: %v\n%s", err, blob)
	}
	if node["@type"] != "SoftwareApplication" {
		t.Errorf("@type = %v", node["@type"])
	}
}

func TestLiveAgentsOverrideTheMachineReadableFieldsOfTheCuratedStub(t *testing.T) {
	s := site(t, entry(LiveSlug, "A2A agent marketplace."))
	s.LiveAgents = &live.AgentsResponse{Agents: []live.Agent{{
		ID:        "a1",
		Card:      live.AgentCard{Name: "alpha", URL: "https://vtessera.example/alpha"},
		UpdatedAt: time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC),
	}}}

	out := renderTo(t, s)
	page := read(t, out, "vtessera/index.html")

	// The feed owns source and last_verified, and it lists the agents.
	if !strings.Contains(page, "2026-09-26") {
		t.Error("last_verified should come from the feed, not the curated file")
	}
	if strings.Contains(page, "2026-09-20") {
		t.Error("the curated file's stale date should no longer be shown")
	}
	if !strings.Contains(page, "alpha") {
		t.Error("the registered agent should be listed")
	}
	if !strings.Contains(page, "source-live") {
		t.Error("a live-merged entry should be tagged live")
	}
	// The feed carries no marketplace identity: its agents are tenants, and
	// their names must not overwrite the service's own listing.
	if !strings.Contains(page, "https://example.com/vtessera") {
		t.Error("the curated service endpoint should survive a live merge")
	}
	if strings.Contains(page, ">vtessera.example/alpha<") {
		t.Error("a tenant's URL leaked onto the service listing")
	}
	if !strings.Contains(page, "A2A agent marketplace.") {
		t.Error("curated prose should be kept when the feed supplies none")
	}
}

func TestLiveFeedWithNoAgentsFailsTheBuild(t *testing.T) {
	s := site(t, entry(LiveSlug, "summary"))
	s.LiveAgents = &live.AgentsResponse{Agents: []live.Agent{}}

	if err := s.Render(filepath.Join(t.TempDir(), "public")); err == nil {
		t.Fatal("an empty live feed should fail rather than render an empty listing")
	}
}

func TestBadgeIsOmittedBelowTheFloor(t *testing.T) {
	for _, tc := range []struct {
		delivered int
		wantBadge bool
	}{{2, false}, {3, true}, {40, true}} {
		s := site(t, entry("a1", "s"))
		s.Metrics = &live.MetricsResponse{Agents: []live.AgentUsage{
			{AgentID: "a1", Delivered: tc.delivered},
		}}
		page := read(t, renderTo(t, s), "index.html")
		got := strings.Contains(page, "badge")
		if got != tc.wantBadge {
			t.Errorf("delivered=%d badge=%v, want %v", tc.delivered, got, tc.wantBadge)
		}
	}
}

func TestBannerIsHiddenUntilMetricsExist(t *testing.T) {
	page := read(t, renderTo(t, site(t, entry("a1", "s"))), "index.html")
	if strings.Contains(page, `class="metrics`) {
		t.Error("the usage banner must stay hidden until a metrics snapshot exists")
	}
}

func TestBannerShowsZeroTotalsOnceASnapshotExists(t *testing.T) {
	s := site(t, entry("a1", "s"))
	s.Metrics = &live.MetricsResponse{Totals: live.Totals{}}

	page := read(t, renderTo(t, s), "index.html")
	if !strings.Contains(page, `class="metrics`) {
		t.Fatal("a zeroed snapshot should still render the banner")
	}
	if !strings.Contains(page, "Live from vtessera.") {
		t.Error("a fresh snapshot should be labelled live")
	}
}

func TestBannerGoesDegradedWhenTheSnapshotIsOld(t *testing.T) {
	s := site(t, entry("a1", "s"))
	s.Metrics = &live.MetricsResponse{Totals: live.Totals{Delivered: 5}}
	s.MetricsFromCache = true
	s.MetricsAge = 20 * 24 * time.Hour
	s.MetricsErr = errors.New("connection refused")

	page := read(t, renderTo(t, s), "index.html")
	if !strings.Contains(page, "metrics degraded") {
		t.Error("a snapshot past the staleness window should render degraded")
	}
	if !strings.Contains(page, "Stale:") {
		t.Error("a stale banner should say so plainly")
	}
	if !strings.Contains(page, "20 days ago") {
		t.Errorf("the banner should report the snapshot age, got: %s", page)
	}
}

func TestCacheAgeIsReportedButNotMarkedStaleWhenFresh(t *testing.T) {
	s := site(t, entry("a1", "s"))
	s.Metrics = &live.MetricsResponse{Totals: live.Totals{Delivered: 2}}
	s.MetricsFromCache = true
	s.MetricsAge = 2 * time.Hour
	s.MetricsErr = errors.New("boom")

	page := read(t, renderTo(t, s), "index.html")
	if !strings.Contains(page, "From cache, fetched 2 hours ago.") {
		t.Error("a cached banner should report the cache age")
	}
	if strings.Contains(page, "metrics degraded") {
		t.Error("a two-hour-old cache is not degraded")
	}
}

func TestSitemapIsWellFormedXML(t *testing.T) {
	out := renderTo(t, site(t, entry("vtessera", "s")))
	body := read(t, out, "sitemap.xml")

	if !strings.HasPrefix(body, `<?xml version="1.0" encoding="UTF-8"?>`) {
		t.Error("sitemap is missing the XML declaration")
	}
	if !strings.Contains(body, `<loc>https://agent-ai-tool.com/vtessera</loc>`) {
		t.Error("sitemap is missing the entry URL")
	}
}

func TestRobotsSitemapIsAbsolute(t *testing.T) {
	body := read(t, renderTo(t, site(t, entry("vtessera", "s"))), "robots.txt")
	if !strings.Contains(body, "Sitemap: https://agent-ai-tool.com/sitemap.xml") {
		t.Error("robots.txt should point at the absolute sitemap URL")
	}
	if !strings.Contains(body, "User-agent: ClaudeBot\nDisallow: /") {
		t.Error("robots.txt should block the ClaudeBot training crawler")
	}
	if !strings.Contains(body, "User-agent: Claude-SearchBot\nAllow: /") {
		t.Error("robots.txt should allow Claude-SearchBot")
	}
}

func TestAgentsJSONCarriesTheEndpointPerEntry(t *testing.T) {
	out := renderTo(t, site(t, entry("vtessera", "A marketplace.")))

	var payload struct {
		Agents []struct {
			Slug       string  `json:"slug"`
			URL        string  `json:"url"`
			AgentCard  *string `json:"agent_card_url"`
			LastVerify string  `json:"last_verified"`
			Source     string  `json:"source"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(read(t, out, "agents.json")), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Agents) != 1 {
		t.Fatalf("got %d entries", len(payload.Agents))
	}
	got := payload.Agents[0]
	if got.URL != "https://example.com/vtessera" {
		t.Errorf("url = %q", got.URL)
	}
	if got.AgentCard != nil {
		t.Error("an omitted agent_card_url should serialise as null")
	}
	if got.LastVerify == "" {
		t.Error("last_verified should be serialised")
	}
}

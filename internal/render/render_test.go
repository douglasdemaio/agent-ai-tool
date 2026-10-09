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

// A live marketplace with no registered agents is a real state, not a fault.
// Failing the build would couple site availability to the service, which the
// design rules out. But rendering nothing would let an empty marketplace look
// identical to a working one, so the page must say so in words.
func TestEmptyLiveFeedPublishesAndSaysSo(t *testing.T) {
	s := site(t, entry(LiveSlug, "summary"))
	s.LiveAgents = &live.AgentsResponse{Agents: []live.Agent{}}

	dir := t.TempDir()
	if err := s.Render(filepath.Join(dir, "public")); err != nil {
		t.Fatalf("an empty live feed must not fail the build: %v", err)
	}
	page := read(t, filepath.Join(dir, "public"), LiveSlug+"/index.html")
	if !strings.Contains(page, "no agents are registered yet") {
		t.Error("an empty marketplace should be stated, not silently omitted")
	}
	if strings.Contains(page, "<h2>Registered agents</h2>") &&
		!strings.Contains(page, "no agents are registered yet") {
		t.Error("the heading should not imply a populated list")
	}
}

// The curated service listing keeps its identity even when the feed is empty, so
// the directory still tells an agent what vtessera is.
func TestEmptyLiveFeedKeepsTheCuratedListing(t *testing.T) {
	s := site(t, entry(LiveSlug, "summary"))
	s.LiveAgents = &live.AgentsResponse{Agents: []live.Agent{}}

	dir := t.TempDir()
	if err := s.Render(filepath.Join(dir, "public")); err != nil {
		t.Fatalf("render: %v", err)
	}
	page := read(t, filepath.Join(dir, "public"), LiveSlug+"/index.html")
	if !strings.Contains(page, "summary") {
		t.Error("the curated summary should survive an empty feed")
	}
}

// With no curated listing to fall back on, an empty feed has no name, URL, or
// summary to publish, so the entry is simply absent.
func TestEmptyLiveFeedWithoutACuratedEntryPublishesNoListing(t *testing.T) {
	s := site(t, entry("other", "summary"))
	s.LiveAgents = &live.AgentsResponse{Agents: []live.Agent{}}

	dir := t.TempDir()
	if err := s.Render(filepath.Join(dir, "public")); err != nil {
		t.Fatalf("render: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "public", LiveSlug, "index.html")); !os.IsNotExist(err) {
		t.Error("an empty feed with no curated entry should publish no vtessera page")
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
	if !strings.Contains(body, `<loc>https://agent-ai-tool.com/vtessera/</loc>`) {
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

func TestEntryCanonicalNamesTheServingURL(t *testing.T) {
	out := renderTo(t, site(t, entry("vtessera", "A marketplace.")))
	page := read(t, out, "vtessera/index.html")
	if !strings.Contains(page, `<link rel="canonical" href="https://agent-ai-tool.com/vtessera/">`) {
		t.Error("entry canonical should carry the trailing slash the host actually serves")
	}
}

func TestAgentsJSONNamesTheServingURL(t *testing.T) {
	body := read(t, renderTo(t, site(t, entry("vtessera", "A marketplace."))), "agents.json")
	if !strings.Contains(body, `"page": "https://agent-ai-tool.com/vtessera/"`) {
		t.Error("page should name the trailing-slash URL that serves 200")
	}
	if strings.Contains(body, "https://agent-ai-tool.com//") {
		t.Error("canonical change produced a double slash")
	}
}

func TestFourOhFourNamesNoCanonical(t *testing.T) {
	page := read(t, renderTo(t, site(t, entry("vtessera", "A marketplace."))), "404.html")
	if strings.Contains(page, `rel="canonical"`) {
		t.Error("a 404 should not point a canonical at a page that does not exist")
	}
}

func TestSitemapIndexLastModTracksContentNotBuildTime(t *testing.T) {
	s := site(t, entry("vtessera", "A marketplace."))
	first := read(t, renderTo(t, s), "sitemap.xml")
	// A rebuild triggered by a live snapshot rather than by an edit must not
	// make the directory look changed.
	s.GeneratedAt = s.GeneratedAt.Add(72 * time.Hour)
	second := read(t, renderTo(t, s), "sitemap.xml")
	if first != second {
		t.Error("sitemap lastmod changed on a rebuild that did not change the entries")
	}
}

func TestSitemapIndexLastModFollowsNewestEntry(t *testing.T) {
	e := entry("vtessera", "A marketplace.")
	e.LastVerified = time.Date(2026, 9, 25, 0, 0, 0, 0, time.UTC)
	body := read(t, renderTo(t, site(t, e)), "sitemap.xml")
	if !strings.Contains(body, "<loc>https://agent-ai-tool.com/</loc>") ||
		!strings.Contains(body, "2026-09-25T00:00:00Z") {
		t.Error("index lastmod should be the newest entry last_verified")
	}
}

func TestDirectoryCardSkillsCarryDescriptionsAndEndpoints(t *testing.T) {
	e := entry("vtessera", "Settles work with signed receipts.")
	mcp := "https://vtessera.example.com/mcp"
	e.MCPEndpointURL = &mcp
	var payload struct {
		Skills []struct {
			ID          string `json:"id"`
			Description string `json:"description"`
			Endpoint    string `json:"endpoint"`
		} `json:"skills"`
	}
	body := read(t, renderTo(t, site(t, e)), ".well-known/agent-card.json")
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Skills) != 1 {
		t.Fatalf("got %d skills", len(payload.Skills))
	}
	if payload.Skills[0].Description != "Settles work with signed receipts." {
		t.Errorf("skill description = %q, want the curated summary", payload.Skills[0].Description)
	}
	if payload.Skills[0].Endpoint != mcp {
		t.Errorf("skill endpoint = %q, want the MCP endpoint when the entry has one", payload.Skills[0].Endpoint)
	}
}

func TestGuidanceDoesNotRankOnAnAbsentDeliveryCount(t *testing.T) {
	out := renderTo(t, site(t, entry("vtessera", "A marketplace.")))
	llms := read(t, out, "llms.txt")
	if strings.Contains(llms, "3+ recorded deliveries") || strings.Contains(llms, "badge") {
		t.Error("llms.txt tells agents to prefer a delivered count that no entry carries")
	}
	if !strings.Contains(llms, "Treat the field as") {
		t.Error("llms.txt should say what to do when the count is absent")
	}
	if page := read(t, out, "index.html"); !strings.Contains(page, "No entry currently carries a recorded usage count") {
		t.Error("the index should say the same thing in prose")
	}
}

func TestGuidanceRecommendsBadgedEntriesWhenAnyExist(t *testing.T) {
	s := site(t, entry("vtessera", "A marketplace."))
	s.Metrics = &live.MetricsResponse{Agents: []live.AgentUsage{{AgentID: "vtessera", Delivered: 9}}}
	out := renderTo(t, s)
	if !strings.Contains(read(t, out, "llms.txt"), "3 or more earns a badge") {
		t.Error("llms.txt should recommend badged entries once one exists")
	}
	if !strings.Contains(read(t, out, "index.html"), "recorded completed trades") {
		t.Error("the index should explain the badge once one exists")
	}
}

func TestLLMsTellsAgentsToRevalidateWithTheETag(t *testing.T) {
	llms := read(t, renderTo(t, site(t, entry("vtessera", "A marketplace."))), "llms.txt")
	if !strings.Contains(llms, "If-None-Match") || !strings.Contains(llms, "304 Not Modified") {
		t.Error("llms.txt should tell agents to send If-None-Match and honour 304")
	}
}

func deadEndpointSite(t *testing.T) Site {
	t.Helper()
	e := entry("vtessera", "A marketplace.")
	e.MCPEndpointURL = strptr("https://vtessera.example.com/mcp")
	s := site(t, e)
	s.Unreachable = map[string]bool{"vtessera": true}
	s.EndpointDetails = map[string]string{"vtessera": "GET https://vtessera.example.com/mcp = 404"}
	checked := time.Date(2026, 9, 29, 6, 0, 0, 0, time.UTC)
	s.HealthCheckedAt = &checked
	return s
}

func strptr(s string) *string { return &s }

func intp(v int) *int { return &v }

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func sameInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// A dead endpoint must not reach any surface an agent reads, or the site is
// still telling it to call a URL the site itself could not reach.
func TestDeadEndpointIsWithheldFromAgentsJSON(t *testing.T) {
	body := read(t, renderTo(t, deadEndpointSite(t)), "agents.json")
	var payload struct {
		Agents []struct {
			Slug           string  `json:"slug"`
			MCPEndpointURL *string `json:"mcp_endpoint_url"`
			EndpointDown   bool    `json:"endpoint_unreachable"`
			Reason         string  `json:"endpoint_unreachable_reason"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Agents[0].MCPEndpointURL != nil {
		t.Errorf("agents.json still advertises a dead endpoint: %q", *payload.Agents[0].MCPEndpointURL)
	}
	if !payload.Agents[0].EndpointDown {
		t.Error("agents.json should mark the entry so an agent can tell dead from unchecked")
	}
	if payload.Agents[0].Reason == "" {
		t.Error("agents.json should carry the failure reason")
	}
}

func TestDeadEndpointIsWithheldFromTheAgentCard(t *testing.T) {
	body := read(t, renderTo(t, deadEndpointSite(t)), ".well-known/agent-card.json")
	var payload struct {
		Skills []struct {
			ID          string `json:"id"`
			Endpoint    string `json:"endpoint"`
			Unavailable bool   `json:"endpointUnavailable"`
		} `json:"skills"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatal(err)
	}
	skill := payload.Skills[0]
	if skill.Endpoint == "https://vtessera.example.com/mcp" {
		t.Error("the agent card still hands an agent a dead endpoint")
	}
	if !skill.Unavailable {
		t.Error("the agent card should record the endpoint as unavailable")
	}
}

func TestDeadEndpointIsWithheldFromLLMsTxt(t *testing.T) {
	llms := read(t, renderTo(t, deadEndpointSite(t)), "llms.txt")
	if strings.Contains(llms, "MCP endpoint: https://vtessera.example.com/mcp") {
		t.Error("llms.txt still lists a dead endpoint as callable")
	}
	if !strings.Contains(llms, "withheld") {
		t.Error("llms.txt should say the endpoint was withheld rather than dropping the line")
	}
}

func TestDeadEndpointIsExplainedOnItsPage(t *testing.T) {
	page := read(t, renderTo(t, deadEndpointSite(t)), "vtessera/index.html")
	// The URL may still appear inside the quoted failure reason, which is the
	// point of the explanation. What must not survive is an anchor inviting an
	// agent to call it.
	if strings.Contains(page, `href="https://vtessera.example.com/mcp"`) {
		t.Error("the entry page still links a dead endpoint")
	}
	if !strings.Contains(page, "withheld") {
		t.Error("the entry page should explain the absent endpoint")
	}
}

// Withholding is reversible: the endpoint comes back the moment the report says
// it is reachable again, with no edit to the source file.
func TestAHealthyReportRepublishesTheEndpoint(t *testing.T) {
	e := entry("vtessera", "A marketplace.")
	e.MCPEndpointURL = strptr("https://vtessera.example.com/mcp")
	s := site(t, e)
	if body := read(t, renderTo(t, s), "agents.json"); !strings.Contains(body, "https://vtessera.example.com/mcp") {
		t.Error("a healthy endpoint should be published")
	}
}

// The health check proves an endpoint answers. It cannot prove the summary is
// still true, so an entry that has gone too long without a human confirming it
// has to say so on the page and in the index.
func TestAnOverdueEntryIsMarkedForReview(t *testing.T) {
	e := entry("old", "Confirmed a long time ago and never revisited.")
	e.LastVerified = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	out := renderTo(t, site(t, e))

	if !strings.Contains(read(t, out, "old/index.html"), "due for review") {
		t.Error("an entry confirmed over six months ago should be marked on its page")
	}
	if !strings.Contains(read(t, out, "index.html"), "review due") {
		t.Error("the index should mark an overdue entry")
	}
}

func TestAConfirmedEntryIsNotMarkedForReview(t *testing.T) {
	out := renderTo(t, site(t, entry("fresh", "Confirmed last week.")))

	if strings.Contains(read(t, out, "fresh/index.html"), "due for review") {
		t.Error("an entry inside the review window should not be marked")
	}
	if strings.Contains(read(t, out, "index.html"), "review due") {
		t.Error("the index should not mark an entry inside the review window")
	}
}

// The ordering trap. viewFor builds the view from the curated stub; the live
// merge rewrites that view's source and date afterwards. Judging review-due
// before the rewrite would flag the marketplace as overdue on a curated date the
// feed has already replaced, and the flag would be wrong every single time.
func TestALiveEntryIsJudgedOnItsFeedDateNotItsCuratedStub(t *testing.T) {
	stub := entry(LiveSlug, "A2A agent marketplace.")
	stub.LastVerified = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	s := site(t, stub)
	s.LiveAgents = &live.AgentsResponse{Agents: []live.Agent{{
		ID:        "a1",
		Card:      live.AgentCard{Name: "alpha", URL: "https://vtessera.example/alpha"},
		UpdatedAt: time.Date(2026, 9, 26, 0, 0, 0, 0, time.UTC),
	}}}

	out := renderTo(t, s)
	if strings.Contains(read(t, out, "vtessera/index.html"), "due for review") {
		t.Error("a live entry was judged on its curated stub date instead of the feed's")
	}
	if strings.Contains(read(t, out, "index.html"), "review due") {
		t.Error("the index judged a live entry on its curated stub date")
	}
}

func TestHumanAgePluralizes(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{-time.Second, "moments ago"},
		{0, "moments ago"},
		{30 * time.Second, "moments ago"},
		{59 * time.Second, "moments ago"},
		{time.Minute, "1 minute ago"},
		{2 * time.Minute, "2 minutes ago"},
		{59 * time.Minute, "59 minutes ago"},
		{time.Hour, "1 hour ago"},
		{5 * time.Hour, "5 hours ago"},
		{23 * time.Hour, "23 hours ago"},
		{24 * time.Hour, "1 day ago"},
		{49 * time.Hour, "2 days ago"},
	}
	for _, c := range cases {
		if got := humanAge(c.in); got != c.want {
			t.Errorf("humanAge(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

// An empty marketplace has no agent to take a date from. The listing must still
// carry a real one, because last_verified reaches agents.json, the JSON-LD
// dateModified, and the sitemap lastmod.
func TestEmptyMarketplaceStillDatesTheLiveListing(t *testing.T) {
	fetched := time.Date(2026, 9, 29, 13, 33, 17, 0, time.UTC)
	s := site(t, content.Entry{
		Slug:         "vtessera",
		Name:         "vtessera",
		Summary:      "marketplace",
		URL:          "https://vtessera.fly.dev",
		Source:       content.SourceCurated,
		LastVerified: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC),
	})
	s.LiveAgents = &live.AgentsResponse{}
	s.LiveAgentsFetchedAt = fetched
	s.GeneratedAt = fetched.Add(2 * time.Hour)

	views, err := s.resolve()
	if err != nil {
		t.Fatal(err)
	}
	v := views[0]
	if got := v.Entry.LastVerified; !got.Equal(fetched) {
		t.Errorf("LastVerified = %s, want the feed's fetch time %s", got, fetched)
	}
	if v.Entry.LastVerified.IsZero() {
		t.Error("live listing dated year 1: agents.json would publish 0001-01-01")
	}
	if !v.LiveEmpty {
		t.Error("expected the empty-marketplace state to be preserved")
	}
}

// When agents do exist, the newest agent's updatedAt is the better date, and the
// fetch time must not override it.
func TestLiveListingPrefersAgentTimestampOverFetch(t *testing.T) {
	fetched := time.Date(2026, 9, 29, 13, 33, 17, 0, time.UTC)
	agentTime := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	s := site(t, content.Entry{
		Slug: "vtessera", Name: "vtessera", Summary: "marketplace",
		URL: "https://vtessera.fly.dev", Source: content.SourceCurated,
	})
	s.LiveAgents = &live.AgentsResponse{Agents: []live.Agent{{
		ID: "a1", Card: live.AgentCard{Name: "a1", PublicKey: "k"}, UpdatedAt: agentTime,
	}}}
	s.LiveAgentsFetchedAt = fetched
	s.GeneratedAt = fetched

	views, err := s.resolve()
	if err != nil {
		t.Fatal(err)
	}
	if got := views[0].Entry.LastVerified; !got.Equal(agentTime) {
		t.Errorf("LastVerified = %s, want the agent's updatedAt %s", got, agentTime)
	}
}

// The directory's job is to make a service callable, not merely locatable. A
// published call must survive into every surface an agent might read.
func TestHowToCallReachesEverySurface(t *testing.T) {
	s := site(t, content.Entry{
		Slug: "vtessera", Name: "vtessera", Summary: "marketplace",
		URL: "https://vtessera.fly.dev", Source: content.SourceCurated,
		LastVerified: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC),
		HowToCall: &content.HowToCall{
			Note: "Writes need a token first.",
			Auth: &content.AuthFlow{
				Type: "Ed25519 challenge-response", KeyEncoding: "base58",
				SignatureEncoding: "base64",
				SignedMessage:     "vtessera/auth/v1\\nchallenge:<challengeId>",
				TestVector:        "https://example.com/test-vectors/handshake.json",
				Steps: []content.AuthStep{{
					Name: "challenge", Method: "POST", Path: "/v1/auth/challenge",
					Body:    map[string]any{"agentId": "<base58 pubkey>"},
					Returns: "201 with a nonce.",
				}},
			},
			Calls: []content.Call{
				{
					Name: "route", Method: "POST", Path: "/agp/route",
					ContentType: "application/json", Auth: "none",
					Body:    map[string]any{"method": "agp/route"},
					Returns: "-32200 while empty.",
				},
				{
					Name: "open a trade", Method: "POST", Path: "/v1/trades",
					ContentType: "application/json", Auth: "bearer",
					Body:    map[string]any{"offerId": "<from the offer>"},
					Returns: "a trade awaiting acceptance.",
				},
			},
		},
	})
	s.GeneratedAt = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	dir := t.TempDir()
	if err := s.Render(dir); err != nil {
		t.Fatal(err)
	}
	page, err := os.ReadFile(filepath.Join(dir, "vtessera", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"How to call it", "Getting a token", "/v1/auth/challenge", "/agp/route", "Ed25519 challenge-response", "https://example.com/test-vectors/handshake.json"} {
		if !strings.Contains(string(page), want) {
			t.Errorf("entry page missing %q", want)
		}
	}

	llms, err := os.ReadFile(filepath.Join(dir, "llms.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/v1/auth/challenge", "/agp/route", "base58-encoded", "requires a token", "https://example.com/test-vectors/handshake.json"} {
		if !strings.Contains(string(llms), want) {
			t.Errorf("llms.txt missing %q", want)
		}
	}
	// Angle-bracketed placeholders must survive as themselves, not as escapes.
	if !strings.Contains(string(llms), "<base58 pubkey>") {
		t.Error("llms.txt HTML-escaped a placeholder, making it unreadable")
	}

	var doc struct {
		Agents []struct {
			HowToCall *content.HowToCall `json:"how_to_call"`
		} `json:"agents"`
	}
	raw, err := os.ReadFile(filepath.Join(dir, "agents.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Agents) != 1 || doc.Agents[0].HowToCall == nil {
		t.Fatal("agents.json dropped how_to_call")
	}
	if got := doc.Agents[0].HowToCall.Calls[0].Path; got != "/agp/route" {
		t.Errorf("agents.json call path = %q", got)
	}
}

// An entry with no how_to_call must omit the key entirely rather than publish
// an empty object, which reads as "documented, and there is nothing here".
func TestHowToCallIsOmittedWhenAbsent(t *testing.T) {
	s := site(t, content.Entry{
		Slug: "mcp-registry", Name: "MCP Registry", Summary: "registry",
		URL: "https://modelcontextprotocol.io/registry", Source: content.SourceCurated,
		LastVerified: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC),
	})
	s.GeneratedAt = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	if err := s.Render(dir); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "agents.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "how_to_call") {
		t.Error("agents.json published an empty how_to_call")
	}
}

// A retired agent is one the marketplace operator withdrew. vtessera drops it
// from the feed, but this site serves committed snapshots whenever the
// marketplace cannot be reached, so a snapshot taken before the withdrawal goes
// on advertising a seller somebody was removed on purpose. The status the feed
// carried is the only thing that distinguishes the two.
func TestAnAgentTheMarketplaceWithdrewIsNotAdvertisedAsASeller(t *testing.T) {
	stub := entry(LiveSlug, "A2A agent marketplace.")
	s := site(t, stub)
	updated := time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC)
	s.LiveAgents = &live.AgentsResponse{Agents: []live.Agent{
		{
			ID:        "a1",
			Status:    live.StatusActive,
			Card:      live.AgentCard{Name: "alpha", URL: "https://vtessera.example/alpha"},
			UpdatedAt: updated,
		},
		{
			ID:        "a2",
			Status:    "retired",
			Card:      live.AgentCard{Name: "beta", URL: "https://vtessera.example/beta"},
			UpdatedAt: updated,
		},
	}}

	out := renderTo(t, s)
	page := read(t, out, LiveSlug+"/index.html")

	if !strings.Contains(page, "https://vtessera.example/alpha") {
		t.Error("an active agent lost its link")
	}
	if strings.Contains(page, "https://vtessera.example/beta") {
		t.Error("a withdrawn agent is still linked as somewhere to go and buy")
	}
	if !strings.Contains(page, "beta") {
		t.Error("a withdrawn agent vanished from the page: a withdrawal is a fact worth publishing, not a deletion")
	}
	if !strings.Contains(page, "retired by the marketplace") {
		t.Error("the page does not say the agent was withdrawn, so a reader cannot tell it apart from a live one")
	}
}

// A snapshot predating status reporting carries no status at all. Treating that
// as withdrawn would hide every agent on the site the first time an old snapshot
// was rendered.
func TestASnapshotWithNoStatusIsStillListed(t *testing.T) {
	stub := entry(LiveSlug, "A2A agent marketplace.")
	s := site(t, stub)
	s.LiveAgents = &live.AgentsResponse{Agents: []live.Agent{{
		ID:        "a1",
		Card:      live.AgentCard{Name: "alpha", URL: "https://vtessera.example/alpha"},
		UpdatedAt: time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
	}}}

	page := read(t, renderTo(t, s), LiveSlug+"/index.html")
	if !strings.Contains(page, "https://vtessera.example/alpha") {
		t.Error("an agent with no reported status was hidden")
	}
	if strings.Contains(page, "by the marketplace") {
		t.Error("an agent with no reported status was marked withdrawn")
	}
}

func TestASuspendedAgentIsMarkedTheSameWayARetiredOneIs(t *testing.T) {
	row := liveRow{Agent: live.Agent{Status: "suspended"}}
	if !row.Withdrawn() {
		t.Error("a suspended agent is still advertised as a seller")
	}
	if got := row.WithdrawnReason(); got != "suspended by the marketplace" {
		t.Errorf("reason = %q", got)
	}
	if (liveRow{}).Withdrawn() {
		t.Error("an agent with no status counts as withdrawn")
	}
}

// agents.json, llms.txt and the entry page are three views of one render pass.
// An agent reading two of them must never get two different answers about the
// facts it acts on: whether it may call the endpoint, what the endpoint is,
// and when the entry was last checked.
func TestTheThreeDiscoverySurfacesCannotDisagree(t *testing.T) {
	const endpoint = "https://vtessera.example.com/mcp"
	const verified = "2026-09-20"

	reachable := entry("vtessera", "A marketplace.")
	reachable.MCPEndpointURL = strptr(endpoint)

	for _, tc := range []struct {
		name       string
		s          Site
		advertised bool
	}{
		{"a reachable endpoint is named on all three surfaces", site(t, reachable), true},
		{"a withheld endpoint is withheld on all three surfaces", deadEndpointSite(t), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := renderTo(t, tc.s)

			var payload struct {
				GeneratedAt string `json:"generatedAt"`
				Agents      []struct {
					MCPEndpointURL *string `json:"mcp_endpoint_url"`
					EndpointDown   bool    `json:"endpoint_unreachable"`
					Reason         string  `json:"endpoint_unreachable_reason"`
					LastVerified   string  `json:"last_verified"`
				} `json:"agents"`
			}
			if err := json.Unmarshal([]byte(read(t, out, "agents.json")), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.GeneratedAt == "" {
				t.Error("agents.json carries no generatedAt, so a reader cannot age it")
			}
			if len(payload.Agents) != 1 {
				t.Fatalf("got %d entries", len(payload.Agents))
			}
			got := payload.Agents[0]
			llms := read(t, out, "llms.txt")
			page := read(t, out, "vtessera/index.html")

			if !strings.HasPrefix(got.LastVerified, verified) {
				t.Errorf("agents.json last_verified = %q, want %s", got.LastVerified, verified)
			}
			if !strings.Contains(llms, "- Last verified: "+verified+"\n") {
				t.Error("llms.txt reports a different last verified date")
			}
			if !strings.Contains(page, "<dd>"+verified+"</dd>") {
				t.Error("the entry page reports a different last verified date")
			}

			if tc.advertised {
				if got.MCPEndpointURL == nil || *got.MCPEndpointURL != endpoint {
					t.Errorf("agents.json endpoint = %v, want %s", got.MCPEndpointURL, endpoint)
				}
				if got.EndpointDown {
					t.Error("agents.json marks a reachable endpoint unreachable")
				}
				if !strings.Contains(llms, "- MCP endpoint: "+endpoint+"\n") {
					t.Error("llms.txt does not name the endpoint agents.json carries")
				}
				if !strings.Contains(page, `href="`+endpoint+`"`) {
					t.Error("the entry page does not link the endpoint agents.json carries")
				}
				return
			}

			if got.MCPEndpointURL != nil {
				t.Errorf("agents.json still advertises a dead endpoint: %q", *got.MCPEndpointURL)
			}
			if !got.EndpointDown {
				t.Error("agents.json does not mark the endpoint unreachable")
			}
			if got.Reason == "" {
				t.Fatal("agents.json carries no failure reason to compare")
			}
			if !strings.Contains(llms, got.Reason) {
				t.Error("llms.txt does not carry the failure reason agents.json carries")
			}
			if !strings.Contains(page, got.Reason) {
				t.Error("the entry page does not carry the failure reason agents.json carries")
			}
			if strings.Contains(llms, "- MCP endpoint: "+endpoint) {
				t.Error("llms.txt names a dead endpoint as callable")
			}
			if strings.Contains(page, `href="`+endpoint+`"`) {
				t.Error("the entry page links a dead endpoint")
			}
			if !strings.Contains(llms, "withheld") || !strings.Contains(page, "withheld") {
				t.Error("the withheld state is not stated on every surface")
			}
		})
	}
}

// A withheld endpoint with no date reads as a service that has always been
// down. Every surface that states the verdict has to state how old the
// evidence behind it is, or a reader cannot tell an outage from a bad first
// impression.
func TestAWithheldEndpointSaysWhenItLastAnswered(t *testing.T) {
	lastOK := time.Date(2026, 10, 5, 14, 54, 0, 0, time.UTC)
	s := deadEndpointSite(t)
	s.EndpointLastAlive = map[string]time.Time{"vtessera": lastOK}
	out := renderTo(t, s)

	const stamp = "2026-10-05 14:54 UTC"
	if page := read(t, out, "vtessera/index.html"); !strings.Contains(page, "last answered "+stamp) {
		t.Error("the entry page does not say when the endpoint last answered")
	}
	if index := read(t, out, "index.html"); !strings.Contains(index, "last answered "+stamp) {
		t.Error("the directory badge does not say when the endpoint last answered")
	}
	if llms := read(t, out, "llms.txt"); !strings.Contains(llms, "(last answered "+stamp+")") {
		t.Error("llms.txt does not say when the endpoint last answered")
	}

	var payload struct {
		Agents []struct {
			Down   bool       `json:"endpoint_unreachable"`
			LastOK *time.Time `json:"endpoint_last_ok"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(read(t, out, "agents.json")), &payload); err != nil {
		t.Fatal(err)
	}
	got := payload.Agents[0]
	if !got.Down {
		t.Fatal("agents.json does not mark the endpoint unreachable")
	}
	if got.LastOK == nil || !got.LastOK.Equal(lastOK) {
		t.Errorf("endpoint_last_ok = %v, want %s", got.LastOK, lastOK)
	}
}

func TestAnEndpointThatHasNeverAnsweredSaysSoInsteadOfShowingNoDate(t *testing.T) {
	out := renderTo(t, deadEndpointSite(t))

	if page := read(t, out, "vtessera/index.html"); !strings.Contains(page, "it has not answered a check on record") {
		t.Error("the entry page shows neither a date nor an explanation")
	}
	if index := read(t, out, "index.html"); !strings.Contains(index, "no successful check on record") {
		t.Error("the directory badge shows neither a date nor an explanation")
	}
	if llms := read(t, out, "llms.txt"); !strings.Contains(llms, "(it has not answered a check on record)") {
		t.Error("llms.txt shows neither a date nor an explanation")
	}

	var payload struct {
		Agents []struct {
			LastOK *time.Time `json:"endpoint_last_ok"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(read(t, out, "agents.json")), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Agents[0].LastOK != nil {
		t.Errorf("endpoint_last_ok = %v, want the field omitted when there is no record", payload.Agents[0].LastOK)
	}
}

// Two claims sit next to each other on every surface: a human read the entry,
// and a machine watched the endpoint answer. The second has to advance on its
// own while the first stays exactly where the person left it — including when
// the first is overdue, because a stale review date is the finding and a probe
// must not quietly wash it away.
func TestTheCheckedDateAdvancesWhileTheReviewDateDoesNotMove(t *testing.T) {
	e := entry("vtessera", "A marketplace.")
	e.MCPEndpointURL = strptr("https://vtessera.example.com/mcp")
	// Six months and a bit before GeneratedAt, so the entry is already due.
	e.LastVerified = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	s := site(t, e)

	type dates struct {
		lastVerified string
		lastChecked  *string
	}
	datesFor := func(t *testing.T, s Site) dates {
		t.Helper()
		var payload struct {
			Agents []struct {
				LastVerified string  `json:"last_verified"`
				LastChecked  *string `json:"last_checked"`
			} `json:"agents"`
		}
		if err := json.Unmarshal([]byte(read(t, renderTo(t, s), "agents.json")), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Agents) != 1 {
			t.Fatalf("got %d entries", len(payload.Agents))
		}
		return dates{payload.Agents[0].LastVerified, payload.Agents[0].LastChecked}
	}
	stamp := func(d time.Time) string { return d.Format(time.RFC3339) }

	dayOne := time.Date(2026, 9, 27, 6, 0, 0, 0, time.UTC)
	dayTwo := dayOne.Add(24 * time.Hour)

	s.HealthCheckedAt = &dayOne
	first := datesFor(t, s)
	s.HealthCheckedAt = &dayTwo
	second := datesFor(t, s)

	if first.lastVerified != "2026-03-01T00:00:00Z" {
		t.Errorf("last_verified = %q, want the date a human filed", first.lastVerified)
	}
	if second.lastVerified != first.lastVerified {
		t.Errorf("last_verified moved to %q on a second check; only a human may move it", second.lastVerified)
	}
	if first.lastChecked == nil || *first.lastChecked != stamp(dayOne) {
		t.Errorf("last_checked = %v, want %s", first.lastChecked, stamp(dayOne))
	}
	if second.lastChecked == nil || *second.lastChecked != stamp(dayTwo) {
		t.Errorf("last_checked = %v after a second day's check, want %s", second.lastChecked, stamp(dayTwo))
	}

	// The overdue verdict belongs to the review date alone, and must survive
	// the probe moving underneath it.
	page := read(t, renderTo(t, s), "vtessera/index.html")
	if !strings.Contains(page, "due for review") {
		t.Error("an entry last reviewed over six months ago is no longer marked due")
	}
	if !strings.Contains(page, "<dt>Last verified</dt><dd>2026-03-01") {
		t.Error("the page dates the review from the curated date rather than the probe")
	}
	if want := "<dt>Endpoint checked</dt><dd>2026-09-28</dd>"; !strings.Contains(page, want) {
		t.Errorf("the page does not carry the second day's check as %q", want)
	}
	if index := read(t, renderTo(t, s), "index.html"); !strings.Contains(index, "verified 2026-03-01") ||
		!strings.Contains(index, "endpoint checked 2026-09-28") {
		t.Error("the directory does not show both dates side by side")
	}
}

// An entry with no trusted report behind it, or with nothing to probe at all,
// is unchecked rather than up. Dating it would let a build that never ran a
// check claim one, which is the same failure as a stale report demoting an
// endpoint: a wrong answer that looks like a fact.
func TestAnEntryNobodyHasCheckedSaysSoInsteadOfDatingItself(t *testing.T) {
	probed := entry("registry", "A registry.")
	probed.MCPEndpointURL = strptr("https://registry.example.com/mcp")
	unprobed := entry("notes", "Some notes.")

	for _, e := range []content.Entry{probed, unprobed} {
		out := renderTo(t, site(t, e))

		var payload struct {
			Agents []struct {
				LastChecked *string `json:"last_checked"`
			} `json:"agents"`
		}
		if err := json.Unmarshal([]byte(read(t, out, "agents.json")), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Agents[0].LastChecked != nil {
			t.Errorf("%s: last_checked = %s with no check ever recorded", e.Slug, *payload.Agents[0].LastChecked)
		}
		if page := read(t, out, e.Slug+"/index.html"); !strings.Contains(page, "no successful check on record") {
			t.Errorf("%s: the page neither dates the check nor explains its absence", e.Slug)
		}
		if llms := read(t, out, "llms.txt"); !strings.Contains(llms, "- Endpoint checked: no successful check on record\n") {
			t.Errorf("%s: llms.txt neither dates the check nor explains its absence", e.Slug)
		}
	}
}

// The sitemap tells a crawler when the listing changed. A health check is not
// that, and a lastmod that moved with every probe would have crawlers
// re-reading a directory whose entries nobody has touched.
func TestSitemapLastmodIgnoresTheProbe(t *testing.T) {
	e := entry("vtessera", "A marketplace.")
	e.MCPEndpointURL = strptr("https://vtessera.example.com/mcp")
	s := site(t, e)
	probed := time.Date(2026, 10, 8, 6, 0, 0, 0, time.UTC)
	s.HealthCheckedAt = &probed

	body := read(t, renderTo(t, s), "sitemap.xml")
	if strings.Contains(body, "2026-10-08") {
		t.Error("sitemap lastmod followed the probe instead of the content")
	}
	if !strings.Contains(body, "2026-09-20T00:00:00Z") {
		t.Error("sitemap lastmod no longer carries the entry's last_verified")
	}
}

// The checked date is a claim an agent may act on, so it has to be the same
// number wherever it appears — including while the endpoint is down, where the
// number is the last success rather than the check that is reporting failure.
func TestTheCheckedDateAgreesAcrossTheThreeSurfaces(t *testing.T) {
	const endpoint = "https://vtessera.example.com/mcp"
	checked := func(t *testing.T, s Site) (jsonDate, llmsLine, pageDate string) {
		t.Helper()
		out := renderTo(t, s)
		var payload struct {
			Agents []struct {
				LastChecked *string `json:"last_checked"`
			} `json:"agents"`
		}
		if err := json.Unmarshal([]byte(read(t, out, "agents.json")), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Agents[0].LastChecked != nil {
			// agents.json carries the machine form; compare the date part,
			// which is what the other two surfaces print.
			jsonDate = strings.SplitN(*payload.Agents[0].LastChecked, "T", 2)[0]
		}
		for _, line := range strings.Split(read(t, out, "llms.txt"), "\n") {
			if strings.HasPrefix(line, "- Endpoint checked: ") {
				llmsLine = strings.TrimPrefix(line, "- Endpoint checked: ")
				break
			}
		}
		page := read(t, out, "vtessera/index.html")
		const mark = "<dt>Endpoint checked</dt><dd>"
		if i := strings.Index(page, mark); i >= 0 {
			rest := page[i+len(mark):]
			pageDate = rest[:strings.Index(rest, "<")]
		}
		return jsonDate, llmsLine, pageDate
	}

	// Up: the date is the sweep that just accepted the endpoint.
	alive := entry("vtessera", "A marketplace.")
	alive.MCPEndpointURL = strptr(endpoint)
	up := site(t, alive)
	sweep := time.Date(2026, 10, 8, 6, 0, 0, 0, time.UTC)
	up.HealthCheckedAt = &sweep
	gotJSON, gotLLMS, gotPage := checked(t, up)
	if gotJSON != "2026-10-08" || gotLLMS != "2026-10-08" || gotPage != "2026-10-08" {
		t.Errorf("an up endpoint reports checked as agents.json=%q llms.txt=%q page=%q, want one date on all three",
			gotJSON, gotLLMS, gotPage)
	}

	// Down: the date is the last success before the outage, not the check
	// that keeps reporting the failure.
	down := deadEndpointSite(t)
	lastOK := time.Date(2026, 10, 5, 14, 54, 0, 0, time.UTC)
	down.EndpointLastAlive = map[string]time.Time{"vtessera": lastOK}
	gotJSON, gotLLMS, gotPage = checked(t, down)
	if gotJSON != "2026-10-05" || gotLLMS != "2026-10-05" || gotPage != "2026-10-05" {
		t.Errorf("a down endpoint reports checked as agents.json=%q llms.txt=%q page=%q, want the last success on all three",
			gotJSON, gotLLMS, gotPage)
	}
}

// The only agents registered on the marketplace are this repository's own
// probes, so every number in the banner is our test traffic. A heading that
// presents it as outside usage is worse than no heading, because the number
// looks the same either way and only the heading says what it measures.
func TestTheBannerNamesWhoseActivityItIsShowing(t *testing.T) {
	probe := live.ProbeAgents[0]
	for _, tc := range []struct {
		name     string
		agents   []live.AgentUsage
		want     string
		wantLLMS string
	}{
		{
			name:     "every contributing agent is ours",
			agents:   []live.AgentUsage{{AgentID: probe, Delivered: 2}},
			want:     "Test activity on vtessera",
			wantLLMS: "## Test activity on vtessera",
		},
		{
			// No apostrophe: html/template escapes it, and the assertion is
			// about the claim rather than about the punctuation.
			name: "ours are mixed with an outside agent",
			agents: []live.AgentUsage{
				{AgentID: probe, Delivered: 2},
				{AgentID: "outside-agent", Delivered: 4},
			},
			want:     "What agents are doing on vtessera, including this repository",
			wantLLMS: "include this repository's own probe agents",
		},
		{
			name:     "nobody's ours",
			agents:   []live.AgentUsage{{AgentID: "outside-agent", Delivered: 4}},
			want:     "What agents are actually doing on vtessera",
			wantLLMS: "## Marketplace usage",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := site(t, entry("a1", "s"))
			s.Metrics = &live.MetricsResponse{
				Totals: live.Totals{Delivered: 6, Consumers: 2},
				Agents: tc.agents,
			}
			page := read(t, renderTo(t, s), "index.html")
			if !strings.Contains(page, tc.want) {
				t.Errorf("banner headline does not state %q", tc.want)
			}
			llms := read(t, renderTo(t, s), "llms.txt")
			if !strings.Contains(llms, tc.wantLLMS) {
				t.Errorf("llms.txt does not carry %q", tc.wantLLMS)
			}
		})
	}
}

// A delivery badge next to an entry says that entry has been exercised by
// somebody. Ours exercising our own marketplace is not that, and the badge is
// the one place a probe could otherwise pass itself off as a customer.
func TestOurOwnProbesNeverEarnAnEntryABadge(t *testing.T) {
	s := site(t, entry(live.ProbeAgents[0], "A marketplace."))
	s.Metrics = &live.MetricsResponse{
		Totals: live.Totals{Delivered: 9, Disputed: 1},
		Agents: []live.AgentUsage{{AgentID: live.ProbeAgents[0], Delivered: 9}},
	}
	out := renderTo(t, s)

	index := read(t, out, "index.html")
	if strings.Contains(index, "9 delivered") {
		t.Error("a probe agent's deliveries earned an entry a badge")
	}
	if !strings.Contains(index, "No entry currently carries a recorded usage count") {
		t.Error("the index should say no entry carries a count rather than showing a probe's")
	}
	page := read(t, out, s.Entries[0].Slug+"/index.html")
	if strings.Contains(page, "Deliveries recorded") {
		t.Error("a probe agent's deliveries reached the entry page")
	}

	// An outside agent with the same figure still earns it, so the exclusion is
	// about who traded rather than about the count. The entry has to carry the
	// same identifier the join is keyed on, which is how badges work today.
	outside := site(t, entry("outside-agent", "A marketplace."))
	outside.Metrics = &live.MetricsResponse{
		Totals: live.Totals{Delivered: 9},
		Agents: []live.AgentUsage{{AgentID: "outside-agent", Delivered: 9}},
	}
	index = read(t, renderTo(t, outside), "index.html")
	if !strings.Contains(index, "9 delivered") {
		t.Error("an outside agent's deliveries should still badge the entry")
	}
}

// Both kinds of endpoint used to be published under one label, so a JSON
// document was advertised as an MCP endpoint and an agent that trusted the
// label opened a session against it and got an HTTP 405. The label has to
// follow what the endpoint speaks on every surface that prints it, or the
// surfaces disagree about what to connect to.
func TestAnEndpointIsLabelledByWhatItSpeaks(t *testing.T) {
	api := entry("models-dev", "A JSON document.")
	api.APIURL = strptr("https://models.dev/api.json")
	mcp := entry("vtessera", "A marketplace.")
	mcp.MCPEndpointURL = strptr("https://vtessera.example.com/mcp")
	out := renderTo(t, site(t, api, mcp))

	for _, tc := range []struct {
		name, path, want, dontWant string
	}{
		{
			name:     "entry page of a plain API names it as an API",
			path:     "models-dev/index.html",
			want:     "<dt>API endpoint</dt>",
			dontWant: "<dt>MCP endpoint</dt>",
		},
		{
			name:     "entry page of an MCP server names it as MCP",
			path:     "vtessera/index.html",
			want:     "<dt>MCP endpoint</dt>",
			dontWant: "<dt>API endpoint</dt>",
		},
		{
			name:     "llms.txt names a plain API as an API",
			path:     "llms.txt",
			want:     "- API endpoint: https://models.dev/api.json",
			dontWant: "- MCP endpoint: https://models.dev/api.json",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := read(t, out, tc.path)
			if !strings.Contains(body, tc.want) {
				t.Errorf("missing %q", tc.want)
			}
			if strings.Contains(body, tc.dontWant) {
				t.Errorf("carries %q, which mislabels the endpoint", tc.dontWant)
			}
		})
	}

	// llms.txt carries both entries, so the MCP bullet is asserted against this
	// file rather than the page that only holds one of them.
	llms := read(t, out, "llms.txt")
	if !strings.Contains(llms, "- MCP endpoint: https://vtessera.example.com/mcp") {
		t.Error("llms.txt does not name the MCP endpoint as MCP")
	}

	var payload struct {
		Agents []struct {
			Slug           string  `json:"slug"`
			MCPEndpointURL *string `json:"mcp_endpoint_url"`
			APIURL         *string `json:"api_url"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(read(t, out, "agents.json")), &payload); err != nil {
		t.Fatalf("agents.json: %v", err)
	}
	for _, a := range payload.Agents {
		switch a.Slug {
		case "models-dev":
			if a.APIURL == nil || *a.APIURL != "https://models.dev/api.json" {
				t.Errorf("api_url = %v, want the plain API published there", a.APIURL)
			}
			if a.MCPEndpointURL != nil {
				t.Errorf("mcp_endpoint_url = %q, want null for an entry that speaks no MCP", *a.MCPEndpointURL)
			}
		case "vtessera":
			if a.MCPEndpointURL == nil || *a.MCPEndpointURL != "https://vtessera.example.com/mcp" {
				t.Errorf("mcp_endpoint_url = %v, want the MCP endpoint published there", a.MCPEndpointURL)
			}
			if a.APIURL != nil {
				t.Errorf("api_url = %q, want absent for an entry with no plain API", *a.APIURL)
			}
		}
	}
}

// Withholding must not erase the kind. A row that says only "endpoint
// withheld" leaves the reader unable to tell whether the thing that did not
// answer was a server to open a session against or a document to fetch, and
// the withheld label is the one place the two could still be confused.
func TestAWithheldEndpointKeepsItsKind(t *testing.T) {
	e := entry("models-dev", "A JSON document.")
	e.APIURL = strptr("https://models.dev/api.json")
	s := site(t, e)
	s.Unreachable = map[string]bool{"models-dev": true}
	s.EndpointDetails = map[string]string{"models-dev": "GET https://models.dev/api.json = 500"}
	checked := time.Date(2026, 9, 29, 6, 0, 0, 0, time.UTC)
	s.HealthCheckedAt = &checked
	out := renderTo(t, s)

	page := read(t, out, "models-dev/index.html")
	if !strings.Contains(page, "<dt>API endpoint</dt>") || !strings.Contains(page, "withheld") {
		t.Errorf("the entry page should withhold an API endpoint under its own label")
	}
	if strings.Contains(page, "<dt>MCP endpoint</dt>") {
		t.Error("the entry page claims an MCP endpoint this entry never had")
	}
	if strings.Contains(page, `href="https://models.dev/api.json"`) {
		t.Error("the entry page still links a dead API")
	}

	llms := read(t, out, "llms.txt")
	if !strings.Contains(llms, "- API endpoint: withheld") {
		t.Error("llms.txt should withhold the API endpoint under its own label")
	}
	if strings.Contains(llms, "- API endpoint: https://") {
		t.Error("llms.txt still lists a dead API as callable")
	}
	if strings.Contains(llms, "- MCP endpoint: withheld") {
		t.Error("llms.txt withholds an MCP endpoint this entry never had")
	}

	var payload struct {
		Agents []struct {
			APIURL *string `json:"api_url"`
			Down   bool    `json:"endpoint_unreachable"`
			Reason string  `json:"endpoint_unreachable_reason"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(read(t, out, "agents.json")), &payload); err != nil {
		t.Fatalf("agents.json: %v", err)
	}
	if len(payload.Agents) != 1 {
		t.Fatalf("agents = %d, want one", len(payload.Agents))
	}
	if payload.Agents[0].APIURL != nil {
		t.Errorf("agents.json still hands out a dead API: %q", *payload.Agents[0].APIURL)
	}
	if !payload.Agents[0].Down || payload.Agents[0].Reason == "" {
		t.Error("the outage should still be reported as unreachable with a reason")
	}
}

// agents.json, the entry page and llms.txt used to disagree about one call:
// agents.json published auth "none" for POST /agp/route while the other two
// said it requires a token, and the live endpoint answers 200 with no
// Authorization header at all. Agents read whichever surface they land on, so
// the label has to follow the declared value on every one of them.
func TestEachCallIsLabelledWithTheAuthItDeclares(t *testing.T) {
	s := site(t, content.Entry{
		Slug: "vtessera", Name: "vtessera", Summary: "marketplace",
		URL: "https://vtessera.fly.dev", Source: content.SourceCurated,
		LastVerified: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC),
		HowToCall: &content.HowToCall{
			Calls: []content.Call{
				{Name: "route", Method: "POST", Path: "/agp/route", Auth: "none"},
				{Name: "open a trade", Method: "POST", Path: "/v1/trades", Auth: "bearer"},
			},
		},
	})
	s.GeneratedAt = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	out := renderTo(t, s)

	for _, surface := range []struct {
		name string
		body string
	}{
		{"entry page", read(t, out, "vtessera/index.html")},
		{"llms.txt", read(t, out, "llms.txt")},
	} {
		for _, tc := range []struct{ path, want string }{
			{"POST /agp/route", "no token needed"},
			{"POST /v1/trades", "requires a token"},
		} {
			line := lineContaining(surface.body, tc.path)
			if line == "" {
				t.Errorf("%s: nothing carries %q", surface.name, tc.path)
				continue
			}
			if !strings.Contains(line, tc.want) {
				t.Errorf("%s: the line for %s reads %q, want it to say %q",
					surface.name, tc.path, strings.TrimSpace(line), tc.want)
			}
		}
	}

	// agents.json publishes the values verbatim and was the surface that was
	// right all along; the fix is the other two reading it correctly.
	var doc struct {
		Agents []struct {
			HowToCall *content.HowToCall `json:"how_to_call"`
		} `json:"agents"`
	}
	body := read(t, out, "agents.json")
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("agents.json: %v", err)
	}
	if len(doc.Agents) != 1 || doc.Agents[0].HowToCall == nil || len(doc.Agents[0].HowToCall.Calls) != 2 {
		t.Fatalf("agents.json dropped how_to_call or its calls")
	}
	calls := doc.Agents[0].HowToCall.Calls
	if calls[0].Auth != "none" || calls[0].RequiresToken() {
		t.Errorf("agents.json auth = %q for the call that needs none", calls[0].Auth)
	}
	if calls[1].Auth != "bearer" || !calls[1].RequiresToken() {
		t.Errorf("agents.json auth = %q for the call that needs a token", calls[1].Auth)
	}
}

func lineContaining(body, needle string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

// The three states an agent needs to skip a dead endpoint from agents.json
// alone: one that answers, one that failed a check, and one nobody has checked.
// "unknown" is published rather than folded into either of the others, because
// not having been checked is not the same as having failed a check — and the
// timing and the date travel with the verdict, so a precise number never
// appears next to a status that disclaims the report it came from.
func TestAgentsJSONPublishesWhatItCanProveAboutEachEntry(t *testing.T) {
	healthy := entry("models-dev", "A registry.")
	healthy.APIURL = strptr("https://models.dev/api.json")
	dead := entry("vtessera", "A marketplace.")
	dead.MCPEndpointURL = strptr("https://vtessera.example.com/mcp")
	// A home page is a destination for a human, so nothing probes it and
	// nothing can prove anything about it, report or no report.
	unprobed := entry("notes", "Some notes.")

	checked := time.Date(2026, 9, 29, 6, 0, 0, 0, time.UTC)
	lastAlive := checked.Add(-24 * time.Hour)
	s := site(t, healthy, dead, unprobed)
	s.HealthCheckedAt = &checked
	s.Unreachable = map[string]bool{"vtessera": true}
	s.EndpointDetails = map[string]string{"vtessera": "GET https://vtessera.example.com/mcp = 404"}
	s.EndpointLastAlive = map[string]time.Time{"vtessera": lastAlive}
	s.EndpointResponseMS = map[string]int{"models-dev": 12}

	out := renderTo(t, s)
	var payload struct {
		SchemaVersion int `json:"schemaVersion"`
		Agents        []struct {
			Slug       string     `json:"slug"`
			Status     string     `json:"status"`
			LastOK     *time.Time `json:"last_ok"`
			ResponseMs *int       `json:"response_ms"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(read(t, out, "agents.json")), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SchemaVersion != SchemaVersion {
		t.Errorf("schemaVersion = %d, want %d, so a cached reader can tell a new contract from a new build", payload.SchemaVersion, SchemaVersion)
	}
	bySlug := map[string]int{}
	for i, a := range payload.Agents {
		bySlug[a.Slug] = i
	}
	for _, tc := range []struct {
		slug       string
		status     string
		lastOK     *time.Time
		responseMs *int
	}{
		{"models-dev", "up", &checked, intp(12)},
		{"vtessera", "down", &lastAlive, nil},
		{"notes", "unknown", nil, nil},
	} {
		a := payload.Agents[bySlug[tc.slug]]
		if a.Status != tc.status {
			t.Errorf("%s: status = %q, want %q", tc.slug, a.Status, tc.status)
		}
		if !sameTime(a.LastOK, tc.lastOK) {
			t.Errorf("%s: last_ok = %v, want %v", tc.slug, a.LastOK, tc.lastOK)
		}
		if !sameInt(a.ResponseMs, tc.responseMs) {
			t.Errorf("%s: response_ms = %v, want %v", tc.slug, a.ResponseMs, tc.responseMs)
		}
	}
}

// An entry nobody has checked has not answered a check and has not failed one
// either. Folding that into "up" would promise a liveness the site never
// observed; folding it into "down" would slander a service that may be fine.
// The stray timing goes with it: a number next to "unknown" would be a claim
// the status just disclaimed.
func TestAnEntryNobodyHasCheckedPublishesUnknownNotUp(t *testing.T) {
	e := entry("vtessera", "A marketplace.")
	e.APIURL = strptr("https://vtessera.example.com/api")
	s := site(t, e)
	s.EndpointResponseMS = map[string]int{"vtessera": 12}

	out := renderTo(t, s)
	var payload struct {
		Agents []struct {
			Status     string  `json:"status"`
			LastOK     *string `json:"last_ok"`
			ResponseMs *int    `json:"response_ms"`
		} `json:"agents"`
	}
	if err := json.Unmarshal([]byte(read(t, out, "agents.json")), &payload); err != nil {
		t.Fatal(err)
	}
	got := payload.Agents[0]
	if got.Status != "unknown" {
		t.Errorf("status = %q, want %q", got.Status, "unknown")
	}
	if got.LastOK != nil {
		t.Errorf("last_ok = %s, want the field absent when nothing has been checked", *got.LastOK)
	}
	if got.ResponseMs != nil {
		t.Errorf("response_ms = %d, want the timing dropped with the verdict it came from", *got.ResponseMs)
	}
}

// agents.json is the machine surface, but an agent arrives at it through this
// prose. A reader that never opens the JSON still has to know what each field
// means, that an absent one is a statement rather than an omission, and that
// schemaVersion is the promise that a change of meaning is announced rather
// than slipped in.
func TestLLMsTxtDocumentsTheAgentsJSONFields(t *testing.T) {
	out := renderTo(t, site(t, entry("vtessera", "A marketplace.")))
	llms := read(t, out, "llms.txt")
	for _, want := range []string{
		"## agents.json fields",
		"`schemaVersion`",
		"`status`",
		"`up` when a check inside the last 48 hours",
		"`unknown` is not a\n  softer `up`",
		"`last_ok`",
		"`response_ms`",
		"Absent when\n  none did, which is not the same as zero",
	} {
		if !strings.Contains(llms, want) {
			t.Errorf("llms.txt does not document %q", want)
		}
	}
}

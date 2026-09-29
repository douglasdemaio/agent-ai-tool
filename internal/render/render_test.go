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

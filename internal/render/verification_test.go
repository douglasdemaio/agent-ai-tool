package render

import (
	"encoding/json"
	"errors"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemaio/agent-ai-tool/internal/content"
	"github.com/douglasdemaio/agent-ai-tool/internal/live"
)

// fixture is a card, its two signatures, and one probe report, all produced by
// the marketplace's own code rather than written by hand here.
//
// The point of a fixture rather than a locally signed card is that these tests
// then exercise the real canonical encoder. A test that rebuilt the encoding
// alongside the code under test would agree with it even if both were wrong,
// and the whole feature is an encoding nobody in this repository owns.
type fixture struct {
	VerificationKey string               `json:"verificationKey"`
	Agent           live.Agent           `json:"agent"`
	Attestation     live.CardAttestation `json:"attestation"`
	Probe           live.ProbeReport     `json:"probe"`
}

func loadFixture(t *testing.T) fixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "verification.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.VerificationKey == "" || f.Agent.ID == "" {
		t.Fatal("the fixture is missing its keys")
	}
	return f
}

// site builds the render input for one signed agent, with mutate available so a
// test can change the card after it was signed.
func (f fixture) site(t *testing.T, at time.Time, mutate func(*live.Agent), reports *live.AgentReportsResponse) Site {
	t.Helper()
	agent := f.Agent
	if reports == nil {
		reports = &live.AgentReportsResponse{Agents: []live.AgentReport{{
			AgentID: f.Agent.ID, Attestation: f.Attestation,
		}}}
	}
	if mutate != nil {
		mutate(&agent)
	}
	agents := live.AgentsResponse{Agents: []live.Agent{agent}}
	return Site{
		Domain: "example.test",
		Entries: []content.Entry{
			{Slug: "vtessera", Name: "vtessera", Summary: "A2A agent marketplace.",
				URL: "https://vtessera.example", Source: content.SourceCurated, LastVerified: at},
			{Slug: "other", Name: "other", Summary: "Another service.",
				URL: "https://other.example", Source: content.SourceCurated, LastVerified: at},
		},
		LiveAgents:       &agents,
		Marketplace:      &live.Health{Status: "ok", VerificationKey: f.VerificationKey, Cluster: "mainnet-beta", SettlementTier: "onchain"},
		AgentReports:     reports,
		GeneratedAt:      at.Add(time.Hour),
		ReportsFetchedAt: at,
	}
}

func renderToDir(t *testing.T, s Site) string {
	t.Helper()
	out := t.TempDir()
	if err := s.Render(out); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return out
}

func TestAFixtureCardVerifiesAgainstTheMarketplaceAndTheAgent(t *testing.T) {
	// Before anything is asserted about the pages: the fixture has to pass the
	// production verifier, or the rendering tests below would be asserting that
	// broken input produces a confident page.
	f := loadFixture(t)
	at := time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)
	got := f.site(t, at, nil, nil).VerifyAgents()
	if len(got) != 1 {
		t.Fatalf("got %d agents", len(got))
	}
	v := got[0]
	if !v.Marketplace.Valid {
		t.Errorf("marketplace signature did not verify: %s", v.Marketplace.Reason)
	}
	if !v.Agent.Valid {
		t.Errorf("agent signature did not verify: %s", v.Agent.Reason)
	}
	if v.Probe.Probed {
		t.Error("a fixture with no probe reported one")
	}
}

func TestTheVerificationBlockLivesOnOnePageAndEntriesLinkToIt(t *testing.T) {
	// The block is about the marketplace and the cards registered with it, not
	// about any one listing. Rendering it on every entry page told a reader that
	// each listing had been checked on its own, which was never true, so it is
	// rendered once at /verify/ and every entry links to it.
	f := loadFixture(t)
	at := time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)
	out := renderToDir(t, f.site(t, at, nil, nil))

	verify, err := os.ReadFile(filepath.Join(out, "verify", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		f.VerificationKey,
		f.Agent.ID,
		"state-verified",
		"never probed",
		"mainnet-beta",
	} {
		if !strings.Contains(string(verify), want) {
			t.Errorf("the verification page does not contain %q", want)
		}
	}

	for _, slug := range []string{"vtessera", "other"} {
		body, err := os.ReadFile(filepath.Join(out, slug, "index.html"))
		if err != nil {
			t.Fatalf("%s: %v", slug, err)
		}
		page := string(body)
		if !strings.Contains(page, `href="/verify/"`) {
			t.Errorf("%s page does not link to the verification page", slug)
		}
		if strings.Contains(page, `id="verification"`) || strings.Contains(page, "state-verified") {
			t.Errorf("%s page still embeds the verification block", slug)
		}
	}
}

func TestAPageSaysInvalidWhenTheCardNoLongerMatchesItsSignature(t *testing.T) {
	f := loadFixture(t)
	at := time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)
	out := renderToDir(t, f.site(t, at, func(a *live.Agent) {
		a.Card.Capabilities = append(a.Card.Capabilities, "admin")
	}, nil))
	body, err := os.ReadFile(filepath.Join(out, "verify", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	if !strings.Contains(page, "state-invalid") {
		t.Error("an edited card rendered as anything but invalid")
	}
	if strings.Contains(page, "state-verified") {
		t.Error("an edited card still rendered a verified signature")
	}
}

func TestNoPublishedKeyMeansNothingIsCalledVerified(t *testing.T) {
	// The failure this guards: a build that cannot reach /healthz quietly rendering
	// every signature as fine. Falling back to the key a signature names would
	// make that possible, so the empty key has to check nothing.
	f := loadFixture(t)
	at := time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)
	s := f.site(t, at, nil, nil)
	s.Marketplace = nil
	out := renderToDir(t, s)

	body, err := os.ReadFile(filepath.Join(out, "verify", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	if strings.Contains(page, "state-verified") {
		t.Error("a card verified with no published key to check it against")
	}
	if !strings.Contains(page, "No marketplace answered") {
		t.Error("the page did not say why nothing could be checked")
	}
}

func TestAPassedProbeIsOnlyPublishedAsVerifiedOnceItsSignatureChecksOut(t *testing.T) {
	f := loadFixture(t)
	at := time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)

	signed := &live.AgentReportsResponse{Agents: []live.AgentReport{{
		AgentID: f.Agent.ID, Attestation: f.Attestation, Probe: &f.Probe,
	}}}
	out := renderToDir(t, f.site(t, at, nil, signed))
	body, err := os.ReadFile(filepath.Join(out, "verify", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "checked 2026-10-05") {
		t.Error("a verified probe report was not dated on the page")
	}

	// The same report with one result edited after signing. The marketplace said
	// it passed; the directory must not repeat that.
	edited := f.Probe
	edited.Results = []live.ProbeOutcome{{Capability: "text", Status: "fail", Detail: "answered in 240ms"}}
	tampered := &live.AgentReportsResponse{Agents: []live.AgentReport{{
		AgentID: f.Agent.ID, Attestation: f.Attestation, Probe: &edited,
	}}}
	out = renderToDir(t, f.site(t, at, nil, tampered))
	body, err = os.ReadFile(filepath.Join(out, "verify", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	if page := string(body); !strings.Contains(page, "state-unverified") {
		t.Error("an edited probe report was published as checked")
	}
}

func TestAgentsJSONCarriesTheVerdictsAndNotTheMarketplacesOwnFlags(t *testing.T) {
	f := loadFixture(t)
	at := time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)
	// The marketplace's own claim that its signature is valid, plus a probe it
	// says passed and says is valid. None of it may reach the output: a directory
	// republishing those would be reporting the marketplace's assessment of itself
	// under its own name.
	// The probe here arrives unsigned, which is the shape that must not be
	// published as checked however confidently the marketplace flagged it.
	unsigned := f.Probe
	unsigned.Signature = nil
	unsigned.AttestedBy = ""
	reports := &live.AgentReportsResponse{Agents: []live.AgentReport{{
		AgentID: f.Agent.ID, Attestation: f.Attestation, Probe: &unsigned,
	}}}
	out := renderToDir(t, f.site(t, at, nil, reports))

	raw, err := os.ReadFile(filepath.Join(out, "agents.json"))
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Verification struct {
			MarketplaceKey string `json:"marketplace_key"`
			CanonicalForm  string `json:"canonical_form"`
			Agents         []struct {
				AgentID              string `json:"agent_id"`
				MarketplaceSignature string `json:"marketplace_signature"`
				AgentSignature       string `json:"agent_signature"`
				CapabilityList       string `json:"capability_list"`
			} `json:"agents"`
		} `json:"verification"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatal(err)
	}
	v := parsed.Verification
	if v.MarketplaceKey != f.VerificationKey || v.CanonicalForm != live.CanonicalForm {
		t.Fatalf("the verification block was %+v", v)
	}
	if len(v.Agents) != 1 {
		t.Fatalf("got %d verified agents, want 1", len(v.Agents))
	}
	if v.Agents[0].MarketplaceSignature != "verified" {
		t.Errorf("the marketplace signature read as %q", v.Agents[0].MarketplaceSignature)
	}
	// The report carried no signature, so the directory could not check it. The
	// honest answer is that the capability list is unverified, whatever the
	// marketplace said about it.
	if v.Agents[0].CapabilityList == "verified" {
		t.Error("an unsigned probe report was published as verified")
	}
}

func TestNoVerificationBlockIsPublishedWhenNoMarketplaceAnswered(t *testing.T) {
	f := loadFixture(t)
	at := time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)
	s := f.site(t, at, nil, nil)
	s.Marketplace = nil
	out := renderToDir(t, s)
	raw, err := os.ReadFile(filepath.Join(out, "agents.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"verification"`) {
		t.Error("agents.json published a verification block with no key behind it")
	}
}

func TestAMarketplaceThatCannotBeReachedIsReportedRatherThanHidden(t *testing.T) {
	// An outage is a fact about the directory's own build, so it is printed in the
	// same place as the other degradations rather than silently rendering pages
	// that look checked.
	f := loadFixture(t)
	at := time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)
	s := f.site(t, at, nil, nil)
	s.Marketplace = nil
	s.AgentReports = nil
	block := s.verificationBlock()
	if block.Known {
		t.Error("a block claimed a marketplace that never answered")
	}
	// The agent is still counted, because the feed says how many cards there are.
	// What must be zero is the number of signatures anyone vouched for.
	if block.MarketVerified != 0 || block.AgentVerified != 0 {
		t.Errorf("a block reported %d and %d verified signatures with no key", block.MarketVerified, block.AgentVerified)
	}
	if block.Summary() == "" {
		t.Error("an empty block gave no summary")
	}
}

func TestAReportFeedThatFailedDoesNotLeaveEveryCardReadingAsUnsigned(t *testing.T) {
	// The failure mode this guards: with no report feed at all, each agent falls
	// through to an empty report, which has no signature and no error. That reads
	// as "the marketplace has never attested this card" when the truth is "this
	// build could not ask", and only one of those is the marketplace's fault.
	f := loadFixture(t)
	at := time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)
	s := f.site(t, at, nil, nil)
	s.AgentReports = nil
	s.ReportsErr = errors.New("live fetch failed (no committed snapshot) and no snapshot exists")

	for _, v := range s.VerifyAgents() {
		if v.Marketplace.State() == "unsigned" || v.Agent.State() == "unsigned" {
			t.Errorf("%s reads as unsigned with no report feed: %q", v.AgentID, v.Marketplace.Reason)
		}
		if v.Marketplace.Reason == "" {
			t.Errorf("%s was given no reason", v.AgentID)
		}
	}
}

func TestThePageDoesNotBlameTheMarketplaceForCardsItPredates(t *testing.T) {
	// Six agents registered before the marketplace began signing cards, none of
	// them signed and none of them refused. The page said "0 verified by the
	// marketplace" for all six, which is the worst reading available and the
	// wrong one.
	f := loadFixture(t)
	at := time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)
	// The row is absent, which is what "registered before attestations existed"
	// looks like to a reader of the endpoint.
	s := f.site(t, at, nil, &live.AgentReportsResponse{Agents: []live.AgentReport{{
		AgentID:     f.Agent.ID,
		Attestation: live.CardAttestation{AgentID: f.Agent.ID, Recorded: false, CanonicalForm: live.CanonicalForm},
	}}})

	for _, v := range s.VerifyAgents() {
		if v.Marketplace.State() == "unsigned" {
			t.Errorf("a card with no attestation row reads as unsigned")
		}
		if got := v.Marketplace.State(); got != "not attested" {
			t.Errorf("state = %q, want %q", got, "not attested")
		}
	}

	out := renderToDir(t, s)
	body, err := os.ReadFile(filepath.Join(out, "verify", "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	for _, want := range []string{
		"no attestation recorded",
		"state-not-attested",
		"registered before this marketplace signed cards",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the page does not say %q", want)
		}
	}
	if strings.Contains(page, "state-unsigned") {
		t.Error("the page still renders a card with no attestation row as unsigned")
	}
	if !strings.Contains(page, "0 verified by the marketplace") {
		t.Error("the counts changed; this test is asserting about a page that moved")
	}
}

func TestAnEntryPageEmbedsTheSameRecordAgentsJSONPublishes(t *testing.T) {
	// The page's machine-readable block exists so an agent can act on one listing
	// without fetching agents.json, so it has to be the same object. Both come
	// from one projection, and this pins that they cannot drift.
	f := loadFixture(t)
	at := time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)
	out := renderToDir(t, f.site(t, at, nil, nil))

	page := read(t, out, "other/index.html")
	const open = "<pre><code>"
	i := strings.Index(page, open)
	if i < 0 {
		t.Fatal("the entry page carries no machine-readable block")
	}
	rest := page[i+len(open):]
	j := strings.Index(rest, "</code></pre>")
	if j < 0 {
		t.Fatal("the machine-readable block is not closed")
	}
	// html/template escapes the JSON's quotes and angle brackets for the
	// document; unescape so it can be compared as JSON.
	block := html.UnescapeString(rest[:j])

	var embedded map[string]any
	if err := json.Unmarshal([]byte(block), &embedded); err != nil {
		t.Fatalf("the embedded block is not JSON: %v\n%s", err, block)
	}

	var payload struct {
		Agents []map[string]any `json:"agents"`
	}
	if err := json.Unmarshal([]byte(read(t, out, "agents.json")), &payload); err != nil {
		t.Fatal(err)
	}
	var filed map[string]any
	for _, a := range payload.Agents {
		if a["slug"] == "other" {
			filed = a
		}
	}
	if filed == nil {
		t.Fatal("agents.json has no entry for other")
	}
	want, _ := json.Marshal(filed)
	got, _ := json.Marshal(embedded)
	if string(got) != string(want) {
		t.Errorf("the embedded record differs from agents.json:\n got: %s\nwant: %s", got, want)
	}
}

func TestTheHealthVerdictIsStyledApartFromTheSignatureStates(t *testing.T) {
	// Up, down and unknown are a separate vocabulary from the signature states,
	// and the whole point of separate classes is that a reader can tell a
	// reachable endpoint from a verified signature without reading the words.
	checked := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)

	e := entry("vtessera", "A marketplace.")
	e.MCPEndpointURL = strptr("https://vtessera.example.com/mcp")
	up := site(t, e)
	up.HealthCheckedAt = &checked
	up.EndpointResponseMS = map[string]int{"vtessera": 42}
	page := read(t, renderTo(t, up), "vtessera/index.html")
	for _, want := range []string{
		`class="state state-up"`,
		"42 ms",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("an up entry's page does not contain %q", want)
		}
	}

	down := read(t, renderTo(t, deadEndpointSite(t)), "vtessera/index.html")
	if !strings.Contains(down, "state-down") {
		t.Error("a withheld endpoint is not styled as down")
	}

	never := site(t, entry("vtessera", "A marketplace."))
	if page := read(t, renderTo(t, never), "vtessera/index.html"); !strings.Contains(page, "state-unknown") {
		t.Error("an unchecked entry is not styled as unknown")
	}
}

func TestTheVerificationPageIsInTheSitemap(t *testing.T) {
	f := loadFixture(t)
	at := time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)
	out := renderToDir(t, f.site(t, at, nil, nil))
	if body := read(t, out, "sitemap.xml"); !strings.Contains(body, "<loc>https://example.test/verify/</loc>") {
		t.Error("the sitemap does not list the verification page")
	}
}

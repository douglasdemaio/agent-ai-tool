package render

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/douglasdemaio/agent-ai-tool/internal/content"
	"github.com/douglasdemaio/agent-ai-tool/internal/live"
)

//go:embed templates/*.html templates/robots.txt
var templates embed.FS

const LiveSlug = "vtessera"

type Site struct {
	Domain     string
	Entries    []content.Entry
	LiveAgents *live.AgentsResponse
	// LiveAgentsFetchedAt is when the agent feed was fetched or read from cache.
	// It is the fallback dating for a live listing whose feed is empty.
	LiveAgentsFetchedAt time.Time
	Metrics             *live.MetricsResponse
	MetricsFromCache    bool
	MetricsAge          time.Duration
	MetricsErr          error
	GeneratedAt         time.Time
	AssetsDir           string
	// ChangesPath is the committed change feed — the history a reader fetches
	// to catch up, and the baseline this build diffs against. Empty means the
	// directory publishes no feed, so no changes.json is written at all
	// rather than an empty one that would read as "nothing has ever changed".
	ChangesPath string
	// RecordChanges permits a write back to ChangesPath, and is set only for
	// builds that read committed data. See updateChangeFeed for why a build
	// that fetched fresh values publishes the feed without adding to it.
	RecordChanges bool
	// Unreachable names entries whose advertised endpoints a recent health
	// check could not reach. Their endpoints are withheld from every published
	// surface rather than advertised as callable, because the directory's claim
	// is that these endpoints work.
	Unreachable map[string]bool
	// EndpointDetails carries each unreachable entry's last failure reason, for
	// the page to show rather than leaving a blank field unexplained.
	EndpointDetails map[string]string
	// HealthCheckedAt is when that check ran, and nil when no trusted report
	// exists. Withheld endpoints carry it so a reader can tell a service that
	// is down from one that was merely never checked.
	HealthCheckedAt *time.Time
	// EndpointLastAlive is when each withheld endpoint was last judged alive,
	// keyed by slug. An entry absent from the map has never answered a check,
	// which is a different statement from being up and is rendered as such
	// rather than as a missing date.
	EndpointLastAlive map[string]time.Time
	// EndpointResponseMS is how long each entry's machine endpoint took to
	// answer at the last sweep, keyed by slug. An entry absent from the map
	// published no measurable answer, and agents.json omits the field rather
	// than printing a zero that reads as instant.
	EndpointResponseMS map[string]int

	// Marketplace is the marketplace's own account of itself, and the only source
	// of the key that AgentReports is checked against. It is nil when nothing has
	// answered, which leaves every verdict unverified rather than passing.
	Marketplace          *live.Health
	MarketplaceFromCache bool
	AgentReports         *live.AgentReportsResponse
	ReportsFromCache     bool
	ReportsFetchedAt     time.Time
	ReportsErr           error
}

// verificationKey is the marketplace's published key, or empty when there is
// none. Every signature on the site is checked against exactly this string, and
// an empty one checks nothing: the alternative would be to fall back to the key a
// signature names, which would let any key vouch for itself.
func (s Site) verificationKey() string {
	if s.Marketplace == nil {
		return ""
	}
	return s.Marketplace.VerificationKey
}

// VerifyAgents re-derives every signature over every agent card, using this
// package's encoder rather than anything the marketplace reported. The verdicts
// returned are the only ones rendered.
func (s Site) VerifyAgents() []live.AgentVerification {
	if s.LiveAgents == nil {
		return nil
	}
	byAgent := map[string]live.AgentReport{}
	if s.AgentReports != nil {
		byAgent = s.AgentReports.ByAgent()
	}
	// When the whole feed is missing, the reason belongs on every row. Falling
	// through to an empty report per agent would render them all as "unsigned",
	// which says the marketplace never attested anything rather than that this
	// build could not ask.
	feedErr := ""
	if s.AgentReports == nil {
		feedErr = errText(s.ReportsErr)
	}
	key := s.verificationKey()
	out := make([]live.AgentVerification, 0, len(s.LiveAgents.Agents))
	for _, agent := range s.LiveAgents.Agents {
		report, ok := byAgent[agent.ID]
		reason := feedErr
		if ok && report.Error != "" && !report.Attestation.Marketplace.Attested {
			reason = report.Error
		}
		if reason != "" {
			out = append(out, live.AgentVerification{
				AgentID:     agent.ID,
				CardName:    agent.Card.Name,
				CardURL:     agent.Card.URL,
				Marketplace: live.Verified{Reason: reason},
				Agent:       live.Verified{Reason: reason},
			})
			continue
		}
		out = append(out, live.VerifyAgent(agent, report.Attestation, report.Probe, key))
	}
	return out
}

type usage struct {
	Delivered int
	Disputed  int
	Cancelled int
}

// liveRow is one agent registered with vtessera, carrying its own usage so the
// vtessera page can badge each agent rather than only the marketplace total.
type liveRow struct {
	Agent live.Agent
	Usage *usage
}

// Withdrawn reports an agent the marketplace has taken off its listings.
//
// vtessera stops listing an agent it has retired or suspended, so a fresh feed
// will not contain it at all. This exists for the other case: this site serves
// committed snapshots when the marketplace cannot be reached, and a snapshot
// taken before a withdrawal goes on advertising a seller somebody was removed on
// purpose. Rendering the status the feed reported is what stops a cached page
// from quietly selling something withdrawn.
//
// An empty status is treated as active. Snapshots taken before the marketplace
// reported one carry no status, and hiding every agent in them would be a worse
// answer than showing them.
func (r liveRow) Withdrawn() bool {
	return r.Agent.Status != "" && r.Agent.Status != live.StatusActive
}

// WithdrawnReason is the status as a reader would understand it, or empty when
// the agent is listed normally.
func (r liveRow) WithdrawnReason() string {
	if !r.Withdrawn() {
		return ""
	}
	return r.Agent.Status + " by the marketplace"
}

type entryView struct {
	Entry content.Entry
	Usage *usage
	Live  []liveRow
	// LiveEmpty distinguishes a feed that answered and reported no agents from
	// one that was never consulted. A nil Live covers both as far as the
	// template's truthiness test is concerned, which would let an empty
	// marketplace look like a working one.
	LiveEmpty bool
	// MCPEndpoint is the entry's MCP endpoint — one an agent opens a session
	// against — and APIEndpoint is a plain HTTP endpoint it calls directly,
	// such as a JSON document to fetch. Neither is removed when a check fails:
	// Unreachable and EndpointDetail say that, and an address silently dropped
	// would leave a withheld row with no label to show, so the page could not
	// tell a dead MCP server from a dead JSON API. What agents.json publishes
	// is nilled on the same rule as before: a URL the site has just failed to
	// reach must not be handed out as if it worked.
	MCPEndpoint *string
	APIEndpoint *string
	// Unreachable reports that the endpoint is withheld, so the page can say
	// why rather than silently omitting a field an agent expects.
	Unreachable bool
	// EndpointDetail carries the check's last failure reason.
	EndpointDetail string
	// HealthCheckedAt is when the check ran, for the same reason.
	HealthCheckedAt *time.Time
	// LastAlive is when this endpoint last answered, or nil when it never has.
	// A withheld endpoint without one is a service nobody has seen work, which
	// says more than an empty date would.
	LastAlive *time.Time
	// LastChecked is when an automated check last saw this entry's endpoint
	// answer: the sweep that just judged it, or the last sweep that did while
	// it is down. It is a machine's observation about reachability and carries
	// no claim that the summary, category or terms still describe the service —
	// which is the claim Entry.LastVerified makes and only a human can make
	// again. The two are shown side by side precisely so neither is read as the
	// other, and it is nil when nothing has ever checked this entry.
	LastChecked *time.Time
	// ReviewDue reports that a curated entry has gone too long without a human
	// confirming it still describes reality. The health check proves an
	// endpoint answers; it cannot prove the summary is still true.
	ReviewDue bool
	// Verification is the marketplace's signed provenance, shown on every entry
	// page rather than only on the marketplace's own. A reader arriving at some
	// other service has no reason to know a marketplace exists, so a claim that
	// only appears on one page of four is a claim most readers never see.
	Verification verification
}

// verification is what this directory checked, and how it checked it. It exists
// to be read by someone deciding whether to trust a line elsewhere on the page,
// so it names the key everything was checked against instead of asking to be
// taken on faith.
type verification struct {
	// Known is false when no marketplace answered. That is not the same as
	// nothing to say: the block renders as explicitly unverified rather than
	// disappearing, so an absent feed never reads as an absence of problems.
	Known          bool
	Key            string
	Status         string
	Version        string
	Cluster        string
	SettlementTier string
	Sandbox        bool
	FromCache      bool
	FetchedAt      time.Time
	// FetchedAge is how long ago that was, as a duration, because the age
	// template function takes a duration and cannot read a clock of its own.
	FetchedAge     time.Duration
	Err            string
	Agents         []live.AgentVerification
	Checked        int
	MarketVerified int
	AgentVerified  int
	// MarketUnrecorded counts cards the marketplace holds no attestation for,
	// which is every card registered before it began signing them. Reported apart
	// from the unsigned because lumping them together told readers a working
	// marketplace had refused to vouch for anybody.
	MarketUnrecorded int
	Probed           int
	ProbeVerified    int
	ProbeFailed      int
	ProbeNone        int
	ProbeUnchecked   int
}

// Summary is the one-line count for the index page, where the per-agent detail
// would be a wall of text nobody came for.
func (v verification) Summary() string {
	switch {
	case !v.Known:
		return "no marketplace answered"
	case v.Checked == 0:
		return "no agent cards to check"
	case v.MarketVerified == v.Checked && v.AgentVerified == v.Checked:
		return fmt.Sprintf("%d of %d cards verified by the marketplace and the agent", v.Checked, v.Checked)
	case v.MarketVerified > 0:
		return fmt.Sprintf("%d of %d cards verified by the marketplace", v.MarketVerified, v.Checked)
	case v.MarketUnrecorded == v.Checked:
		// "none of 6 cards verified" is what this used to say, with every one of
		// the six registered before the marketplace signed cards. It reads as a
		// finding about the marketplace and is really a fact about when the
		// agents registered.
		return fmt.Sprintf("no attestation recorded for any of %d cards, all registered before the marketplace signed cards", v.Checked)
	default:
		return fmt.Sprintf("none of %d cards verified", v.Checked)
	}
}

// ClusterOrUnknown keeps an empty cluster from rendering as a blank field. A
// marketplace with settlement off says so; one that stopped reporting it should
// not look like a marketplace that never claimed a chain.
func (v verification) ClusterOrUnknown() string {
	if v.Cluster == "" {
		return "none reported"
	}
	return v.Cluster
}

type pageData struct {
	Site        Site
	Title       string
	Description string
	Canonical   string
	Views       []entryView
	View        *entryView
	// Verification is the same block every entry page carries, hoisted here for
	// the index. It is computed once per render, so the two surfaces cannot
	// disagree about what was checked.
	Verification verification
	// BadgedCount lets the templates drop the "prefer a delivered count"
	// instruction when no entry has one, rather than telling an agent to rank
	// on a field the build produced no values for.
	BadgedCount int
	Year        int
}

func (s Site) MetricsStale() bool {
	return s.Metrics != nil && s.MetricsAge > live.StaleAfter
}

func (s Site) MetricsUnavailable() bool {
	return s.MetricsErr != nil && s.Metrics == nil
}

func rowsFor(agents []live.Agent, byAgent map[string]usage) []liveRow {
	rows := make([]liveRow, 0, len(agents))
	for _, agent := range agents {
		row := liveRow{Agent: agent}
		if u, ok := byAgent[agent.ID]; ok && u.Delivered >= live.BadgeFloor {
			copied := u
			row.Usage = &copied
		}
		rows = append(rows, row)
	}
	return rows
}

func (s Site) usageByAgent() map[string]usage {
	if s.Metrics == nil {
		return nil
	}
	out := make(map[string]usage, len(s.Metrics.Agents))
	for _, a := range s.Metrics.Agents {
		if live.IsProbeAgent(a.AgentID) {
			// Our own test agents must never earn an entry a delivery badge.
			// The banner is labelled as test activity; a badge next to an
			// entry is a claim about that entry being exercised, and ours
			// exercising it is not that.
			continue
		}
		out[a.AgentID] = usage{Delivered: a.Delivered, Disputed: a.Disputed, Cancelled: a.Cancelled}
	}
	return out
}

// MetricsLead is the banner's headline, and its job is to name whose activity
// the numbers describe.
//
// vtessera publishes aggregate totals and cannot split them by who produced
// them, so while any of our own probe agents have traded, the figures include
// our tests. The honest options are therefore to say so or to show nothing:
// what the directory may not do is print the same totals under a heading that
// invites a reader to take them as outside usage.
func (s Site) MetricsLead() string {
	if s.Metrics == nil {
		return ""
	}
	switch {
	case s.Metrics.AllProbes():
		return "Test activity on vtessera — our own probe agents, no outside usage yet"
	case s.Metrics.AnyProbes():
		return "What agents are doing on vtessera, including this repository's own test agents"
	default:
		return "What agents are actually doing on vtessera"
	}
}

// viewFor builds a view and applies the health verdict. The curated entry keeps
// its own endpoint URLs as filed: the source file stays the record of what the
// service publishes, while the rendered site withholds an endpoint a check could
// not reach. Editing the JSON to remove a URL would lose that distinction the
// next time the service came back.
func (s Site) viewFor(e content.Entry) entryView {
	view := entryView{
		Entry:           e,
		MCPEndpoint:     e.MCPEndpointURL,
		APIEndpoint:     e.APIURL,
		Unreachable:     s.Unreachable[e.Slug],
		HealthCheckedAt: s.HealthCheckedAt,
		LastChecked:     s.lastChecked(e),
	}
	if view.Unreachable {
		if detail, ok := s.endpointDetail(e.Slug); ok {
			view.EndpointDetail = detail
		}
		if when, ok := s.EndpointLastAlive[e.Slug]; ok {
			view.LastAlive = &when
		}
	}
	return view
}

// statusFor is what this build can prove about an entry right now.
//
// "up" and "down" come from a report this build trusts; "unknown" is the honest
// answer when there is none, when the report has aged past StaleAfter, or when
// the entry publishes nothing worth probing. Unknown is not a softer word for
// up: an agent that needs a live endpoint skips it exactly as it skips down,
// which is the whole point of the third state.
func (s Site) statusFor(e content.Entry) string {
	if s.HealthCheckedAt == nil {
		return "unknown"
	}
	if e.MachineEndpoint() == nil && e.AgentCardURL == nil {
		return "unknown"
	}
	if s.Unreachable[e.Slug] {
		return "down"
	}
	return "up"
}

// responseMS is how long this entry's machine endpoint took to answer, or nil
// when the sweep measured nothing it could publish — no endpoint, no probe, or
// no response at all.
func (s Site) responseMS(e content.Entry) *int {
	// The same gate as the status: a timing measured by a report this build no
	// longer trusts must not outlive the verdict it came with, or agents.json
	// publishes a precise number next to "unknown".
	if s.HealthCheckedAt == nil {
		return nil
	}
	ms, ok := s.EndpointResponseMS[e.Slug]
	if !ok {
		return nil
	}
	return &ms
}

// lastChecked is when the last automated check saw this entry's endpoints
// answer, or nil when none ever has.
//
// Three cases, and the difference between them is the whole point: an entry
// with nothing to probe has no check at all; an entry whose endpoint a fresh
// report accepts is dated by that report; an entry currently withheld is dated
// by the last answer before the outage, carried across reports so the number
// does not reset to whichever check keeps noticing the failure. An entry
// published without a trusted report is unchecked, not up — a HealthCheckedAt
// of nil means no sweep has been believed recently, and claiming a check on
// that would be the same mistake as letting a stale report demote.
//
// Entry.LastVerified is never read here and never written by anything this
// touches. The probe can observe an HTTP response; only a human can confirm
// that the entry still describes the service.
func (s Site) lastChecked(e content.Entry) *time.Time {
	if e.MCPEndpointURL == nil && e.APIURL == nil && e.AgentCardURL == nil {
		return nil
	}
	if !s.Unreachable[e.Slug] {
		if s.HealthCheckedAt == nil {
			return nil
		}
		when := *s.HealthCheckedAt
		return &when
	}
	if when, ok := s.EndpointLastAlive[e.Slug]; ok {
		return &when
	}
	return nil
}

func (s Site) endpointDetail(slug string) (string, bool) {
	if s.EndpointDetails == nil {
		return "", false
	}
	detail, ok := s.EndpointDetails[slug]
	return detail, ok
}

// resolve merges the live vtessera feed over the curated stub.
//
// The feed supplies the list of registered agents, so it owns the entry's source
// tag, its last_verified, and the agent rows. It does not supply marketplace
// identity: those agents are tenants of vtessera, not vtessera itself, so their
// names and URLs must not overwrite the curated ones. The curated summary is
// kept for the same reason — an agent list carries no marketplace prose, and
// inventing one would be worse than wording a human wrote.
// verificationBlock assembles what was checked. It is built once per render and
// shared by every view, because the verdicts are the same on each page: a
// per-entry recomputation would make them free to disagree, and a reader
// comparing two pages would have no way to tell which one to believe.
func (s Site) verificationBlock() verification {
	block := verification{Err: errText(s.ReportsErr)}
	if s.Marketplace != nil {
		block.Known = true
		block.Key = s.Marketplace.VerificationKey
		block.Status = s.Marketplace.Status
		block.Version = s.Marketplace.Version
		block.Cluster = s.Marketplace.Cluster
		block.SettlementTier = s.Marketplace.SettlementTier
		block.Sandbox = s.Marketplace.Sandbox
		block.FromCache = s.MarketplaceFromCache
	}
	block.FetchedAt = s.ReportsFetchedAt
	if !s.ReportsFetchedAt.IsZero() {
		block.FetchedAge = s.GeneratedAt.Sub(s.ReportsFetchedAt)
	}
	for _, v := range s.VerifyAgents() {
		block.Checked++
		if v.Marketplace.Valid {
			block.MarketVerified++
		}
		if v.Agent.Valid {
			block.AgentVerified++
		}
		if !v.Marketplace.Recorded {
			block.MarketUnrecorded++
		}
		switch {
		case !v.Probe.Probed:
			block.ProbeNone++
		case !v.Probe.Verified:
			// A report exists but does not check out. Counting it as never probed
			// would turn a broken signature into a clean bill of health.
			block.Probed++
			block.ProbeUnchecked++
		case v.Probe.Passed:
			block.Probed++
			block.ProbeVerified++
		default:
			block.Probed++
			block.ProbeFailed++
		}
		block.Agents = append(block.Agents, v)
	}
	return block
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (s Site) resolve() ([]entryView, error) {
	byAgent := s.usageByAgent()
	block := s.verificationBlock()
	views := make([]entryView, 0, len(s.Entries))
	found := false

	for _, e := range s.Entries {
		view := s.viewFor(e)
		if u, ok := byAgent[e.Slug]; ok && u.Delivered >= live.BadgeFloor {
			copied := u
			view.Usage = &copied
		}
		if e.Slug == LiveSlug {
			found = true
			if s.LiveAgents != nil {
				agents := s.LiveAgents.Normalized().Agents
				// An empty marketplace is a real state, not a fault. The feed
				// still owns the entry's provenance, so it is marked live and
				// the emptiness is rendered explicitly rather than dropped.
				view.Entry.Source = content.SourceLive
				view.Entry.LastVerified = s.liveVerified()
				view.Live = rowsFor(agents, byAgent)
				view.LiveEmpty = len(agents) == 0
			}
		}
		// Computed here, not in viewFor: the override above rewrites a live
		// entry's source and date, and judging it on the curated stub it arrived
		// as would flag the marketplace as overdue the moment its stub aged out.
		view.ReviewDue = view.Entry.ReviewDue(s.GeneratedAt)
		view.Verification = block
		views = append(views, view)
	}

	// Without a curated entry there is no vetted name, URL, or summary to stand
	// in for the marketplace, and an empty feed carries no agent card to borrow
	// one from. So a feed-only entry exists only once the feed has something to
	// describe; otherwise the service is simply not deployed yet.
	if !found && s.LiveAgents != nil {
		agents := s.LiveAgents.Normalized().Agents
		if len(agents) == 0 {
			return views, nil
		}
		view := entryView{
			Entry: content.Entry{
				Slug:         LiveSlug,
				Name:         LiveSlug,
				Summary:      "A2A agent marketplace with signed, non-custodial settlement.",
				URL:          agents[0].Card.URL,
				Source:       content.SourceLive,
				LastVerified: s.liveVerified(),
			},
			Live: rowsFor(agents, byAgent),
		}
		if u, ok := byAgent[LiveSlug]; ok && u.Delivered >= live.BadgeFloor {
			copied := u
			view.Usage = &copied
		}
		view.ReviewDue = view.Entry.ReviewDue(s.GeneratedAt)
		view.Verification = block
		view.Verification = block
		views = append(views, view)
	}
	return views, nil
}

// canonical returns the one address for a page. Entry pages are written as
// <slug>/index.html, which the host serves at /<slug>/ and redirects /<slug>
// there, so the canonical carries the trailing slash. Naming the redirecting
// form instead would ask crawlers to consolidate on a URL that immediately
// redirects, which is the opposite of what a canonical is for.
func (s Site) canonical(path string) string {
	clean := strings.TrimPrefix(path, "/")
	if clean == "" {
		return "https://" + s.Domain + "/"
	}
	return "https://" + s.Domain + "/" + strings.TrimSuffix(clean, "/") + "/"
}

// stampFormat is how a health date is written to a human. The machine form
// stays RFC 3339 in agents.json; this is the one on the page and in llms.txt,
// where the timezone is worth spelling out because "14:54" alone invites a
// reader to assume their own.
const stampFormat = "2006-01-02 15:04 UTC"

func (s Site) Render(outDir string) error {
	views, err := s.resolve()
	if err != nil {
		return err
	}
	// The feed is settled before anything is written: it is the one output
	// that also reads and rewrites a committed file, so a failure here must
	// happen before half a site is on disk.
	var changes []ChangeRecord
	if s.ChangesPath != "" {
		changes, err = s.updateChangeFeed(s.ChangesPath, views)
		if err != nil {
			return err
		}
	}
	tmpl, err := template.New("site").Funcs(template.FuncMap{
		"age":    humanAge,
		"verify": func(t time.Time) string { return t.UTC().Format("2006-01-02") },
		// stamp is for a date that may be absent. Templates branch on the
		// pointer first, so the empty case here is a guard rather than a
		// rendering: an unguarded nil would panic in a template, which is the
		// least legible way to report a missing health record.
		"stamp": func(t *time.Time) string {
			if t == nil {
				return ""
			}
			return t.UTC().Format(stampFormat)
		},
		// Bodies are published as formatted JSON so a reader can copy them
		// directly. Indented rather than compact because this is documentation
		// being read by a person deciding whether to paste it.
		"json": func(v any) (string, error) {
			b, err := json.MarshalIndent(v, "", "  ")
			if err != nil {
				return "", err
			}
			return string(b), nil
		},
	}).ParseFS(templates, "templates/*.html", "templates/robots.txt")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	base := pageData{Site: s, Year: s.GeneratedAt.UTC().Year(), Verification: s.verificationBlock()}

	home := base
	home.Title = s.Domain
	home.Description = "A directory of tools an AI agent can actually connect to: registries, marketplaces, and compute services. Fetch agents.json once."
	home.Canonical = s.canonical("")
	home.Views = views
	home.BadgedCount = s.badgedCount(views)
	if err := writePage(tmpl, filepath.Join(outDir, "index.html"), "index.html", home); err != nil {
		return err
	}

	for i := range views {
		page := base
		page.Title = views[i].Entry.Name
		page.Description = views[i].Entry.Summary
		page.Canonical = s.canonical(views[i].Entry.Slug)
		page.View = &views[i]
		if err := writePage(tmpl, filepath.Join(outDir, views[i].Entry.Slug, "index.html"), "entry.html", page); err != nil {
			return err
		}
	}

	notFound := base
	notFound.Title = "Not found"
	notFound.Description = "No such page."
	// A 404 is not a page anyone should index, so it names no canonical at all
	// rather than pointing the checker at a path that does not exist.
	notFound.Canonical = ""
	if err := writePage(tmpl, filepath.Join(outDir, "404.html"), "404.html", notFound); err != nil {
		return err
	}

	if err := writePage(tmpl, filepath.Join(outDir, "robots.txt"), "robots.txt", base); err != nil {
		return err
	}
	if err := s.writeJSON(filepath.Join(outDir, "agents.json"), s.agentsJSON(views)); err != nil {
		return err
	}
	if s.ChangesPath != "" {
		if err := s.writeJSON(filepath.Join(outDir, "changes.json"), s.changeFeedJSON(changes)); err != nil {
			return err
		}
	}
	if err := s.writeJSON(filepath.Join(outDir, ".well-known", "agent-card.json"), s.directoryCard(views)); err != nil {
		return err
	}
	if err := s.write(filepath.Join(outDir, "sitemap.xml"), s.sitemap(views)); err != nil {
		return err
	}
	if err := s.write(filepath.Join(outDir, "llms.txt"), []byte(s.llms(views))); err != nil {
		return err
	}
	if err := s.write(filepath.Join(outDir, "CNAME"), []byte(s.Domain+"\n")); err != nil {
		return err
	}
	return s.copyAssets(outDir)
}

// liveVerified dates a live listing from the feed rather than by hand, so nobody
// has to remember to re-stamp it.
//
// It prefers the newest agent's updatedAt. An empty marketplace has no agent to
// take a date from, and that is a real state rather than a fault, so it falls
// back to when the feed was fetched: we did reach the service and parse it, and
// that is a real observation. Returning the zero time instead is the one answer
// that is always wrong — it publishes as 0001-01-01 in agents.json, sorts the
// sitemap as the oldest page on the site, and dates the JSON-LD a year before
// the directory existed.
func (s Site) liveVerified() time.Time {
	if s.LiveAgents == nil {
		return time.Time{}
	}
	if t := s.LiveAgents.UpdatedAt(); !t.IsZero() {
		return t
	}
	return s.LiveAgentsFetchedAt
}

func writePage(tmpl *template.Template, path, name string, data pageData) error {
	var buf strings.Builder
	if err := tmpl.ExecuteTemplate(&buf, name, data); err != nil {
		return fmt.Errorf("render %s: %w", name, err)
	}
	return writeFile(path, []byte(buf.String()))
}

func (s Site) write(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}

func (s Site) writeJSON(path string, payload any) error {
	body, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return err
	}
	return s.write(path, append(body, '\n'))
}

func (s Site) copyAssets(outDir string) error {
	if s.AssetsDir == "" {
		return nil
	}
	items, err := os.ReadDir(s.AssetsDir)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.IsDir() {
			continue
		}
		src := filepath.Join(s.AssetsDir, item.Name())
		dst := filepath.Join(outDir, "assets", item.Name())
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := copyFile(src, dst); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func writeFile(path string, body []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
}

func humanAge(d time.Duration) string {
	// Anything under a minute reads as "moments". Rounding a 30-second-old
	// snapshot down to "0 minutes ago" states a precision the data does not
	// have, and it is the common case right after a refresh.
	if d < time.Minute {
		return "moments ago"
	}
	var n int
	var unit string
	switch {
	case d < time.Hour:
		n, unit = int(d.Minutes()), "minute"
	case d < 24*time.Hour:
		n, unit = int(d.Hours()), "hour"
	default:
		n, unit = int(d.Hours()/24), "day"
	}
	if n != 1 {
		unit += "s"
	}
	return fmt.Sprintf("%d %s ago", n, unit)
}

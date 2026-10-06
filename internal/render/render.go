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

// endpointFor returns the machine endpoint an entry advertises, or nil when the
// entry has none or a recent check says it cannot be reached.
func (s Site) endpointFor(e content.Entry) *string {
	if e.MCPEndpointURL == nil {
		return nil
	}
	if s.Unreachable[e.Slug] {
		return nil
	}
	return e.MCPEndpointURL
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
	// Endpoint is the machine endpoint this entry advertises, withheld when a
	// recent health check could not reach it.
	Endpoint *string
	// Unreachable reports that the endpoint is withheld, so the page can say
	// why rather than silently omitting a field an agent expects.
	Unreachable bool
	// EndpointDetail carries the check's last failure reason.
	EndpointDetail string
	// HealthCheckedAt is when the check ran, for the same reason.
	HealthCheckedAt *time.Time
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
	Probed         int
	ProbeVerified  int
	ProbeFailed    int
	ProbeNone      int
	ProbeUnchecked int
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
		out[a.AgentID] = usage{Delivered: a.Delivered, Disputed: a.Disputed, Cancelled: a.Cancelled}
	}
	return out
}

// viewFor builds a view and applies the health verdict. The curated entry keeps
// its own mcp_endpoint_url: the source file stays the record of what the service
// publishes, while the rendered site withholds an endpoint a check could not
// reach. Editing the JSON to remove a URL would lose that distinction the next
// time the service came back.
func (s Site) viewFor(e content.Entry) entryView {
	view := entryView{
		Entry:           e,
		Endpoint:        s.endpointFor(e),
		Unreachable:     s.Unreachable[e.Slug],
		HealthCheckedAt: s.HealthCheckedAt,
	}
	if view.Unreachable {
		if detail, ok := s.endpointDetail(e.Slug); ok {
			view.EndpointDetail = detail
		}
	}
	return view
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

func (s Site) Render(outDir string) error {
	views, err := s.resolve()
	if err != nil {
		return err
	}
	tmpl, err := template.New("site").Funcs(template.FuncMap{
		"age":    humanAge,
		"verify": func(t time.Time) string { return t.UTC().Format("2006-01-02") },
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

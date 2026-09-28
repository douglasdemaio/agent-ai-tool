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
	Domain           string
	Entries          []content.Entry
	LiveAgents       *live.AgentsResponse
	Metrics          *live.MetricsResponse
	MetricsFromCache bool
	MetricsAge       time.Duration
	MetricsErr       error
	GeneratedAt      time.Time
	AssetsDir        string
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

type entryView struct {
	Entry content.Entry
	Usage *usage
	Live  []liveRow
	// LiveEmpty distinguishes a feed that answered and reported no agents from
	// one that was never consulted. A nil Live covers both as far as the
	// template's truthiness test is concerned, which would let an empty
	// marketplace look like a working one.
	LiveEmpty bool
}

type pageData struct {
	Site        Site
	Title       string
	Description string
	Canonical   string
	Views       []entryView
	View        *entryView
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

// resolve merges the live vtessera feed over the curated stub.
//
// The feed supplies the list of registered agents, so it owns the entry's source
// tag, its last_verified, and the agent rows. It does not supply marketplace
// identity: those agents are tenants of vtessera, not vtessera itself, so their
// names and URLs must not overwrite the curated ones. The curated summary is
// kept for the same reason — an agent list carries no marketplace prose, and
// inventing one would be worse than wording a human wrote.
func (s Site) resolve() ([]entryView, error) {
	byAgent := s.usageByAgent()
	views := make([]entryView, 0, len(s.Entries))
	found := false

	for _, e := range s.Entries {
		view := entryView{Entry: e}
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
				view.Entry.LastVerified = s.LiveAgents.UpdatedAt()
				view.Live = rowsFor(agents, byAgent)
				view.LiveEmpty = len(agents) == 0
			}
		}
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
				LastVerified: s.LiveAgents.UpdatedAt(),
			},
			Live: rowsFor(agents, byAgent),
		}
		if u, ok := byAgent[LiveSlug]; ok && u.Delivered >= live.BadgeFloor {
			copied := u
			view.Usage = &copied
		}
		views = append(views, view)
	}
	return views, nil
}

func (s Site) canonical(path string) string {
	clean := strings.TrimPrefix(path, "/")
	if clean == "" {
		return "https://" + s.Domain
	}
	return "https://" + s.Domain + "/" + clean
}

func (s Site) Render(outDir string) error {
	views, err := s.resolve()
	if err != nil {
		return err
	}
	tmpl, err := template.New("site").Funcs(template.FuncMap{
		"age":    humanAge,
		"verify": func(t time.Time) string { return t.UTC().Format("2006-01-02") },
	}).ParseFS(templates, "templates/*.html", "templates/robots.txt")
	if err != nil {
		return err
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}

	base := pageData{Site: s, Year: s.GeneratedAt.UTC().Year()}

	home := base
	home.Title = s.Domain
	home.Description = "A directory of tools an AI agent can actually connect to: registries, marketplaces, and compute services. Fetch agents.json once."
	home.Canonical = s.canonical("")
	home.Views = views
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
	notFound.Canonical = s.canonical("404.html")
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
	switch {
	case d <= 0:
		return "moments ago"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

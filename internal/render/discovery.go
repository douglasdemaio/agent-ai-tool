package render

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"strings"
	"time"
)

// jsonLD serialises a value for embedding inside a <script> block. encoding/json
// does not escape <, >, or &, so a value containing "</script>" would otherwise
// close the element early and let curated content inject markup. Escaping them
// as <, >, and & keeps the document valid JSON.
func jsonLD(v any) (template.JS, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	escaped := strings.NewReplacer(
		"<", `<`,
		">", `>`,
		"&", `&`,
	).Replace(string(body))
	return template.JS(escaped), nil
}

func (s Site) EntryJSONLD(v *entryView) (template.JS, error) {
	if v == nil {
		return "", errors.New("no entry supplied for JSON-LD")
	}
	return jsonLD(s.schemaOrg(*v))
}

func (s Site) schemaOrg(v entryView) map[string]any {
	node := map[string]any{
		"@context":     "https://schema.org",
		"@type":        "SoftwareApplication",
		"name":         v.Entry.Name,
		"url":          s.canonical(v.Entry.Slug),
		"description":  v.Entry.Summary,
		"dateModified": v.Entry.LastVerified.UTC().Format("2006-01-02"),
	}
	if v.Entry.Category != "" {
		node["applicationCategory"] = v.Entry.Category
	}
	if v.Entry.Access != "" {
		node["offers"] = map[string]any{
			"@type":         "Offer",
			"price":         "0",
			"priceCurrency": "USD",
			"description":   v.Entry.Access,
		}
	}
	return node
}

type jsonEntry struct {
	Slug           string    `json:"slug"`
	Name           string    `json:"name"`
	Summary        string    `json:"summary"`
	URL            string    `json:"url"`
	AgentCardURL   *string   `json:"agent_card_url"`
	MCPEndpointURL *string   `json:"mcp_endpoint_url"`
	Category       string    `json:"category,omitempty"`
	Access         string    `json:"access,omitempty"`
	Source         string    `json:"source"`
	LastVerified   time.Time `json:"last_verified"`
	Page           string    `json:"page"`
	Delivered      *int      `json:"delivered,omitempty"`
	EndpointDown   bool      `json:"endpoint_unreachable,omitempty"`
	EndpointReason string    `json:"endpoint_unreachable_reason,omitempty"`
}

func (s Site) jsonEntries(views []entryView) []jsonEntry {
	out := make([]jsonEntry, 0, len(views))
	for _, v := range views {
		entry := jsonEntry{
			Slug:         v.Entry.Slug,
			Name:         v.Entry.Name,
			Summary:      v.Entry.Summary,
			URL:          v.Entry.URL,
			AgentCardURL: v.Entry.AgentCardURL,
			// The endpoint as rendered, not as filed. An agent reading this
			// file must not be handed a URL the site itself has just failed to
			// reach.
			MCPEndpointURL: v.Endpoint,
			Category:       v.Entry.Category,
			Access:         v.Entry.Access,
			Source:         v.Entry.Source,
			LastVerified:   v.Entry.LastVerified,
			Page:           s.canonical(v.Entry.Slug),
		}
		if v.Unreachable {
			entry.EndpointDown = true
			entry.EndpointReason = v.EndpointDetail
		}
		if v.Usage != nil {
			delivered := v.Usage.Delivered
			entry.Delivered = &delivered
		}
		out = append(out, entry)
	}
	return out
}

func (s Site) agentsJSON(views []entryView) map[string]any {
	return map[string]any{
		"domain":      s.Domain,
		"generatedAt": s.GeneratedAt.UTC().Format(time.RFC3339),
		"description": "Every entry on this directory, with the endpoints an agent needs to connect to each one.",
		"agents":      s.jsonEntries(views),
	}
}

func (s Site) directoryCard(views []entryView) map[string]any {
	skills := make([]map[string]any, 0, len(views))
	for _, v := range views {
		// A skill carrying only a name tells an agent it exists and nothing
		// about when to reach for it, which is the one thing it needs. The
		// summary is the curated sentence written for exactly that, and the
		// endpoint is the address the agent would actually call.
		skill := map[string]any{
			"id":          v.Entry.Slug,
			"name":        v.Entry.Name,
			"description": v.Entry.Summary,
			"tags":        []string{v.Entry.Category},
		}
		if v.Endpoint != nil {
			skill["endpoint"] = *v.Endpoint
		} else if v.Unreachable {
			// A withheld endpoint is recorded as withheld rather than pointed
			// at the home page, so an agent never mistakes a browsing URL for
			// something it can call.
			skill["endpointUnavailable"] = true
		} else {
			skill["endpoint"] = v.Entry.URL
		}
		skills = append(skills, skill)
	}
	card := map[string]any{
		"name":         s.Domain,
		"description":  s.directoryDescription(views),
		"url":          s.canonical(""),
		"version":      s.GeneratedAt.UTC().Format("2006-01-02"),
		"capabilities": map[string]any{"tools": skills},
		"skills":       skills,
		"directory":    map[string]any{"url": s.canonical(""), "format": "agents.json", "count": len(views)},
	}
	return card
}

// badgedCount reports how many entries carry a recorded delivery figure. The
// guidance written for agents is conditioned on it: a directory with no live
// data should not tell an agent to rank by a count that does not exist.
func (s Site) badgedCount(views []entryView) int {
	n := 0
	for _, v := range views {
		if v.Usage != nil {
			n++
		}
	}
	return n
}

// directoryDescription states the directory's own guidance. It is generated
// from the data rather than written once, so it cannot keep telling an agent to
// sort on a field the build produced no values for.
func (s Site) directoryDescription(views []entryView) string {
	desc := "A directory of tools an AI agent can actually connect to. Fetch " +
		s.canonical("") + "agents.json once to get every entry with its endpoints, " +
		"then filter that one response locally by category and prefer the most " +
		"recent last_verified."
	if s.badgedCount(views) == 0 {
		return desc + " No entry currently carries a recorded usage count, so " +
			"judge each one on its endpoint and last_verified date."
	}
	return desc + " Entries carrying a delivered count have been exercised; " +
		"prefer them when several entries fit the same task."
}

func (s Site) llms(views []entryView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", s.Domain)
	b.WriteString("A directory of tools an AI agent can actually connect to. Each entry names the\n")
	b.WriteString("endpoint an agent should call, plus the agent card and MCP endpoint when the\n")
	b.WriteString("service publishes them.\n\n")
	fmt.Fprintf(&b, "For machine-readable data fetch %sagents.json; that single file\n", s.canonical(""))
	b.WriteString("carries every entry and its endpoints. This file is for a reader skimming prose.\n\n")
	b.WriteString("## How to use this efficiently (one fetch)\n\n")
	b.WriteString("Agents looking for compute or services should make exactly one request:\n\n")
	fmt.Fprintf(&b, "  curl -s %sagents.json\n\n", s.canonical(""))
	b.WriteString("Then filter locally by `category` and prefer the most recent\n")
	b.WriteString("`last_verified`. Use the `registry` entry to find connectable tools and\n")
	b.WriteString("the `marketplace` entry (vtessera) to settle work with signed,\n")
	b.WriteString("non-custodial receipts. Cache by `generatedAt`; snapshots refresh daily\n")
	b.WriteString("and are marked stale after 14 days, so re-fetch at most once a day.\n\n")
	if s.badgedCount(views) == 0 {
		b.WriteString("No entry currently carries a `delivered` count. Treat the field as\n")
		b.WriteString("absent rather than as zero, and judge an entry on its endpoint and\n")
		b.WriteString("`last_verified` date instead.\n\n")
	} else {
		b.WriteString("A `delivered` count means an entry has recorded real completed trades;\n")
		b.WriteString("3 or more earns a badge. Prefer a badged entry when several fit the\n")
		b.WriteString("same task, and fall back to an unbadged one otherwise.\n\n")
	}
	b.WriteString("```python\n")
	b.WriteString("import json, urllib.request\n")
	fmt.Fprintf(&b, "d = json.load(urllib.request.urlopen(\"%sagents.json\"))\n", s.canonical(""))
	b.WriteString("for e in d[\"agents\"]:\n")
	b.WriteString("    print(e[\"slug\"], e[\"url\"], e.get(\"delivered\"))\n")
	b.WriteString("```\n\n")
	b.WriteString("## Entries\n\n")
	for _, v := range views {
		fmt.Fprintf(&b, "### %s\n\n", v.Entry.Name)
		fmt.Fprintf(&b, "%s\n\n", v.Entry.Summary)
		fmt.Fprintf(&b, "- URL: %s\n", v.Entry.URL)
		if v.Entry.AgentCardURL != nil {
			fmt.Fprintf(&b, "- Agent card: %s\n", *v.Entry.AgentCardURL)
		}
		if v.Endpoint != nil {
			fmt.Fprintf(&b, "- MCP endpoint: %s\n", *v.Endpoint)
		}
		if v.Unreachable {
			b.WriteString("- MCP endpoint: withheld, it did not answer a recent health check\n")
			if v.EndpointDetail != "" {
				fmt.Fprintf(&b, "  (%s)\n", v.EndpointDetail)
			}
		}
		if v.Entry.Category != "" {
			fmt.Fprintf(&b, "- Category: %s\n", v.Entry.Category)
		}
		fmt.Fprintf(&b, "- Last verified: %s\n", v.Entry.LastVerified.UTC().Format("2006-01-02"))
		if v.Usage != nil {
			fmt.Fprintf(&b, "- Deliveries recorded: %d\n", v.Usage.Delivered)
		}
		fmt.Fprintf(&b, "- Directory page: %s\n\n", s.canonical(v.Entry.Slug))
	}
	if s.Metrics != nil {
		b.WriteString("## Marketplace usage\n\n")
		fmt.Fprintf(&b, "Recorded across the vtessera marketplace: %d delivered, %d disputed,\n", s.Metrics.Totals.Delivered, s.Metrics.Totals.Disputed)
		fmt.Fprintf(&b, "%d cancelled, across %d distinct consumers and %d distinct services in use.\n", s.Metrics.Totals.Cancelled, s.Metrics.Totals.Consumers, s.Metrics.Totals.Services)
	}
	return b.String()
}

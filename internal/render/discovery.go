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
}

func (s Site) jsonEntries(views []entryView) []jsonEntry {
	out := make([]jsonEntry, 0, len(views))
	for _, v := range views {
		entry := jsonEntry{
			Slug:           v.Entry.Slug,
			Name:           v.Entry.Name,
			Summary:        v.Entry.Summary,
			URL:            v.Entry.URL,
			AgentCardURL:   v.Entry.AgentCardURL,
			MCPEndpointURL: v.Entry.MCPEndpointURL,
			Category:       v.Entry.Category,
			Access:         v.Entry.Access,
			Source:         v.Entry.Source,
			LastVerified:   v.Entry.LastVerified,
			Page:           s.canonical(v.Entry.Slug),
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
	skills := make([]map[string]string, 0, len(views))
	for _, v := range views {
		skills = append(skills, map[string]string{
			"id":   v.Entry.Slug,
			"name": v.Entry.Name,
		})
	}
	return map[string]any{
		"name":         s.Domain,
		"description":  "A directory of tools an AI agent can actually connect to. Fetch /agents.json for machine-readable endpoints.",
		"url":          s.canonical(""),
		"version":      s.GeneratedAt.UTC().Format("2006-01-02"),
		"capabilities": map[string]any{"tools": skills},
		"skills":       skills,
	}
}

func (s Site) llms(views []entryView) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", s.Domain)
	b.WriteString("A directory of tools an AI agent can actually connect to. Each entry names the\n")
	b.WriteString("endpoint an agent should call, plus the agent card and MCP endpoint when the\n")
	b.WriteString("service publishes them.\n\n")
	fmt.Fprintf(&b, "For machine-readable data fetch %s/agents.json; that single file\n", s.canonical(""))
	b.WriteString("carries every entry and its endpoints. This file is for a reader skimming prose.\n\n")
	b.WriteString("## Entries\n\n")
	for _, v := range views {
		fmt.Fprintf(&b, "### %s\n\n", v.Entry.Name)
		fmt.Fprintf(&b, "%s\n\n", v.Entry.Summary)
		fmt.Fprintf(&b, "- URL: %s\n", v.Entry.URL)
		if v.Entry.AgentCardURL != nil {
			fmt.Fprintf(&b, "- Agent card: %s\n", *v.Entry.AgentCardURL)
		}
		if v.Entry.MCPEndpointURL != nil {
			fmt.Fprintf(&b, "- MCP endpoint: %s\n", *v.Entry.MCPEndpointURL)
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

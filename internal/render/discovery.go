package render

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"strings"
	"time"

	"github.com/douglasdemaio/agent-ai-tool/internal/content"
	"github.com/douglasdemaio/agent-ai-tool/internal/live"
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
	Slug         string  `json:"slug"`
	Name         string  `json:"name"`
	Summary      string  `json:"summary"`
	URL          string  `json:"url"`
	AgentCardURL *string `json:"agent_card_url"`
	// MCPEndpointURL is published for one release more under its old meaning
	// narrowed to what the name says: an endpoint that speaks MCP, and nothing
	// else. Consumers that read it for the address of any machine endpoint now
	// also have to read api_url, because the values that never spoke MCP have
	// moved there rather than continue to be advertised as a session an agent
	// could open. It stays in the file so a key lookup does not break, but it
	// is null for every entry whose only endpoint is a plain HTTP API.
	MCPEndpointURL *string `json:"mcp_endpoint_url"`
	// APIURL is a plain HTTP endpoint an agent calls directly: a JSON API with
	// no MCP session behind it.
	APIURL       *string   `json:"api_url,omitempty"`
	Category     string    `json:"category,omitempty"`
	Access       string    `json:"access,omitempty"`
	Source       string    `json:"source"`
	LastVerified time.Time `json:"last_verified"`
	// LastChecked is when an automated check last saw this entry's endpoint
	// answer, and is absent when none ever has. It is deliberately separate
	// from LastVerified: that one is a human's word that the entry still
	// describes the service, this one is a machine's word that the endpoint
	// answered, and a reader deciding whether to call needs the second without
	// being misled into thinking the first was refreshed by it.
	LastChecked    *time.Time         `json:"last_checked,omitempty"`
	HowToCall      *content.HowToCall `json:"how_to_call,omitempty"`
	Page           string             `json:"page"`
	Delivered      *int               `json:"delivered,omitempty"`
	EndpointDown   bool               `json:"endpoint_unreachable,omitempty"`
	EndpointReason string             `json:"endpoint_unreachable_reason,omitempty"`
	// EndpointLastOK is when the withheld endpoint last answered, and is
	// present only while the endpoint is withheld. An agent reading a reason
	// needs the age of the evidence behind it: a service down for a minute and
	// a service down for a week are different decisions.
	EndpointLastOK *time.Time `json:"endpoint_last_ok,omitempty"`
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
			Category:     v.Entry.Category,
			Access:       v.Entry.Access,
			Source:       v.Entry.Source,
			LastVerified: v.Entry.LastVerified,
			LastChecked:  v.LastChecked,
			Page:         s.canonical(v.Entry.Slug),
			HowToCall:    v.Entry.HowToCall,
		}
		// The endpoints as rendered, not as filed. An agent reading this file
		// must not be handed a URL the site itself has just failed to reach;
		// a withheld one is reported by the flags below instead.
		if !v.Unreachable {
			entry.MCPEndpointURL = v.MCPEndpoint
			entry.APIURL = v.APIEndpoint
		}
		if v.Unreachable {
			entry.EndpointDown = true
			entry.EndpointReason = v.EndpointDetail
			entry.EndpointLastOK = v.LastAlive
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
	out := map[string]any{
		"domain":      s.Domain,
		"generatedAt": s.GeneratedAt.UTC().Format(time.RFC3339),
		"description": "Every entry on this directory, with the endpoints an agent needs to connect to each one.",
		"agents":      s.jsonEntries(views),
	}
	// The machine counterpart of the block every page carries, and one object
	// rather than one per entry: the verdicts are about the marketplace, not about
	// any single listing, and repeating them N times would invite a reader to
	// believe they had been checked separately.
	if v := s.verificationBlock(); v.Known {
		out["verification"] = v.json()
	}
	return out
}

// jsonVerification is the machine form of a verdict. The states are the same
// words the page uses, so an agent that reads both is not translating between two
// vocabularies and guessing which one is authoritative.
type jsonVerification struct {
	MarketplaceKey string              `json:"marketplace_key"`
	CanonicalForm  string              `json:"canonical_form"`
	Cluster        string              `json:"cluster,omitempty"`
	Sandbox        bool                `json:"sandbox,omitempty"`
	FetchedAt      string              `json:"fetched_at,omitempty"`
	FromCache      bool                `json:"from_cache,omitempty"`
	Summary        string              `json:"summary"`
	Agents         []jsonVerifiedAgent `json:"agents"`
}

type jsonVerifiedAgent struct {
	AgentID string `json:"agent_id"`
	Name    string `json:"name"`
	URL     string `json:"url,omitempty"`
	// MarketplaceSignature and AgentSignature are states, not booleans: an agent
	// choosing whether to trade needs to tell unsigned from invalid.
	MarketplaceSignature string `json:"marketplace_signature"`
	AgentSignature       string `json:"agent_signature"`
	CapabilityList       string `json:"capability_list"`
	MarketplaceReason    string `json:"marketplace_reason,omitempty"`
	AgentReason          string `json:"agent_reason,omitempty"`
	ProbeReason          string `json:"capability_list_reason,omitempty"`
}

func (v verification) json() jsonVerification {
	out := jsonVerification{
		MarketplaceKey: v.Key,
		CanonicalForm:  live.CanonicalForm,
		Cluster:        v.Cluster,
		Sandbox:        v.Sandbox,
		FromCache:      v.FromCache,
		Summary:        v.Summary(),
		Agents:         make([]jsonVerifiedAgent, 0, len(v.Agents)),
	}
	if !v.FetchedAt.IsZero() {
		out.FetchedAt = v.FetchedAt.UTC().Format(time.RFC3339)
	}
	for _, a := range v.Agents {
		out.Agents = append(out.Agents, jsonVerifiedAgent{
			AgentID:              a.AgentID,
			Name:                 a.CardName,
			URL:                  a.CardURL,
			MarketplaceSignature: a.Marketplace.State(),
			AgentSignature:       a.Agent.State(),
			CapabilityList:       a.Probe.State(),
			MarketplaceReason:    a.Marketplace.Detail(),
			AgentReason:          a.Agent.Detail(),
			ProbeReason:          a.Probe.Detail(),
		})
	}
	return out
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
		if v.Unreachable {
			// A withheld endpoint is recorded as withheld rather than pointed
			// at the home page, so an agent never mistakes a browsing URL for
			// something it can call.
			skill["endpointUnavailable"] = true
		} else if v.MCPEndpoint != nil {
			skill["endpoint"] = *v.MCPEndpoint
		} else if v.APIEndpoint != nil {
			skill["endpoint"] = *v.APIEndpoint
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
	b.WriteString("endpoint an agent should call, plus the agent card and the MCP endpoint or\n")
	b.WriteString("plain API endpoint when the service publishes them.\n\n")
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
	stepNum := 1
	for _, v := range views {
		i := stepNum
		fmt.Fprintf(&b, "### %s\n\n", v.Entry.Name)
		fmt.Fprintf(&b, "%s\n\n", v.Entry.Summary)
		fmt.Fprintf(&b, "- URL: %s\n", v.Entry.URL)
		if v.Entry.AgentCardURL != nil {
			fmt.Fprintf(&b, "- Agent card: %s\n", *v.Entry.AgentCardURL)
		}
		endpoints := []struct {
			label string
			url   *string
		}{
			{"MCP endpoint", v.MCPEndpoint},
			{"API endpoint", v.APIEndpoint},
		}
		for _, ep := range endpoints {
			if ep.url == nil {
				continue
			}
			if v.Unreachable {
				fmt.Fprintf(&b, "- %s: withheld, it did not answer a recent health check\n", ep.label)
			} else {
				fmt.Fprintf(&b, "- %s: %s\n", ep.label, *ep.url)
			}
		}
		if v.Unreachable && (v.MCPEndpoint != nil || v.APIEndpoint != nil) {
			if v.EndpointDetail != "" {
				fmt.Fprintf(&b, "  (%s)\n", v.EndpointDetail)
			}
			if v.LastAlive != nil {
				fmt.Fprintf(&b, "  (last answered %s)\n", v.LastAlive.UTC().Format(stampFormat))
			} else {
				b.WriteString("  (it has not answered a check on record)\n")
			}
		}
		if v.Entry.Category != "" {
			fmt.Fprintf(&b, "- Category: %s\n", v.Entry.Category)
		}
		fmt.Fprintf(&b, "- Last verified: %s\n", v.Entry.LastVerified.UTC().Format("2006-01-02"))
		if v.LastChecked != nil {
			fmt.Fprintf(&b, "- Endpoint checked: %s\n", v.LastChecked.UTC().Format("2006-01-02"))
		} else {
			b.WriteString("- Endpoint checked: no successful check on record\n")
		}
		if v.Usage != nil {
			fmt.Fprintf(&b, "- Deliveries recorded: %d\n", v.Usage.Delivered)
		}
		if h := v.Entry.HowToCall; h != nil {
			if h.Note != "" {
				fmt.Fprintf(&b, "\n%s\n", h.Note)
			}
			// The request shapes go in llms.txt as prose and a code block
			// because that file exists to be read by a model, and a model
			// cannot follow a pointer to another repository's source.
			if a := h.Auth; a != nil {
				fmt.Fprintf(&b, "\nAuth: %s.", a.Type)
				if a.Credential != "" {
					fmt.Fprintf(&b, " %s.", a.Credential)
				}
				if a.KeyEncoding != "" {
					fmt.Fprintf(&b, " Public key is %s-encoded, signature is %s-encoded.", a.KeyEncoding, a.SignatureEncoding)
				}
				for _, step := range a.Steps {
					fmt.Fprintf(&b, "\n  %d. %s %s", i, step.Method, step.Path)
					i++
					if body, err := stepBody(step.Body); err == nil {
						fmt.Fprintf(&b, " body: %s", body)
					}
					if step.Returns != "" {
						fmt.Fprintf(&b, "\n     %s", step.Returns)
					}
				}
				if a.SignedMessage != "" {
					fmt.Fprintf(&b, "\n  Sign exactly: %s\n", a.SignedMessage)
				}
			}
			for _, c := range h.Calls {
				auth := "no token needed"
				if c.Auth != "" {
					auth = "requires a token"
				}
				fmt.Fprintf(&b, "\n- %s — %s %s (%s)", c.Name, c.Method, c.Path, auth)
				if body, err := stepBody(c.Body); err == nil {
					fmt.Fprintf(&b, "\n  body: %s", body)
				}
				if c.Returns != "" {
					fmt.Fprintf(&b, "\n  %s", c.Returns)
				}
			}
		}
		fmt.Fprintf(&b, "\n- Directory page: %s\n\n", s.canonical(v.Entry.Slug))
		stepNum = i
	}
	if s.Metrics != nil {
		switch {
		case s.Metrics.AllProbes():
			b.WriteString("## Test activity on vtessera\n\n")
			b.WriteString("Every figure below comes from this repository's own probe agents, so it\n")
			b.WriteString("measures our tests rather than outside use of the marketplace. No agent\n")
			b.WriteString("that is not ours has traded yet.\n\n")
		case s.Metrics.AnyProbes():
			b.WriteString("## Marketplace usage\n\n")
			b.WriteString("These totals include this repository's own probe agents; vtessera does\n")
			b.WriteString("not publish a split between them and anyone else.\n\n")
		default:
			b.WriteString("## Marketplace usage\n\n")
		}
		fmt.Fprintf(&b, "Recorded across the vtessera marketplace: %d delivered, %d disputed,\n", s.Metrics.Totals.Delivered, s.Metrics.Totals.Disputed)
		fmt.Fprintf(&b, "%d cancelled, across %d distinct consumers and %d distinct services in use.\n", s.Metrics.Totals.Cancelled, s.Metrics.Totals.Consumers, s.Metrics.Totals.Services)
	}
	return b.String()
}

// stepBody renders a published request body as a single line, so llms.txt stays
// readable. A pretty-printed body would be a wall of braces in a file whose
// whole purpose is to be scanned.
func stepBody(body map[string]any) (string, error) {
	if len(body) == 0 {
		return "", nil
	}
	// HTML escaping is off because the placeholders are angle-bracketed. Left
	// on, "<agentId>" would reach the reader as \u003cagentId\u003e, which is
	// valid JSON and unreadable in the one file written to be read.
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		return "", err
	}
	return strings.TrimSpace(sb.String()), nil
}

package content

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type Entry struct {
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
}

const (
	SourceCurated = "curated"
	SourceLive    = "live"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func (e Entry) Validate() error {
	if !slugPattern.MatchString(e.Slug) {
		return fmt.Errorf("slug %q must be lowercase alphanumeric words separated by single hyphens", e.Slug)
	}
	if strings.TrimSpace(e.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(e.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if err := absoluteHTTP(e.URL); err != nil {
		return fmt.Errorf("url: %w", err)
	}
	if e.AgentCardURL != nil {
		if err := absoluteHTTP(*e.AgentCardURL); err != nil {
			return fmt.Errorf("agent_card_url: %w", err)
		}
	}
	if e.MCPEndpointURL != nil {
		if err := absoluteHTTP(*e.MCPEndpointURL); err != nil {
			return fmt.Errorf("mcp_endpoint_url: %w", err)
		}
	}
	if e.Source != SourceCurated && e.Source != SourceLive {
		return fmt.Errorf("source %q must be %q or %q", e.Source, SourceCurated, SourceLive)
	}
	if e.LastVerified.IsZero() {
		return fmt.Errorf("last_verified is required")
	}
	return nil
}

func absoluteHTTP(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("must not be empty; use null to omit")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%q is not a URL: %w", raw, err)
	}
	if !parsed.IsAbs() {
		return fmt.Errorf("%q must be absolute", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%q must be http or https, got %q", raw, parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%q has no host", raw)
	}
	return nil
}

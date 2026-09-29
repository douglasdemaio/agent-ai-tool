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

// ReviewWindow is how long a curated entry is trusted before a human is asked to
// confirm it still describes reality.
//
// The health check covers one half of drift: whether an endpoint answers. It
// cannot see the other half — whether the summary, pricing, or access terms
// still match the service. This window is for that.
//
// 180 days is deliberately generous. The cost of the reminder is a moment of
// attention; the cost of a short window is that genuine work drowns under
// entries that are perfectly fine, and a reminder nobody reads is worse than no
// reminder, because it looks like coverage.
const ReviewWindow = 180 * 24 * time.Hour

// ReviewDue reports whether a curated entry has outlived ReviewWindow without a
// human confirming it.
//
// Live entries are exempt. Their last_verified comes from the feed, so a stale
// date there means the refresh job is failing, which is a different problem with
// a different remedy; flagging the entry would point the maintainer at the
// wrong file.
func (e Entry) ReviewDue(now time.Time) bool {
	if e.Source != SourceCurated {
		return false
	}
	// Negative ages (a date in the future) are not due. A future date is a data
	// error, and treating it as infinitely fresh is the safer direction: the
	// alternative flags a working entry and teaches the reader to ignore flags.
	return now.Sub(e.LastVerified) > ReviewWindow
}

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

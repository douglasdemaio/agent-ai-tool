package content

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Entry struct {
	Slug         string  `json:"slug"`
	Name         string  `json:"name"`
	Summary      string  `json:"summary"`
	URL          string  `json:"url"`
	AgentCardURL *string `json:"agent_card_url"`
	// MCPEndpointURL is an endpoint that speaks MCP: a server an agent opens a
	// session against. It is now a narrower field than it started out — it used
	// to hold any URL the service wanted an agent to call, which put a plain
	// JSON document and a protocol endpoint under one label. An agent that
	// trusts the label and posts an MCP initialize to models.dev/api.json gets
	// an HTTP 405, and a directory that hands out that label has mis-described
	// every consumer that believes it. See APIURL for the other half.
	//
	// Deprecated: the field is being narrowed rather than removed, and it stays
	// published for one release so consumers reading it do not break on a
	// missing key. Values that do not speak MCP have moved to APIURL.
	MCPEndpointURL *string `json:"mcp_endpoint_url"`
	// APIURL is a plain HTTP endpoint an agent calls directly — a JSON API to
	// fetch or POST to, with no MCP session and no server to run. It answers
	// the same question MCPEndpointURL does ("which address do I call?") for
	// services that do not speak MCP, which is most of them.
	APIURL       *string    `json:"api_url,omitempty"`
	Category     string     `json:"category,omitempty"`
	Access       string     `json:"access,omitempty"`
	Source       string     `json:"source"`
	LastVerified time.Time  `json:"last_verified"`
	HowToCall    *HowToCall `json:"how_to_call,omitempty"`
}

// HowToCall is what a reader needs in order to actually make a call, rather
// than merely learn that an endpoint exists.
//
// A directory that publishes a URL but not its request shape has moved the
// problem rather than solved it: the reader now has to find the schema by
// reading someone else's source. The gap is not academic — it is why a service
// can be live, healthy, correctly described, and still unusable by the agents
// the directory exists to serve.
type HowToCall struct {
	// Note states the prerequisite in one sentence, e.g. that a caller needs a
	// keypair before any write.
	Note string `json:"note,omitempty"`
	// Auth is the credential dance, when there is one. A service that returns
	// 401 on every write with no reachable path to a token is unusable.
	Auth *AuthFlow `json:"auth,omitempty"`
	// Calls are the operations worth publishing, most useful first.
	Calls []Call `json:"calls"`
}

// AuthFlow describes how to obtain a credential. It is published rather than
// linked because a spec file in another repository is not reachable by an agent
// that only reads this one.
type AuthFlow struct {
	Type              string     `json:"type"`
	Credential        string     `json:"credential,omitempty"`
	KeyEncoding       string     `json:"key_encoding,omitempty"`
	SignatureEncoding string     `json:"signature_encoding,omitempty"`
	SignedMessage     string     `json:"signed_message,omitempty"`
	Steps             []AuthStep `json:"steps"`
}

// AuthStep is one request in the credential dance.
type AuthStep struct {
	Name    string         `json:"name"`
	Method  string         `json:"method"`
	Path    string         `json:"path"`
	Body    map[string]any `json:"body,omitempty"`
	Returns string         `json:"returns,omitempty"`
}

// Call is a single published operation.
type Call struct {
	Name        string         `json:"name"`
	Method      string         `json:"method"`
	Path        string         `json:"path"`
	ContentType string         `json:"content_type,omitempty"`
	Auth        string         `json:"auth,omitempty"`
	Body        map[string]any `json:"body,omitempty"`
	Returns     string         `json:"returns,omitempty"`
}

// RequiresToken reports whether a call needs a credential before it works.
//
// The data states this in words, and "none" is a statement rather than an
// absence: an entry whose call takes no token declares exactly that, so a
// renderer testing Auth for non-emptiness reads the declaration as a
// credential and tells every reader a token is required. Only a named
// credential counts. Getting this wrong is not cosmetic — agents skip calls
// they believe they cannot make.
func (c Call) RequiresToken() bool {
	return c.Auth != "" && !strings.EqualFold(c.Auth, "none")
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
	if e.APIURL != nil {
		if err := absoluteHTTP(*e.APIURL); err != nil {
			return fmt.Errorf("api_url: %w", err)
		}
	}
	if e.Source != SourceCurated && e.Source != SourceLive {
		return fmt.Errorf("source %q must be %q or %q", e.Source, SourceCurated, SourceLive)
	}
	if e.LastVerified.IsZero() {
		return fmt.Errorf("last_verified is required")
	}
	if e.HowToCall != nil {
		if err := e.HowToCall.validate(); err != nil {
			return fmt.Errorf("how_to_call: %w", err)
		}
	}
	return nil
}

// A published call is a promise that this exact request works. Validating it at
// load time is what keeps that promise honest: a call with no path, or a
// relative one, or a GET with a body, would send a reader down a dead end
// dressed as an instruction.
func (h *HowToCall) validate() error {
	if len(h.Calls) == 0 {
		return fmt.Errorf("at least one call is required")
	}
	if h.Auth != nil {
		if strings.TrimSpace(h.Auth.Type) == "" {
			return fmt.Errorf("auth.type is required")
		}
		if len(h.Auth.Steps) == 0 {
			return fmt.Errorf("auth.steps must not be empty")
		}
		for i, s := range h.Auth.Steps {
			if err := validateRequest("auth.steps["+strconv.Itoa(i)+"]", s.Method, s.Path); err != nil {
				return err
			}
		}
	}
	seen := make(map[string]bool, len(h.Calls))
	for i, c := range h.Calls {
		label := "calls[" + strconv.Itoa(i) + "]"
		if strings.TrimSpace(c.Name) == "" {
			return fmt.Errorf("%s.name is required", label)
		}
		if err := validateRequest(label, c.Method, c.Path); err != nil {
			return err
		}
		if c.Method == "GET" && c.Body != nil {
			return fmt.Errorf("%s: a GET carries no request body", label)
		}
		if seen[c.Method+" "+c.Path] {
			return fmt.Errorf("%s: %s %s is published twice", label, c.Method, c.Path)
		}
		seen[c.Method+" "+c.Path] = true
	}
	return nil
}

// validateRequest requires a real method and a service-relative path. Paths are
// relative on purpose: the entry already publishes an absolute base URL, and
// repeating the host in every call is a second place for the two to disagree.
func validateRequest(label, method, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%s.path is required", label)
	}
	// Checked before the prefix rule, so an absolute URL gets the error that
	// names the actual mistake rather than complaining about a missing slash.
	if strings.Contains(path, "://") {
		return fmt.Errorf("%s.path %q must be relative, not absolute; the entry's url is the base", label, path)
	}
	if !strings.HasPrefix(path, "/") {
		return fmt.Errorf("%s.path %q must start with / so it is relative to the entry's url", label, path)
	}
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE":
		return nil
	default:
		return fmt.Errorf("%s.method %q must be a standard HTTP method", label, method)
	}
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

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
	// Specs is the operator-declared technical surface of the service: how it
	// settles, what its limits are, and where its documentation lives. It is
	// kept apart from Summary because the two answer different questions — a
	// summary is the sentence a reader or search engine skims, while specs is
	// the detail an integrator reads next. Nothing in it is claimed about the
	// run state of the service; that comes from the live feed (status,
	// version, keys, cluster, genesis hash), so the two never have to agree.
	Specs *Specs `json:"specs,omitempty"`
}

// Specs is what an operator declares about how a service works. Only
// operator-declared, stable facts belong here: anything a live feed reports
// (an address changing, a version bump, a new key) is drawn from the feed at
// render time instead, because a static claim about live state rots.
type Specs struct {
	// SettlementModes are the ways a trade can settle, and are published as
	// the domain vocabulary rather than invented words for the page.
	SettlementModes []string `json:"settlement_modes,omitempty"`
	// NonCustodial declares that the service never holds a party's funds — a
	// property of the settlement design, not an observation about a ledger.
	NonCustodial bool `json:"non_custodial,omitempty"`
	// Onchain names what follows an on-chain settlement. The cluster and
	// genesis hash are deliberately not here: they are live facts reported by
	// the feed, not a stable design detail, and transcribing them into a file
	// would be a second copy that could drift from the first.
	Onchain *Onchain `json:"onchain,omitempty"`
	// Limits are the operator-declared spending caps and rate limits, so an
	// agent can decide whether a service is worth calling before it calls.
	Limits *Limits `json:"limits,omitempty"`
	// Documentation carries the where-to-read links: quickstart guides, fixed
	// test vectors. Each value is a URL this build does not verify, so the
	// page presents them as pointers rather than as facts about their content.
	Documentation *Documentation `json:"documentation,omitempty"`
}

// Onchain is the part of a settlement design that belongs in a declarative
// file: a fixed fee payable to the buyer's own signing of the transaction.
type Onchain struct {
	FeeLamports int64 `json:"fee_lamports,omitempty"`
}

// Limits are the numbers an agent can budget against before it calls.
type Limits struct {
	SpendCapUSDPerTrade int        `json:"spend_cap_usd_per_trade,omitempty"`
	SpendCapUSDPerDay   int        `json:"spend_cap_usd_per_day,omitempty"`
	RateLimit           *RateLimit `json:"rate_limit,omitempty"`
}

type RateLimit struct {
	Agent *RateLimitBucket `json:"agent,omitempty"`
	IP    *RateLimitBucket `json:"ip,omitempty"`
}

type RateLimitBucket struct {
	RPS   int `json:"rps,omitempty"`
	Burst int `json:"burst,omitempty"`
}

type Documentation struct {
	QuickstartPython     string `json:"quickstart_python,omitempty"`
	QuickstartTypeScript string `json:"quickstart_typescript,omitempty"`
	TestVectorHandshake  string `json:"test_vector_handshake,omitempty"`
	SigningKeyRotation   string `json:"signing_key_rotation,omitempty"`
	ThreatModel          string `json:"threat_model,omitempty"`
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
	Type              string `json:"type"`
	Credential        string `json:"credential,omitempty"`
	KeyEncoding       string `json:"key_encoding,omitempty"`
	SignatureEncoding string `json:"signature_encoding,omitempty"`
	SignedMessage     string `json:"signed_message,omitempty"`
	// TestVector links a fixed handshake sample — published key, exact bytes,
	// expected signature — that a reader can re-sign and compare without
	// contacting the service. Optional: most services have no such sample, and
	// one that does makes the signing code checkable in a minute.
	TestVector string     `json:"test_vector,omitempty"`
	Steps      []AuthStep `json:"steps"`
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

// MachineEndpoint is the URL an agent would actually call: an MCP endpoint
// when the entry publishes one, otherwise a plain API. Nil when it publishes
// neither, because a homepage is a destination for a human, not a call an
// agent can make.
//
// One answer to one question, so every surface that has to choose — the
// directory card, the response time in agents.json — chooses the same way
// instead of each picking its own favourite.
func (e Entry) MachineEndpoint() *string {
	if e.MCPEndpointURL != nil {
		return e.MCPEndpointURL
	}
	return e.APIURL
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
	if e.Specs != nil {
		if err := e.Specs.validate(); err != nil {
			return fmt.Errorf("specs: %w", err)
		}
	}
	return nil
}

// validate keeps a published spec honest the same way how_to_call does: a
// declared number is a promise, so a zero dressed as a value, a settlement mode
// that is not one, or a documentation link that is not a URL must fail the
// build rather than reach a reader.
func (s *Specs) validate() error {
	allowed := map[string]bool{"offchain": true, "onchain": true}
	for i, mode := range s.SettlementModes {
		if !allowed[mode] {
			return fmt.Errorf("settlement_modes[%d] %q must be %q or %q", i, mode, "offchain", "onchain")
		}
	}
	if s.Onchain != nil && s.Onchain.FeeLamports < 0 {
		return fmt.Errorf("onchain.fee_lamports must not be negative")
	}
	if s.Limits != nil {
		if s.Limits.SpendCapUSDPerTrade < 0 {
			return fmt.Errorf("limits.spend_cap_usd_per_trade must not be negative")
		}
		if s.Limits.SpendCapUSDPerDay < 0 {
			return fmt.Errorf("limits.spend_cap_usd_per_day must not be negative")
		}
		if s.Limits.RateLimit != nil {
			for _, slot := range []struct {
				name string
				b    *RateLimitBucket
			}{{"agent", s.Limits.RateLimit.Agent}, {"ip", s.Limits.RateLimit.IP}} {
				if slot.b == nil {
					continue
				}
				if slot.b.RPS < 0 {
					return fmt.Errorf("limits.rate_limit.%s.rps must not be negative", slot.name)
				}
				if slot.b.Burst < 0 {
					return fmt.Errorf("limits.rate_limit.%s.burst must not be negative", slot.name)
				}
			}
		}
	}
	if s.Documentation != nil {
		for _, link := range []struct {
			name string
			url  string
		}{
			{"documentation.quickstart_python", s.Documentation.QuickstartPython},
			{"documentation.quickstart_typescript", s.Documentation.QuickstartTypeScript},
			{"documentation.test_vector_handshake", s.Documentation.TestVectorHandshake},
			{"documentation.signing_key_rotation", s.Documentation.SigningKeyRotation},
			{"documentation.threat_model", s.Documentation.ThreatModel},
		} {
			if link.url == "" {
				continue
			}
			if err := absoluteHTTP(link.url); err != nil {
				return fmt.Errorf("%s: %w", link.name, err)
			}
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
		if h.Auth.TestVector != "" {
			if err := absoluteHTTP(h.Auth.TestVector); err != nil {
				return fmt.Errorf("auth.test_vector: %w", err)
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

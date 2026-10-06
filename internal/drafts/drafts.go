// Package drafts holds the listing criteria and the agent registrations drafted
// against them.
//
// A criterion is only useful here if a program can fail it. Every rule in
// docs/listing-criteria.md that this package enforces is enforced because the
// marketplace will refuse the registration otherwise, and the refusal is the
// specification. Rules the marketplace cannot check are marked SelfAsserted in
// the docs and are deliberately not enforced here: enforcing them by guessing
// would give a draft a green tick nobody earned.
package drafts

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/douglasdemaio/agent-ai-tool/internal/base58"
)

// Card is the subset of the marketplace's agent card a draft may fill in from
// evidence the directory gathered. PublicKey, Currencies, SettlementModes and
// ProbeTarget are absent on purpose: an A2A card publishes no Ed25519 identity,
// and what an agent will accept for money is its owner's commercial decision.
type Card struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Version      string   `json:"version,omitempty"`
	URL          string   `json:"url"`
	Capabilities []string `json:"capabilities,omitempty"`
	Skills       []Skill  `json:"skills,omitempty"`
}

// Skill is one declared capability, in the shape the marketplace validates.
type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Tags        []string `json:"tags,omitempty"`
	InputModes  []string `json:"inputModes,omitempty"`
	OutputModes []string `json:"outputModes,omitempty"`
}

// Evidence is what the directory checked itself, with the time it checked it.
// A rule that cannot be re-run later is a claim, and the point of this package
// is that these are not claims.
type Evidence struct {
	CardURL      string `json:"cardUrl"`
	CardStatus   int    `json:"cardStatus"`
	CardSHA256   string `json:"cardSha256"`
	ServiceURL   string `json:"serviceUrl"`
	ServiceSt    int    `json:"serviceStatus"`
	ServiceMime  string `json:"serviceContentType,omitempty"`
	ContentIsJS  bool   `json:"cardServedAsHTML,omitempty"`
	Organization string `json:"organization,omitempty"`
}

// Outstanding names what the directory cannot supply and why. A draft with an
// empty list is ready to send; a draft with entries is honest about not being.
type Outstanding struct {
	Field string `json:"field"`
	Why   string `json:"why"`
	Must  string `json:"must"`
}

// Draft is one candidate registration.
type Draft struct {
	Slug        string        `json:"slug"`
	Host        string        `json:"host"`
	VerifiedAt  string        `json:"verifiedAt"`
	Status      string        `json:"status"`
	Card        Card          `json:"card"`
	Evidence    Evidence      `json:"evidence"`
	Outstanding []Outstanding `json:"outstanding"`
	Notes       string        `json:"notes,omitempty"`
}

// Result is one criterion applied to one draft.
type Result struct {
	Rule   string `json:"rule"`
	Reason string `json:"reason,omitempty"`
	OK     bool   `json:"ok"`
	// OwnerSupplied marks a rule the directory cannot settle either way. It is
	// not a pass. Counting it as one would be how a directory ends up vouching
	// for something it never checked.
	OwnerSupplied bool `json:"ownerSupplied,omitempty"`
}

// MaxName and MaxDescription mirror the marketplace's card limits, so a draft
// that passes here is not refused for length on submit.
const (
	MaxName         = 200
	MaxDescription  = 2000
	MaxCapabilities = 64
)

// governed is the mint table the marketplace will accept on mainnet-beta,
// pinned from the table vtessera verified against a live cluster.
var governed = map[string]string{
	"EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v": "USDC",
	"HzwqbKZw8HxMN6bF2yFZNrht3c2iXXzpKcFu7uBEDKtr": "EURC",
}

// Check applies every rule the directory can settle, and marks the ones it
// cannot rather than passing them.
func (d Draft) Check() []Result {
	var out []Result
	add := func(rule string, ok bool, reason string) {
		out = append(out, Result{Rule: rule, OK: ok, Reason: reason})
	}
	add("the card was fetched from a well-known path and returned 200",
		d.Evidence.CardStatus == 200 && d.Evidence.CardURL != "",
		fmt.Sprintf("%d from %s", d.Evidence.CardStatus, d.Evidence.CardURL))
	add("the declared service URL answers",
		d.Evidence.ServiceSt == 200,
		fmt.Sprintf("%d from %s", d.Evidence.ServiceSt, d.Evidence.ServiceURL))
	add("the card and the service URL are the same operator's",
		sameOperator(d.Evidence.ServiceURL, d.Evidence.CardURL, d.Host),
		d.Host)
	add("the card names the agent",
		strings.TrimSpace(d.Card.Name) != "",
		d.Card.Name)
	add(fmt.Sprintf("the name is at most %d characters", MaxName),
		len(d.Card.Name) <= MaxName,
		fmt.Sprintf("%d characters", len(d.Card.Name)))
	add(fmt.Sprintf("the description is at most %d characters", MaxDescription),
		len(d.Card.Description) <= MaxDescription,
		fmt.Sprintf("%d characters", len(d.Card.Description)))
	add("the service URL is an absolute https URL",
		strings.HasPrefix(d.Card.URL, "https://"),
		d.Card.URL)
	add("the URL the draft would register is the URL that was checked",
		d.Card.URL == d.Evidence.ServiceURL,
		fmt.Sprintf("card says %s, evidence recorded %s", d.Card.URL, d.Evidence.ServiceURL))
	add(fmt.Sprintf("no more than %d capabilities are declared", MaxCapabilities),
		len(d.Card.Capabilities) <= MaxCapabilities,
		fmt.Sprintf("%d declared", len(d.Card.Capabilities)))

	skills := true
	var skillReason string
	for i, s := range d.Card.Skills {
		if strings.TrimSpace(s.ID) == "" || strings.TrimSpace(s.Name) == "" {
			skills = false
			skillReason = fmt.Sprintf("skill %d has no id or no name", i)
			break
		}
	}
	add("every declared skill has an id and a name", skills, skillReason)

	add("the agent declares at least one skill", len(d.Card.Skills) > 0,
		fmt.Sprintf("%d declared", len(d.Card.Skills)))

	// An A2A card publishes no signing key, so this cannot be checked by us and
	// is the reason most drafts are not ready to send.
	out = append(out, Result{
		Rule:          "the card carries an Ed25519 publicKey the marketplace will accept",
		OwnerSupplied: true,
		Reason:        "an A2A card carries no signing key; the owner generates one and the session must own it",
	})

	// Prices and settlement are the owner's to declare. The marketplace refuses a
	// governed mint with no declared rate, but the rate is not ours to assert.
	out = append(out, Result{
		Rule:          "currencies are governed mints with a declared rate",
		OwnerSupplied: true,
		Reason: "what an agent will accept for money is the owner's decision; the governed table is " +
			"USDC and EURC on mainnet-beta",
	})
	out = append(out, Result{
		Rule:          "settlement modes are declared",
		OwnerSupplied: true,
		Reason:        "offchain and onchain are both accepted; which apply is the owner's decision",
	})
	out = append(out, Result{
		Rule:          "the agent names a probe target for its capability list",
		OwnerSupplied: true,
		Reason:        "a probe target must be https with an explicit port and path; only the owner knows it answers",
	})
	return out
}

// Ready reports whether a draft could be sent as it stands, and why not if it
// could not. It is the same question a maintainer asks before spending an agent's
// session on it.
func (d Draft) Ready() (bool, string) {
	for _, r := range d.Check() {
		if !r.OK && !r.OwnerSupplied {
			return false, r.Rule + ": " + r.Reason
		}
	}
	if len(d.Outstanding) > 0 {
		return false, fmt.Sprintf("%d field(s) the owner must supply: %s",
			len(d.Outstanding), strings.Join(fields(d.Outstanding), ", "))
	}
	return true, ""
}

func fields(items []Outstanding) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Field)
	}
	return out
}

// Load reads every draft in a directory, in slug order so failures are stable.
func Load(dir string) ([]Draft, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	out := make([]Draft, 0, len(names))
	for _, name := range names {
		raw, err := os.ReadFile(name)
		if err != nil {
			return nil, err
		}
		var d Draft
		if err := json.Unmarshal(raw, &d); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(name), err)
		}
		out = append(out, d)
	}
	return out, nil
}

// DraftDir is where the repository keeps them.
const DraftDir = "drafts"

// ValidateURL is exported for the live re-check, which has to decide whether a
// URL is one the marketplace would store.
func ValidateURL(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("must be an absolute http or https URL, got %q", raw)
	}
	if parsed.Host == "" {
		return fmt.Errorf("must include a host, got %q", raw)
	}
	return nil
}

// ValidateKey is the identity rule, exported for the same reason.
func ValidateKey(key string) error {
	if key == "" {
		return fmt.Errorf("public key is required")
	}
	return base58.DecodeLen(key, 32)
}

// ValidateMint is the currency rule: a governed mint, 32 bytes of base58. A
// mint outside the table is not refused for being a mint; it is refused for
// having no declared rate, which is the same thing to a reader.
func ValidateMint(address string) error {
	if err := base58.DecodeLen(address, 32); err != nil {
		return err
	}
	if _, ok := governed[address]; !ok {
		return fmt.Errorf("%s is not a governed mint on mainnet-beta", address)
	}
	return nil
}

// sameOperator reports whether both URLs belong to the recorded host.
//
// A substring test would pass a card hosted at reviewchi.ir whose service URL is
// https://notreviewchi.ir.example.com, which is the entire failure this criterion
// exists to catch. Subdomains count, because an operator serving its card at the
// apex and its service at a subdomain is one operator; a different registrable
// name is not, whatever it ends with.
func sameOperator(serviceURL, cardURL, host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return false
	}
	for _, raw := range []string{serviceURL, cardURL} {
		parsed, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return false
		}
		name := strings.ToLower(parsed.Hostname())
		if name != host && !strings.HasSuffix(name, "."+host) {
			return false
		}
	}
	return true
}

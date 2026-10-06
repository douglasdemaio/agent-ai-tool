package drafts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T) []Draft {
	t.Helper()
	all, err := Load(filepath.Join("..", "..", DraftDir))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) == 0 {
		t.Fatal("no drafts were found")
	}
	return all
}

func TestTwentyDraftsArePresent(t *testing.T) {
	// The number is the deliverable, so it is asserted rather than assumed.
	if got := len(load(t)); got != 20 {
		t.Errorf("found %d drafts, want 20", got)
	}
}

func TestEveryDraftSatisfiesEveryCriterionTheDirectoryCanSettle(t *testing.T) {
	for _, d := range load(t) {
		for _, r := range d.Check() {
			if !r.OK && !r.OwnerSupplied {
				t.Errorf("%s fails %q: %s", d.Slug, r.Rule, r.Reason)
			}
		}
	}
}

func TestNoDraftClaimsToBeReadyToSend(t *testing.T) {
	// Every draft is missing an Ed25519 identity, which no third party can
	// supply. A draft that reported itself ready would be asserting the owner
	// already agreed to something they never saw.
	for _, d := range load(t) {
		ready, why := d.Ready()
		if ready {
			t.Errorf("%s reports ready to send, but it is missing %s", d.Slug, why)
		}
		if !strings.Contains(why, "publicKey") {
			t.Errorf("%s is not ready for the wrong reason: %s", d.Slug, why)
		}
	}
}

func TestADraftCarriesTheEvidenceForItsOwnClaim(t *testing.T) {
	// A draft with no hash of the card it was built from cannot be re-checked,
	// so a later reader cannot tell whether the service drifted.
	for _, d := range load(t) {
		if len(d.Evidence.CardSHA256) != 64 {
			t.Errorf("%s has no hash of the card it was drafted from", d.Slug)
		}
		if d.VerifiedAt == "" {
			t.Errorf("%s has no time its evidence was gathered", d.Slug)
		}
		if d.Evidence.CardStatus != 200 || d.Evidence.ServiceSt != 200 {
			t.Errorf("%s records card %d and service %d",
				d.Slug, d.Evidence.CardStatus, d.Evidence.ServiceSt)
		}
	}
}

func TestADraftSaysTheServiceItCheckedIsTheServiceItWouldRegister(t *testing.T) {
	// The failure this catches is a draft whose card was checked on one host and
	// whose url points somewhere else, which is how a directory ends up vouching
	// for a domain it never called.
	for _, d := range load(t) {
		if d.Card.URL != d.Evidence.ServiceURL {
			t.Errorf("%s would register %s but checked %s",
				d.Slug, d.Card.URL, d.Evidence.ServiceURL)
		}
		if !strings.Contains(d.Card.URL, d.Host) {
			t.Errorf("%s is hosted at %s but would register %s", d.Slug, d.Host, d.Card.URL)
		}
	}
}

func TestTheCriteriaRefuseADraftThatFailsThem(t *testing.T) {
	// A criteria list nothing can fail is documentation, not a gate. Each case
	// below breaks one rule and expects exactly that rule to catch it.
	good := Draft{
		Slug: "ok", Host: "agent.example",
		VerifiedAt: "2026-10-06T00:00:00Z",
		Card: Card{
			Name: "An agent", Description: "Does a thing.",
			URL:    "https://agent.example",
			Skills: []Skill{{ID: "do", Name: "Do a thing"}},
		},
		Evidence: Evidence{
			CardURL: "https://agent.example/.well-known/agent-card.json", CardStatus: 200,
			CardSHA256: strings.Repeat("a", 64),
			ServiceURL: "https://agent.example", ServiceSt: 200,
		},
	}
	if ready, why := good.Ready(); !ready {
		// It is missing owner fields, so not-ready is expected; what matters is
		// that nothing the directory checks is failing.
		if !strings.Contains(why, "owner must supply") {
			t.Fatalf("a sound draft failed a rule: %s", why)
		}
	}

	cases := []struct {
		name   string
		rule   string
		break_ func(*Draft)
	}{
		{"a card that never loaded", "fetched from a well-known path",
			func(d *Draft) { d.Evidence.CardStatus = 404 }},
		{"a service that does not answer", "the declared service URL answers",
			func(d *Draft) { d.Evidence.ServiceSt = 502 }},
		{"a url that is not https", "absolute https URL",
			func(d *Draft) { d.Card.URL = "http://agent.example" }},
		{"a url that was never checked", "the URL the draft would register is the URL that was checked",
			func(d *Draft) { d.Card.URL = "https://elsewhere.example" }},
		{"no name", "the card names the agent",
			func(d *Draft) { d.Card.Name = "  " }},
		{"a name past the limit", "at most 200 characters",
			func(d *Draft) { d.Card.Name = strings.Repeat("x", 201) }},
		{"a description past the limit", "at most 2000 characters",
			func(d *Draft) { d.Card.Description = strings.Repeat("x", 2001) }},
		{"a skill with no id", "every declared skill has an id and a name",
			func(d *Draft) { d.Card.Skills = []Skill{{Name: "Do a thing"}} }},
		{"no skills at all", "the agent declares at least one skill",
			func(d *Draft) { d.Card.Skills = nil }},
		{"too many capabilities", "64 capabilities",
			func(d *Draft) { d.Card.Capabilities = make([]string, 65) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := good
			tc.break_(&d)
			ready, why := d.Ready()
			if ready {
				t.Fatalf("%s was accepted", tc.name)
			}
			if !strings.Contains(why, tc.rule) {
				t.Errorf("refused for the wrong reason:\n got %s\nwant a rule containing %q", why, tc.rule)
			}
		})
	}
}

func TestACardWithNoSigningKeyIsNotACriterionTheDirectoryCanSettle(t *testing.T) {
	// The point of the OwnerSupplied marking: a rule nobody can check must not
	// come back green, or the list becomes a list of things somebody assumed.
	got := Draft{Host: "a.example", Card: Card{Name: "n", URL: "https://a.example"}}
	var owner int
	for _, r := range got.Check() {
		if r.OwnerSupplied {
			owner++
			if r.OK {
				t.Errorf("an owner-supplied rule came back passing: %s", r.Rule)
			}
		}
	}
	if owner == 0 {
		t.Error("no rule was marked owner-supplied")
	}
}

func TestAnIdentityMustBeThirtyTwoBytesOfBase58(t *testing.T) {
	// The marketplace derives the agent id from the session, so a key that is not
	// a real Ed25519 key is refused at the door. Checked against the same table
	// and the same rules the service uses.
	if err := ValidateKey("5LRpM9wpvPfRYuQAC7oNdyaQa6sakpMcnZeR9FS5CgjB"); err != nil {
		t.Errorf("a real key was refused: %v", err)
	}
	for _, bad := range []string{"", "not base58", strings.Repeat("1", 31), strings.Repeat("1", 33)} {
		if err := ValidateKey(bad); err == nil {
			t.Errorf("accepted %q as a key", bad)
		}
	}
}

func TestACurrencyMustBeAGovernedMint(t *testing.T) {
	// Refusing an ungoverned mint is the same refusal as refusing a mint with no
	// declared rate: the marketplace will not price it, so it cannot be a cap.
	if err := ValidateMint("EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"); err != nil {
		t.Errorf("USDC was refused: %v", err)
	}
	if err := ValidateMint("HzwqbKZw8HxMN6bF2yFZNrht3c2iXXzpKcFu7uBEDKtr"); err != nil {
		t.Errorf("EURC was refused: %v", err)
	}
	// 32 bytes of valid base58 that is not on the table. This is the lookalike
	// case the settlement work already got wrong once.
	if err := ValidateMint("HzwqbKZw8HxMN6bF2yFZNrht3c2iXXcyK85CNzz7iwQc"); err == nil {
		t.Error("accepted a lookalike mint address that is not governed")
	}
}

func TestADraftNamesTheFieldsTheOwnerStillHasToSupply(t *testing.T) {
	// An empty outstanding list would mean a draft was ready; every draft must
	// therefore say what is missing, and say the key.
	for _, d := range load(t) {
		if len(d.Outstanding) == 0 {
			t.Fatalf("%s claims nothing is outstanding", d.Slug)
		}
		var sawKey bool
		for _, o := range d.Outstanding {
			if o.Field == "publicKey" {
				sawKey = true
			}
			if strings.TrimSpace(o.Why) == "" || strings.TrimSpace(o.Must) == "" {
				t.Errorf("%s lists %q without saying why or what is needed", d.Slug, o.Field)
			}
		}
		if !sawKey {
			t.Errorf("%s does not list publicKey as outstanding", d.Slug)
		}
	}
}

func TestADraftIsReadableJSONWithTheFieldsAMaintainerWouldLookFor(t *testing.T) {
	// The drafts are read by people, not only by this package, so the on-disk
	// shape is asserted rather than left to the struct tags.
	for _, d := range load(t) {
		raw, err := os.ReadFile(filepath.Join("..", "..", DraftDir, d.Slug+".json"))
		if err != nil {
			t.Fatal(err)
		}
		var generic map[string]any
		if err := json.Unmarshal(raw, &generic); err != nil {
			t.Fatalf("%s is not valid JSON: %v", d.Slug, err)
		}
		for _, key := range []string{"slug", "host", "verifiedAt", "status", "card", "evidence", "outstanding"} {
			if _, ok := generic[key]; !ok {
				t.Errorf("%s has no %q on disk", d.Slug, key)
			}
		}
	}
}

func TestEveryDraftHasItsOwnSlug(t *testing.T) {
	// Two drafts sharing a slug would make the evidence record ambiguous, and one
	// file would silently overwrite the other.
	seen := map[string]string{}
	for _, d := range load(t) {
		if prev, ok := seen[d.Slug]; ok {
			t.Errorf("%s and %s share a slug", prev, d.Slug)
		}
		seen[d.Slug] = d.Host
		if filepath.Base(d.Host) != d.Host || strings.ContainsAny(d.Host, "/ ") {
			t.Errorf("%s has a host that is not a bare hostname: %q", d.Slug, d.Host)
		}
	}
}

func TestAServiceURLThatMerelyContainsTheHostIsNotTheSameOperator(t *testing.T) {
	// The check that a substring would have passed. If this ever comes back true,
	// the criteria cannot tell an operator's own domain from one that ends in it,
	// and a draft could vouch for a host it never called.
	good := Draft{
		Slug: "ok", Host: "reviewchi.ir",
		Card: Card{Name: "n", URL: "https://reviewchi.ir", Skills: []Skill{{ID: "a", Name: "b"}}},
		Evidence: Evidence{
			CardURL: "https://reviewchi.ir/.well-known/agent-card.json", CardStatus: 200,
			ServiceURL: "https://reviewchi.ir", ServiceSt: 200,
		},
	}
	// This draft has no outstanding fields, so it is the shape that would be
	// ready to send: every rule the directory settles passes.
	if ready, why := good.Ready(); !ready {
		t.Fatalf("the sound draft failed a rule: %s", why)
	}

	spoofed := good
	spoofed.Evidence.ServiceURL = "https://notreviewchi.ir.example.com"
	if _, why := spoofed.Ready(); !strings.Contains(why, "same operator") {
		t.Errorf("a service URL ending in the host was accepted: %s", why)
	}

	// A subdomain is the same operator and must still pass.
	sub := good
	sub.Host = "example.com"
	sub.Evidence.CardURL = "https://agent.example.com/.well-known/agent-card.json"
	sub.Evidence.ServiceURL = "https://api.example.com/v1"
	sub.Card.URL = "https://api.example.com/v1"
	if ready, why := sub.Ready(); !ready {
		t.Errorf("a subdomain was refused as a different operator: %s", why)
	}
}

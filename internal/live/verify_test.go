package live

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemaio/agent-ai-tool/internal/base58"
)

// base58Encode is here so a test can name a key it generated. Production only
// ever decodes: the directory publishes no keys of its own, and adding an
// encoder to the shipped binary for the convenience of a test would be the wrong
// way round.
//
// Written independently of internal/base58 rather than by calling its Encode, on
// purpose. A round trip through one implementation proves nothing about the
// decoder that production uses, and the two disagree on nothing that matters
// here — this one is here to produce a string, not to be trusted to read one.
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

func base58Encode(in []byte) string {
	out := []byte{}
	for _, b := range in {
		carry := int(b)
		for i := len(out) - 1; i >= 0; i-- {
			carry += int(out[i]) << 8
			out[i] = byte(carry % 58)
			carry /= 58
		}
		for carry > 0 {
			out = append([]byte{byte(carry % 58)}, out...)
			carry /= 58
		}
	}
	var b strings.Builder
	// Only leading zero bytes get a '1'. A '1' is a zero digit, so writing one
	// for an interior zero would change the key rather than pad it.
	zeros := 0
	for zeros < len(in) && in[zeros] == 0 {
		zeros++
	}
	for range zeros {
		b.WriteByte(base58Alphabet[0])
	}
	for _, c := range out {
		b.WriteByte(base58Alphabet[c])
	}
	return b.String()
}

// newKey returns the private key and the base58 key ID a signature would name.
func mustKeyID(t *testing.T) string {
	t.Helper()
	_, id := newKey(t)
	return id
}

func newKey(t *testing.T) (ed25519.PrivateKey, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return priv, base58Encode(pub)
}

func digestOf(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// sign builds the signature shape the marketplace publishes, from the same key
// and the same payload the production code will later hand to a verifier.
func sign(t *testing.T, priv ed25519.PrivateKey, keyID string, payload []byte, at time.Time) Signature {
	t.Helper()
	return Signature{
		Alg:      Alg,
		KeyID:    keyID,
		Digest:   digestOf(payload),
		Value:    base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload)),
		SignedAt: at,
	}
}

func TestABase58KeyDecodesToTheKeyThatSigned(t *testing.T) {
	// A key whose first byte is zero is the case a naive decoder gets wrong: the
	// leading '1' is a padding marker, not a digit, and treating it as one shifts
	// every subsequent byte.
	for _, first := range []byte{0x00, 0x01, 0x7f, 0xff} {
		raw := make([]byte, ed25519.PublicKeySize)
		raw[0] = first
		for i := 1; i < len(raw); i++ {
			raw[i] = byte(i * 7)
		}
		encoded := base58Encode(raw)
		got, err := base58.Decode(encoded)
		if err != nil {
			t.Fatalf("decoding %s: %v", encoded, err)
		}
		if len(got) != len(raw) {
			t.Fatalf("decoded %d bytes from a %d-byte key (first byte %#x): %x", len(got), len(raw), first, got)
		}
		if string(got) != string(raw) {
			t.Errorf("round trip changed the key: %x then %x", raw, got)
		}
	}
}

func TestABase58DecoderRefusesWhatIsNotBase58(t *testing.T) {
	// The empty string is in this list now rather than tolerated: the shared
	// decoder rejects it, and a key that decodes to nothing is not a key.
	for _, text := range []string{"", "0OIl", "abc def", "5LRpM9wpvPfRYuQAC7oNdyaQa6sakpMcnZeR9FS5Cgj!"} {
		if _, err := base58.Decode(text); err == nil {
			t.Errorf("%q was accepted as base58", text)
		}
	}
}

func TestTheCardEncodingDoesNotDependOnTheOrderTheFeedArrivedIn(t *testing.T) {
	// The feed's JSON order is not the card's meaning. Two cards that declare the
	// same capabilities and skills in a different order are the same claim and
	// must produce the same bytes, or an agent could void its own signature by
	// reordering a list it controls.
	first := AgentCard{
		Name: "summarizer", Description: "Summarises long documents.", Version: "0.1.0",
		URL:       "https://agents.example/summarizer",
		PublicKey: "key", Capabilities: []string{"text", "files", "text"},
		Skills: []AgentSkill{
			{ID: "summarise", Name: "Summarise", Tags: []string{"nlp", "nlp"}, Input: []string{"text", "text"}},
			{ID: "answer", Name: "Answer"},
		},
		Currencies: []string{"USDC", "EURC"}, SettlementModes: []string{"offchain"},
	}
	second := AgentCard{
		Name: "summarizer", Description: "Summarises long documents.", Version: "0.1.0",
		URL:       "https://agents.example/summarizer",
		PublicKey: "key", Capabilities: []string{"files", "text"},
		Skills: []AgentSkill{
			{ID: "answer", Name: "Answer"},
			{ID: "summarise", Name: "Summarise", Tags: []string{"nlp"}, Input: []string{"text"}},
		},
		Currencies: []string{"EURC", "USDC"}, SettlementModes: []string{"offchain"},
	}
	at := time.Date(2026, 10, 6, 1, 31, 49, 591750591, time.UTC)
	if string(cardBytes(first, "agent", at)) != string(cardBytes(second, "agent", at)) {
		t.Error("reordering a card's own lists changed the signed bytes")
	}
}

func TestTheCardEncodingCountsBytesRatherThanCharacters(t *testing.T) {
	// Length prefixing in bytes is what stops a name with a multi-byte rune from
	// being signed one way and checked another. If this ever counted runes, the
	// two would diverge here and nowhere else.
	ascii := AgentCard{Name: "abc"}
	wide := AgentCard{Name: "aüc"}
	if len(string(cardBytes(ascii, "a", time.Unix(0, 0).UTC()))) ==
		len(string(cardBytes(wide, "a", time.Unix(0, 0).UTC()))) {
		t.Error("a two-byte rune produced the same encoding length as one ASCII byte")
	}
}

func TestAVerifiedCardIsOneWhoseBytesMatchBothSignatures(t *testing.T) {
	marketPriv, marketID := newKey(t)
	agentPriv, agentID := newKey(t)
	card := AgentCard{
		Name: "summarizer", PublicKey: agentID,
		Capabilities: []string{"text"},
		Skills:       []AgentSkill{{ID: "summarise", Name: "Summarise"}},
		Currencies:   []string{"USDC"},
	}
	agent := Agent{ID: agentID, Card: card}
	at := time.Date(2026, 10, 6, 1, 31, 49, 591750591, time.UTC)

	var attestation CardAttestation
	attestation.AgentID = agentID
	attestation.CanonicalForm = CanonicalForm
	// Each side signs the same bytes at its own instant, which is what the service
	// does: one record, two witnesses.
	agentSig := sign(t, agentPriv, agentID, cardBytes(card, agentID, at), at)
	marketSig := sign(t, marketPriv, marketID, cardBytes(card, agentID, at.Add(time.Second)), at.Add(time.Second))
	attestation.Agent.Signature, attestation.Agent.Attested = &agentSig, true
	attestation.Marketplace.Signature, attestation.Marketplace.Attested = &marketSig, true

	got := VerifyAgent(agent, attestation, nil, marketID)
	if !got.Marketplace.Valid || !got.Agent.Valid {
		t.Fatalf("a correctly signed card did not verify: marketplace %q, agent %q",
			got.Marketplace.Reason, got.Agent.Reason)
	}
	if got.Marketplace.State() != "verified" || got.Agent.State() != "verified" {
		t.Errorf("states were %q and %q", got.Marketplace.State(), got.Agent.State())
	}
	if got.Probe.State() != "never probed" {
		t.Errorf("an absent probe reported as %q", got.Probe.State())
	}
}

func TestOneChangedByteFailsBothSignatures(t *testing.T) {
	marketPriv, marketID := newKey(t)
	agentPriv, agentID := newKey(t)
	card := AgentCard{Name: "summarizer", PublicKey: agentID, Capabilities: []string{"text"}}
	at := time.Date(2026, 10, 6, 1, 31, 49, 0, time.UTC)
	agentSig := sign(t, agentPriv, agentID, cardBytes(card, agentID, at), at)
	marketSig := sign(t, marketPriv, marketID, cardBytes(card, agentID, at), at)

	var attestation CardAttestation
	attestation.CanonicalForm = CanonicalForm
	attestation.Agent.Signature, attestation.Agent.Attested = &agentSig, true
	attestation.Marketplace.Signature, attestation.Marketplace.Attested = &marketSig, true

	edited := card
	edited.Description = "Summarises long documents."
	got := VerifyAgent(Agent{ID: agentID, Card: edited}, attestation, nil, marketID)
	if got.Marketplace.Valid || got.Agent.Valid {
		t.Fatal("a card edited after signing still verified")
	}
	// An invalid card and an unsigned one have to read differently, or a reader
	// cannot tell a stale mirror from a marketplace that never attested anything.
	if got.Marketplace.State() != "invalid" || got.Agent.State() != "invalid" {
		t.Errorf("states were %q and %q, want invalid", got.Marketplace.State(), got.Agent.State())
	}
	if got.Marketplace.Detail() == "" {
		t.Error("an invalid signature gave no reason")
	}
}

func TestAMarketplaceSignatureIsNotAcceptedFromAnotherKey(t *testing.T) {
	// This is the substitution the design turns on. An agent signs its own claims
	// and the marketplace signs the same bytes; if the directory checked the
	// marketplace's line against whatever key the signature named, an agent's key
	// would vouch for the marketplace's row.
	marketID := mustKeyID(t)
	impostorPriv, impostorID := newKey(t)
	_, agentID := newKey(t)
	card := AgentCard{Name: "summarizer", PublicKey: agentID}
	at := time.Date(2026, 10, 6, 1, 31, 49, 0, time.UTC)
	payload := cardBytes(card, agentID, at)
	forged := sign(t, impostorPriv, impostorID, payload, at)

	var attestation CardAttestation
	attestation.CanonicalForm = CanonicalForm
	attestation.Marketplace.Signature, attestation.Marketplace.Attested = &forged, true
	got := VerifyAgent(Agent{ID: agentID, Card: card}, attestation, nil, marketID)
	if got.Marketplace.Valid {
		t.Error("a signature from an unpublished key was accepted as the marketplace's")
	}
	if !strings.Contains(got.Marketplace.Reason, "expected") {
		t.Errorf("the reason did not name the mismatch: %q", got.Marketplace.Reason)
	}
}

func TestNothingIsCheckedWithoutAPublishedKey(t *testing.T) {
	marketPriv, marketID := newKey(t)
	agentPriv, agentID := newKey(t)
	_ = marketPriv
	card := AgentCard{Name: "summarizer", PublicKey: agentID}
	at := time.Date(2026, 10, 6, 1, 31, 49, 0, time.UTC)
	payload := cardBytes(card, agentID, at)
	marketSig := sign(t, marketPriv, marketID, payload, at)
	agentSig := sign(t, agentPriv, agentID, payload, at)

	var attestation CardAttestation
	attestation.CanonicalForm = CanonicalForm
	attestation.Marketplace.Signature, attestation.Marketplace.Attested = &marketSig, true
	attestation.Agent.Signature, attestation.Agent.Attested = &agentSig, true

	got := VerifyAgent(Agent{ID: agentID, Card: card}, attestation, nil, "")
	if got.Marketplace.Valid {
		t.Error("the marketplace's signature verified with no key to check it against")
	}
	// The agent's own signature is still checkable: the agent ID is the key.
	if !got.Agent.Valid {
		t.Errorf("the agent's own signature was dropped because the marketplace key was missing: %q", got.Agent.Reason)
	}
}

func TestACardSignedInAnUnknownEncodingIsRefusedRatherThanGuessedAt(t *testing.T) {
	marketPriv, marketID := newKey(t)
	_, agentID := newKey(t)
	card := AgentCard{Name: "summarizer", PublicKey: agentID}
	at := time.Date(2026, 10, 6, 1, 31, 49, 0, time.UTC)
	payload := cardBytes(card, agentID, at)
	var attestation CardAttestation
	attestation.CanonicalForm = "vtessera/attest/v2"
	marketSig := sign(t, marketPriv, marketID, payload, at)
	attestation.Marketplace.Signature, attestation.Marketplace.Attested = &marketSig, true

	got := VerifyAgent(Agent{ID: agentID, Card: card}, attestation, nil, marketID)
	if got.Marketplace.Valid || got.Marketplace.State() != "unavailable" {
		t.Errorf("a v2 card was %q rather than refused", got.Marketplace.State())
	}
}

func TestAProbeReportIsCheckedAgainstTheKeyThatClaimsToHaveMadeIt(t *testing.T) {
	priv, keyID := newKey(t)
	at := time.Date(2026, 10, 6, 1, 32, 0, 123456789, time.UTC)
	report := ProbeReport{
		AgentID: "agent", Target: "https://agents.example",
		Passed: true, CheckedAt: at.Add(-time.Minute),
		Results: []ProbeOutcome{
			{Capability: "zeta", Status: "pass"},
			{Capability: "alpha", Status: "fail", Detail: "no route"},
		},
	}
	sig := sign(t, priv, keyID, probeBytes(report, at), at)
	report.Signature, report.AttestedBy = &sig, keyID

	if v := verifyProbe("agent", &report, keyID); !v.Verified {
		t.Fatalf("a correctly signed probe did not verify: %q", v.Reason)
	} else if v.State() != "verified" {
		t.Errorf("a passing probe reported as %q", v.State())
	}

	// A verified report that failed is a real failure, not a reason to withhold the
	// line: the whole point of publishing the report is to see this.
	report.Passed = false
	if v := verifyProbe("agent", &report, keyID); !v.Verified || v.State() != "failed" {
		t.Errorf("a failing probe reported as %q", v.State())
	}
	report.Passed = true

	// Attested-by is part of the claim, so it is checked rather than trusted: a
	// report claiming some other marketplace's key is not this marketplace's.
	report.AttestedBy = base58Encode(make([]byte, ed25519.PublicKeySize))
	if v := verifyProbe("agent", &report, keyID); v.Verified {
		t.Error("a probe claiming another attester verified")
	}
	report.AttestedBy = keyID

	report.Results[0].Status = "fail"
	if v := verifyProbe("agent", &report, keyID); v.Verified {
		t.Error("a probe whose results were edited after signing verified")
	} else if v.ProbeDetail() == "" {
		t.Error("an unverifiable probe gave no reason")
	}
}

// ProbeDetail exists so the test can assert a reason is present without reaching
// into the field the template also reads.
func (p ProbeVerdict) ProbeDetail() string { return p.Detail() }

func TestAnAgentWithNoProbeReportIsNotCountedAsAFailure(t *testing.T) {
	v := VerifyAgent(Agent{ID: "a", Card: AgentCard{Name: "a"}}, CardAttestation{CanonicalForm: CanonicalForm}, nil, "key")
	if v.Probe.State() != "never probed" {
		t.Errorf("an agent nobody probed reported as %q", v.Probe.State())
	}
	if v.Probe.Class() != "never-probed" {
		t.Errorf("the CSS class was %q, which a selector cannot match", v.Probe.Class())
	}
}

func TestTheReportFeedSeparatesNeverProbedFromFailedToFetch(t *testing.T) {
	// The distinction the whole feed exists to preserve: 404 NOT_PROBED means an
	// agent nobody checked, while a broken request means the directory does not
	// know either. Collapsing them publishes an outage as a clean bill of health.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/agents/probed/attestation":
			_, _ = w.Write([]byte(`{"agentId":"probed","recorded":true,"canonicalForm":"vtessera/attest/v1","marketplace":{"attested":false},"agent":{"attested":false}}`))
		case "/v1/agents/probed/capabilities":
			_, _ = w.Write([]byte(`{"agentId":"probed","target":"https://a.example","passed":true,"valid":true,"checkedAt":"2026-10-01T00:00:00Z","results":[]}`))
		case "/v1/agents/unreachable/attestation":
			_, _ = w.Write([]byte(`{"agentId":"unreachable","recorded":true,"canonicalForm":"vtessera/attest/v1","marketplace":{"attested":false},"agent":{"attested":false}}`))
		case "/v1/agents/unreachable/capabilities":
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("upstream is down"))
		case "/v1/agents/untouched/attestation":
			// An agent nobody has heard of is NOT_FOUND on the attestation route,
			// not NOT_PROBED. The two mean opposite things, and only one of them
			// is an answer, so the route that carries a report is not allowed to
			// borrow the other's code.
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NOT_FOUND","error":"no such agent"}`))
		case "/v1/agents/untouched/capabilities":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NOT_PROBED","error":"never probed"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":"NOT_FOUND","error":"no such agent"}`))
		}
	}))
	defer srv.Close()

	agents := []Agent{
		{ID: "probed", Card: AgentCard{Name: "probed"}},
		{ID: "unreachable", Card: AgentCard{Name: "unreachable"}},
		{ID: "untouched", Card: AgentCard{Name: "untouched"}},
	}
	result := LoadAgentReports(context.Background(), srv.URL, agents, "", srv.Client())
	if !result.Available() {
		t.Fatalf("feed unavailable: %v", result.FetchErr)
	}
	var feed AgentReportsResponse
	if err := json.Unmarshal(result.Response, &feed); err != nil {
		t.Fatal(err)
	}
	byAgent := feed.ByAgent()
	if len(byAgent) != 3 {
		t.Fatalf("got %d reports, want 3", len(byAgent))
	}
	if byAgent["probed"].Error != "" {
		t.Errorf("a healthy agent reported an error: %s", byAgent["probed"].Error)
	}
	if byAgent["probed"].Probe == nil {
		t.Error("a signed probe report was dropped")
	}
	// A probe that answers is never checked against a key here, so it is present
	// but not verified: the marketplace's own "valid" flag is not the directory's
	// verdict and must not be rendered as one.
	if v := verifyProbe("probed", byAgent["probed"].Probe, "somekey"); v.Verified {
		t.Error("an unsigned probe report was treated as verified")
	}
	if byAgent["unreachable"].Error == "" {
		t.Error("a 502 from the probe endpoint was swallowed")
	}
	// The agent is in the feed, so the attestation route answering NOT_FOUND is
	// the marketplace disagreeing with its own feed. That is recorded as a fault,
	// not quietly treated as an unsigned card.
	if !strings.Contains(byAgent["untouched"].Error, "NOT_FOUND") {
		t.Errorf("the attestation fault was not recorded: %q", byAgent["untouched"].Error)
	}
	if strings.Contains(byAgent["untouched"].Error, notProbed) {
		t.Error("NOT_PROBED was honoured on the attestation route, where it means nothing")
	}
	if byAgent["untouched"].Probe != nil {
		t.Error("a 404 NOT_PROBED produced a probe report")
	}
}

func TestTheFeedIsNotCachedWhenNoMarketplaceAnswers(t *testing.T) {
	// Writing a snapshot in which every agent failed would commit a file that
	// reads as a broken marketplace, and the next build would render it as fact.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	path := t.TempDir() + "/feed.json"
	if err := RefreshAgentReports(context.Background(), srv.URL,
		[]Agent{{ID: "a", Card: AgentCard{Name: "a"}}}, path, srv.Client()); err == nil {
		t.Error("a snapshot was written from a marketplace that answered nothing")
	}
	if _, err := decodeNoFile(path); err == nil {
		t.Error("the snapshot file exists")
	}
}

func decodeNoFile(path string) (string, error) {
	if _, err := readCache(path); err != nil {
		return "", err
	}
	return "", nil
}

func TestHealthIsRefusedWithoutAVerificationKey(t *testing.T) {
	// Every signature on the site is checked against this field. A /healthz with
	// no key is not a degraded feed, it is a feed with nothing to check against.
	if err := ValidateHealth([]byte(`{"status":"ok"}`)); err == nil {
		t.Error("a health response with no verificationKey was accepted")
	}
	if err := ValidateHealth([]byte(`{"verificationKey":"abc","status":"ok"}`)); err != nil {
		t.Errorf("a health response with a key was refused: %v", err)
	}
}

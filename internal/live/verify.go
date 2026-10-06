package live

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/douglasdemaio/agent-ai-tool/internal/base58"
)

// CanonicalForm is the byte encoding vtessera signs. It is named in every
// signature rather than assumed, because a future change to the encoding has to
// take a new version instead of silently re-reading an old signature under new
// rules. An unrecognised version is refused rather than guessed at.
const CanonicalForm = "vtessera/attest/v1"

// Alg is the only signature algorithm accepted here. A verifier that guesses the
// algorithm from the signature is a verifier that can be talked into a weaker one.
const Alg = "Ed25519"

// Signature is a detached signature over a statement, with enough metadata to
// check it without having talked to the signer.
//
// Digest is checked before the signature because it is cheap and it answers a
// different question: the digest mismatch means this signature is over some other
// content, which is a re-encoding bug here rather than anything anybody did.
type Signature struct {
	Alg      string    `json:"alg"`
	KeyID    string    `json:"keyId"`
	Digest   string    `json:"digest"`
	Value    string    `json:"value"`
	SignedAt time.Time `json:"signedAt"`
}

// Verified is one side of an attestation, reduced to what a page needs to render
// and the reason when it does not hold.
type Verified struct {
	// Attested says a signature exists. Valid says it checks out. They are kept
	// apart because they answer different questions, and a card published before
	// attestations existed has no signature, which is not the same fact as having
	// one that no longer verifies.
	Attested bool
	Valid    bool
	// Reason explains a Valid false on an Attested true, which is the only
	// combination worth an explanation on a page.
	Reason string
	// Checked records that the signature was actually put through the verifier.
	// Without it, "cannot check" and "checked and failed" look identical, and a
	// marketplace speaking a newer encoding would be reported as if it were
	// tampering.
	Checked  bool
	SignedAt time.Time
}

// CardAttestation is what a marketplace knows about a card's provenance.
//
// Market says this marketplace published this card and it has not changed since.
// Agent says the agent stands behind its own claims. Both are needed and neither
// substitutes for the other: the marketplace can confirm it stored a capability
// list, and only the agent can confirm it intends to honour one.
type CardAttestation struct {
	AgentID       string `json:"agentId"`
	Recorded      bool   `json:"recorded"`
	CanonicalForm string `json:"canonicalForm"`
	Marketplace   struct {
		Attested  bool       `json:"attested"`
		Valid     bool       `json:"valid"`
		KeyID     string     `json:"keyId"`
		Signature *Signature `json:"signature,omitempty"`
	} `json:"marketplace"`
	Agent struct {
		Attested  bool       `json:"attested"`
		Valid     bool       `json:"valid"`
		KeyID     string     `json:"keyId"`
		Signature *Signature `json:"signature,omitempty"`
	} `json:"agent"`
}

// ProbeReport is one agent's last recorded capability check.
//
// Absence is not modelled here as a zero value. An agent that has never been
// probed answers 404 with a different code from one that does not exist, and the
// two are different facts: the first means nobody has checked, the second means
// there is nothing to check. So an absent report is NotProbed, not a failure.
type ProbeReport struct {
	AgentID    string         `json:"agentId"`
	Target     string         `json:"target"`
	Passed     bool           `json:"passed"`
	Valid      bool           `json:"valid"`
	CheckedAt  time.Time      `json:"checkedAt"`
	Results    []ProbeOutcome `json:"results"`
	Signature  *Signature     `json:"signature,omitempty"`
	AttestedBy string         `json:"attestedBy,omitempty"`
}

type ProbeOutcome struct {
	Capability string `json:"capability"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
}

// AgentVerification is everything the directory checked about one agent, with the
// verdicts this package computed rather than the ones the service reported.
//
// The service publishes its own valid flags, and they are not used to render
// anything. A directory that displayed them would be displaying the marketplace's
// assessment of the marketplace's work, which is worth less than the reader
// assumes and is not what "verified" would sound like it meant.
type AgentVerification struct {
	AgentID       string
	CardName      string
	CardURL       string
	Marketplace   Verified
	Agent         Verified
	Probe         ProbeVerdict
	CanonicalForm string
}

// ProbeVerdict is the three-way answer about a capability list.
type ProbeVerdict struct {
	// Probed is false when no report exists, which is what NOT_PROBED means.
	Probed    bool
	Passed    bool
	Verified  bool
	CheckedAt time.Time
	// Reason is set when a probe exists and does not check out, and when the
	// marketplace's own report disagreed with the directory's re-check.
	Reason  string
	Target  string
	Results []ProbeOutcome
}

// encoder writes the canonical form: domain separated, length prefixed, sorted.
//
// Length prefixing is in bytes rather than characters, so a card with a non-ASCII
// name cannot be signed one way and checked another. Sorting is inside, so the
// same set of capabilities written in another order is the same claim and an agent
// cannot dodge a changed signature by reordering its own JSON.
type encoder struct{ b strings.Builder }

func newEncoder(kind string, at time.Time) *encoder {
	e := &encoder{}
	e.b.WriteString(CanonicalForm)
	e.b.WriteByte('\n')
	e.b.WriteString("kind:")
	e.b.WriteString(kind)
	e.b.WriteByte('\n')
	// UTC and RFC 3339 so two implementations cannot disagree about what "12:00"
	// meant or how a fractional second is written. This is the signed instant, not
	// a note beside the signature, so a reader cannot have it rewritten.
	e.field("signedAt", at.UTC().Format(time.RFC3339Nano))
	return e
}

func (e *encoder) field(name, value string) {
	e.b.WriteString(name)
	e.b.WriteByte(':')
	e.b.WriteString(strconv.Itoa(len(value)))
	e.b.WriteByte(':')
	e.b.WriteString(value)
	e.b.WriteByte('\n')
}

// list writes a count and then each element. The count is a bare number rather
// than a length-prefixed value, which is why it is not routed through field.
func (e *encoder) list(name string, values []string) {
	e.b.WriteString(name)
	e.b.WriteString(".count:")
	e.b.WriteString(strconv.Itoa(len(values)))
	e.b.WriteByte('\n')
	for i, v := range values {
		e.field(name+"."+strconv.Itoa(i), v)
	}
}

func (e *encoder) skills(values []AgentSkill) {
	ordered := append([]AgentSkill(nil), values...)
	sort.SliceStable(ordered, func(i, j int) bool { return skillKey(ordered[i]) < skillKey(ordered[j]) })
	e.b.WriteString("skills.count:")
	e.b.WriteString(strconv.Itoa(len(ordered)))
	e.b.WriteByte('\n')
	for i, skill := range ordered {
		prefix := "skills." + strconv.Itoa(i)
		e.field(prefix+".id", skill.ID)
		e.field(prefix+".name", skill.Name)
		e.list(prefix+".tags", sortedUnique(skill.Tags))
		e.list(prefix+".inputModes", sortedUnique(skill.Input))
		e.list(prefix+".outputModes", sortedUnique(skill.Output))
	}
}

func (e *encoder) probeResults(results []ProbeOutcome) {
	ordered := append([]ProbeOutcome(nil), results...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Capability < ordered[j].Capability })
	e.b.WriteString("results.count:")
	e.b.WriteString(strconv.Itoa(len(ordered)))
	e.b.WriteByte('\n')
	for i, r := range ordered {
		prefix := "results." + strconv.Itoa(i)
		e.field(prefix+".capability", r.Capability)
		e.field(prefix+".status", r.Status)
		e.field(prefix+".detail", r.Detail)
	}
}

func (e *encoder) bytes() []byte { return []byte(e.b.String()) }

// skillKey is the sort key for a skill. ID alone is not enough: two skills can
// share an ID and disagree about everything else, and sorting on ID alone would
// leave their order down to the input.
func skillKey(s AgentSkill) string {
	return strings.Join([]string{s.ID, s.Name,
		strings.Join(sortedUnique(s.Tags), "\x00"),
		strings.Join(sortedUnique(s.Input), "\x00"),
		strings.Join(sortedUnique(s.Output), "\x00")}, "\x00")
}

func sortedUnique(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// cardBytes is the canonical encoding of an agent card, mirroring CardBytes in the
// service. The order of the fields is part of the format, not an implementation
// detail, so it is written out rather than ranged over a map.
func cardBytes(card AgentCard, agentID string, at time.Time) []byte {
	enc := newEncoder("agent-card", at)
	enc.field("agent", agentID)
	enc.field("name", card.Name)
	enc.field("description", card.Description)
	enc.field("version", card.Version)
	enc.field("url", card.URL)
	enc.list("capabilities", sortedUnique(card.Capabilities))
	enc.skills(card.Skills)
	enc.field("probeTarget", card.ProbeTarget)
	enc.list("currencies", sortedUnique(card.Currencies))
	enc.list("settlementModes", sortedUnique(card.SettlementModes))
	return enc.bytes()
}

func probeBytes(report ProbeReport, at time.Time) []byte {
	enc := newEncoder("probe", at)
	enc.field("agent", report.AgentID)
	enc.field("target", report.Target)
	enc.field("checkedAt", report.CheckedAt.UTC().Format(time.RFC3339Nano))
	enc.probeResults(report.Results)
	return enc.bytes()
}

// verifySignature is the whole of what this package believes about a signature:
// the algorithm is the one named, the signer is the key expected, the digest is
// over the bytes about to be checked, and the signature is made by that key over
// those bytes.
//
// Both keys are named rather than one being inferred. A verifier that only checked
// the content's own key would accept the marketplace vouching for an agent's
// capability list, which is exactly the claim it cannot make.
func verifySignature(payload []byte, sig Signature, expectedKeyID string) error {
	if sig.Alg != Alg {
		return fmt.Errorf("unsupported algorithm %q", sig.Alg)
	}
	if sig.KeyID != expectedKeyID {
		return fmt.Errorf("signed by %s, expected %s", short(sig.KeyID), short(expectedKeyID))
	}
	key, err := base58.Decode(sig.KeyID)
	if err != nil {
		return err
	}
	if len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("key %s is %d bytes, want %d", short(sig.KeyID), len(key), ed25519.PublicKeySize)
	}
	value, err := base64.StdEncoding.DecodeString(sig.Value)
	if err != nil {
		return errors.New("signature is not base64")
	}
	if len(value) != ed25519.SignatureSize {
		return fmt.Errorf("signature is %d bytes, want %d", len(value), ed25519.SignatureSize)
	}
	sum := sha256.Sum256(payload)
	if digest := hex.EncodeToString(sum[:]); sig.Digest != digest {
		return fmt.Errorf("signature is over %s, the content hashes to %s", short(sig.Digest), digest)
	}
	if !ed25519.Verify(ed25519.PublicKey(key), payload, value) {
		return fmt.Errorf("signature does not verify under %s", short(sig.KeyID))
	}
	return nil
}

func short(text string) string {
	if len(text) <= 12 {
		return text
	}
	return text[:6] + "…" + text[len(text)-4:]
}

// VerifyAgent re-derives both signatures over one agent's card and reports what
// holds.
//
// marketplaceKeyID is the key published in /healthz, and it is passed in rather
// than read from the attestation: a signature carries the key that made it, so
// trusting that field would let any key vouch for itself. A caller with no
// verification key cannot check the marketplace side at all, which is reported
// rather than assumed good.
func VerifyAgent(agent Agent, attestation CardAttestation, probe *ProbeReport, marketplaceKeyID string) AgentVerification {
	out := AgentVerification{
		AgentID:       agent.ID,
		CardName:      agent.Card.Name,
		CardURL:       agent.Card.URL,
		CanonicalForm: attestation.CanonicalForm,
		Marketplace:   Verified{Attested: attestation.Marketplace.Attested},
		Agent:         Verified{Attested: attestation.Agent.Attested},
	}
	if attestation.CanonicalForm != "" && attestation.CanonicalForm != CanonicalForm {
		out.Marketplace.Reason = "signed in " + attestation.CanonicalForm + ", which this directory cannot check"
		out.Agent.Reason = out.Marketplace.Reason
		return out
	}
	if sig := attestation.Marketplace.Signature; sig != nil {
		out.Marketplace.Attested = true
		out.Marketplace.SignedAt = sig.SignedAt
		switch {
		case marketplaceKeyID == "":
			out.Marketplace.Reason = "no marketplace verification key was published, so this cannot be checked"
		default:
			payload := cardBytes(agent.Card, agent.ID, sig.SignedAt)
			out.Marketplace.Checked = true
			if err := verifySignature(payload, *sig, marketplaceKeyID); err != nil {
				out.Marketplace.Reason = err.Error()
			} else {
				out.Marketplace.Valid = true
			}
		}
	}
	// The agent's own signature is checked against the agent ID, which is the base58
	// of the key that made it. That is the same identity the session is bound to, so
	// a card signature and a session cannot be two keys that ought to match.
	if sig := attestation.Agent.Signature; sig != nil {
		out.Agent.Attested = true
		out.Agent.SignedAt = sig.SignedAt
		out.Agent.Checked = true
		if err := verifySignature(cardBytes(agent.Card, agent.ID, sig.SignedAt), *sig, agent.ID); err != nil {
			out.Agent.Reason = err.Error()
		} else {
			out.Agent.Valid = true
		}
	}
	out.Probe = verifyProbe(agent.ID, probe, marketplaceKeyID)
	return out
}

func verifyProbe(agentID string, report *ProbeReport, marketplaceKeyID string) ProbeVerdict {
	if report == nil {
		return ProbeVerdict{}
	}
	out := ProbeVerdict{
		Probed:    true,
		Passed:    report.Passed,
		CheckedAt: report.CheckedAt,
		Target:    report.Target,
		Results:   report.Results,
	}
	if report.Signature == nil {
		out.Reason = "the report carries no signature, so it is an unsigned claim"
		return out
	}
	if marketplaceKeyID == "" {
		out.Reason = "no marketplace verification key was published, so this cannot be checked"
		return out
	}
	if report.AttestedBy != "" && report.AttestedBy != marketplaceKeyID {
		out.Reason = "attested by " + short(report.AttestedBy) + ", which is not the published key"
		return out
	}
	if err := verifySignature(probeBytes(*report, report.Signature.SignedAt), *report.Signature, marketplaceKeyID); err != nil {
		out.Reason = err.Error()
		return out
	}
	out.Verified = true
	return out
}

// State is the one-word verdict a template renders. It is deliberately coarse:
// a reader needs to know whether to rely on the line, and the reason beside it
// carries the detail for the reader who wants it.
//
// "unsigned" and "invalid" are separate because they call for different reactions.
// An unsigned card is an older marketplace that never attested anything; an
// invalid one is a card whose bytes no longer match a signature, which is the
// case worth stopping and reading.
func (v Verified) State() string {
	switch {
	case v.Valid:
		return "verified"
	case v.Attested && v.Checked:
		return "invalid"
	case v.Reason != "":
		return "unavailable"
	default:
		return "unsigned"
	}
}

// Detail is the reason to show beside a state, empty when there is nothing to add.
func (v Verified) Detail() string {
	if v.Valid || v.Reason == "" {
		return ""
	}
	return v.Reason
}

func (v Verified) At() time.Time { return v.SignedAt }

// State is the same one-word verdict for a capability list.
//
// "never probed" is its own state rather than a failure, because it is the answer
// for most agents and a page that paints it red would be crying wolf about
// something nobody claimed was checked.
func (p ProbeVerdict) State() string {
	switch {
	case !p.Probed:
		return "never probed"
	case !p.Verified:
		if p.Reason != "" {
			return "unverified"
		}
		return "unsigned"
	case p.Passed:
		return "verified"
	default:
		return "failed"
	}
}

func (p ProbeVerdict) Detail() string { return p.Reason }

// Class is State with the spaces removed, for use as a CSS class. A state is
// prose and a class is not, and a state containing a space would silently split
// into two meaningless classes and style nothing.
func (v Verified) Class() string { return stateClass(v.State()) }

func (p ProbeVerdict) Class() string { return stateClass(p.State()) }

func stateClass(state string) string {
	return strings.ReplaceAll(state, " ", "-")
}

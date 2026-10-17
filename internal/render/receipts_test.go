package render

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemaio/agent-ai-tool/internal/base58"
	"github.com/douglasdemaio/agent-ai-tool/internal/live"
)

// signedReceipt produces a receipt a reader can verify: a real key, named in the
// JWS header, over real claims. The render tests build the same token the
// marketplace would, so the verdict on the page is computed rather than staged.
func signedReceipt(t *testing.T, amount string) (*live.ReceiptsResponse, string, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	kid := base58.Encode(pub)
	claims := live.ReceiptClaims{
		Trade: live.ReceiptTrade{
			TradeID:    "trade-1",
			Buyer:      "buyer-key",
			Seller:     "seller-key",
			Amount:     amount,
			Mint:       "USDC",
			Mode:       "offchain",
			State:      "delivered",
			RecordedAt: time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC),
		},
		Settlement: live.ReceiptSettlement{Mode: "offchain", LedgerSequence: 7},
	}
	header, err := json.Marshal(map[string]string{"alg": "EdDSA", "kid": kid})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload)
	jws := signingInput + "." +
		base64.RawURLEncoding.EncodeToString(ed25519.Sign(priv, []byte(signingInput)))

	return &live.ReceiptsResponse{
		Count:    1,
		Receipts: []live.Receipt{{TradeID: "trade-1", JWS: jws, VerificationKey: kid, Valid: true, IssuedAt: claims.Trade.RecordedAt}},
	}, kid, priv
}

func TestTheTradesPageExistsEvenWithNoMarketplaceAnswer(t *testing.T) {
	out := renderTo(t, site(t, entry("vtessera", "A marketplace.")))
	page := read(t, out, "trades/index.html")
	if !strings.Contains(page, "nothing here to check") {
		t.Error("a gallery with no listing must say so rather than render an empty table")
	}
}

func TestTheTradesPageSaysSoWhenTheListingIsEmpty(t *testing.T) {
	s := site(t)
	s.Marketplace = &live.Health{VerificationKey: "aKey"}
	s.Receipts = &live.ReceiptsResponse{}
	page := read(t, renderTo(t, s), "trades/index.html")
	if !strings.Contains(page, "no delivered trades yet") {
		t.Error("an empty listing should say no trade has settled rather than render an empty table")
	}
}

func TestTheTradesPageShowsAReceiptItVerified(t *testing.T) {
	receipts, kid, _ := signedReceipt(t, "1000000")
	s := site(t)
	s.Marketplace = &live.Health{VerificationKey: kid}
	s.Receipts = receipts

	page := read(t, renderTo(t, s), "trades/index.html")
	if !strings.Contains(page, "state-verified") {
		t.Error("a receipt signed by the published key should render as verified")
	}
	if !strings.Contains(page, "1000000") {
		t.Error("the verified receipt's amount should be shown")
	}
	if !strings.Contains(page, "1 of 1 signatures re-verified") {
		t.Error("the headline should name the verified count")
	}
}

func TestAReceiptSignedByAnUnpublishedKeyIsNotCounted(t *testing.T) {
	receipts, _, _ := signedReceipt(t, "1000000")
	// Publish a key that did not sign the receipt.
	_, otherKid, _ := signedReceipt(t, "1")
	s := site(t)
	s.Marketplace = &live.Health{VerificationKey: otherKid}
	s.Receipts = receipts

	page := read(t, renderTo(t, s), "trades/index.html")
	if strings.Contains(page, "state-verified") {
		t.Error("a receipt whose signing key is not published must not verify")
	}
	if strings.Contains(page, "1000000") {
		t.Error("the claims of an unverified receipt must not be shown as fact")
	}
}

func TestARetiredKeyStillVerifiesUnderTheKeyring(t *testing.T) {
	// A receipt signed before a rotation is verified against the retired key,
	// which is exactly what the keyring is published for.
	receipts, retiredKid, _ := signedReceipt(t, "1000000")
	_, currentKid, _ := signedReceipt(t, "1")

	s := site(t)
	s.Marketplace = &live.Health{VerificationKey: currentKid, VerificationKeys: []string{currentKid, retiredKid}}
	s.Receipts = receipts
	page := read(t, renderTo(t, s), "trades/index.html")
	if !strings.Contains(page, "state-verified") {
		t.Error("a receipt signed by a key in the published keyring should verify")
	}
}

// noKeyReceipt is a receipt whose JWS header names no key, the shape the
// marketplace's earliest receipts have: three base64url parts, EdDSA, no kid. The
// verifier stops at the missing key, so the signature need not hold.
func noKeyReceipt(t *testing.T, issued time.Time) *live.ReceiptsResponse {
	t.Helper()
	header, err := json.Marshal(map[string]string{"alg": "EdDSA"})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(live.ReceiptClaims{
		Trade: live.ReceiptTrade{TradeID: "trade-old", RecordedAt: issued},
	})
	if err != nil {
		t.Fatal(err)
	}
	jws := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
	return &live.ReceiptsResponse{
		Count:    1,
		Receipts: []live.Receipt{{TradeID: "trade-old", JWS: jws, IssuedAt: issued}},
	}
}

func TestATradesPageExplainsAReceiptItCouldNotCheck(t *testing.T) {
	issued := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	s := site(t)
	s.Marketplace = &live.Health{VerificationKey: "aKey"}
	s.Receipts = noKeyReceipt(t, issued)

	page := read(t, renderTo(t, s), "trades/index.html")
	if !strings.Contains(page, "state-unavailable") {
		t.Error("a receipt whose header names no key should render as unavailable")
	}
	if !strings.Contains(page, "that could not be checked") {
		t.Error("the page should explain the rows it could not check rather than leave the state bare")
	}
	if !strings.Contains(page, "names no signing key at all") {
		t.Error("the explanation should say why the header cannot be checked")
	}
	if !strings.Contains(page, "2026-09-29") {
		t.Error("the explanation should date the earliest receipt it could not check")
	}
}

func TestAReceiptThatFailedVerificationIsNotCalledUnavailable(t *testing.T) {
	receipts, kid, _ := signedReceipt(t, "1000000")
	// Keep the header naming the published key but replace the signature, so the
	// verifier runs and the bytes do not hold: checked and failed, not unchecked.
	parts := strings.Split(receipts.Receipts[0].JWS, ".")
	receipts.Receipts[0].JWS = parts[0] + "." + parts[1] + "." +
		base64.RawURLEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))

	s := site(t)
	s.Marketplace = &live.Health{VerificationKey: kid}
	s.Receipts = receipts

	page := read(t, renderTo(t, s), "trades/index.html")
	if !strings.Contains(page, "state-invalid") {
		t.Error("a signature that was checked and failed should render as invalid")
	}
	if strings.Contains(page, "could not be checked") {
		t.Error("a signature that failed must not be reported as one that could not be checked")
	}
}

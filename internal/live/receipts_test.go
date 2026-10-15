package live

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// signJWS builds the compact JWS shape vtessera publishes, from the same key and
// the same claims the gallery will later hand to a verifier. The signing input is
// exactly the two base64url segments joined by a dot, which is what the verifier
// slices back out of the token rather than re-encoding.
func signJWS(t *testing.T, priv ed25519.PrivateKey, kid string, claims ReceiptClaims) string {
	t.Helper()
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
	signature := ed25519.Sign(priv, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func sampleClaims() ReceiptClaims {
	return ReceiptClaims{
		Trade: ReceiptTrade{
			TradeID:    "trade-1",
			Buyer:      "buyer-key",
			Seller:     "seller-key",
			Amount:     "1000000",
			Mint:       "USDC",
			Mode:       "offchain",
			State:      "delivered",
			RecordedAt: time.Date(2026, 9, 27, 0, 0, 0, 0, time.UTC),
		},
		Settlement: ReceiptSettlement{Mode: "offchain", LedgerSequence: 7},
	}
}

func TestAReceiptSignedByThePublishedKeyVerifies(t *testing.T) {
	priv, kid := newKey(t)
	claims := sampleClaims()
	jws := signJWS(t, priv, kid, claims)

	got := VerifyReceipt(Receipt{TradeID: "trade-1", JWS: jws, VerificationKey: kid}, []string{kid})
	if !got.Valid {
		t.Fatalf("a receipt signed by the published key did not verify: %s", got.Reason)
	}
	if !got.Checked {
		t.Error("a checked signature must be marked checked")
	}
	if got.KeyID != kid {
		t.Errorf("key id = %q, want %q", got.KeyID, kid)
	}
	if got.Claims == nil || got.Claims.Trade.Amount != "1000000" {
		t.Errorf("claims were not recovered from the signature: %+v", got.Claims)
	}
}

func TestAReceiptSignedByAKeyTheMarketplaceNeverPublishedDoesNotVerify(t *testing.T) {
	// The signature is genuine; the key is simply not one the marketplace
	// publishes. Trusting the kid in the header would accept this, which is the
	// whole reason the keyring is passed in rather than read from the token.
	priv, kid := newKey(t)
	_, published := newKey(t)
	jws := signJWS(t, priv, kid, sampleClaims())

	got := VerifyReceipt(Receipt{TradeID: "trade-1", JWS: jws}, []string{published})
	if got.Valid {
		t.Fatal("a receipt signed by an unpublished key verified")
	}
	if !strings.Contains(got.Reason, "keyring") {
		t.Errorf("reason should name the keyring, got %q", got.Reason)
	}
}

func TestATamperedReceiptDoesNotVerify(t *testing.T) {
	priv, kid := newKey(t)
	jws := signJWS(t, priv, kid, sampleClaims())

	// Flip the payload: re-encode modified claims with the original signature.
	parts := strings.Split(jws, ".")
	tampered := sampleClaims()
	tampered.Trade.Amount = "9999999"
	payload, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	forged := parts[0] + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + parts[2]

	got := VerifyReceipt(Receipt{TradeID: "trade-1", JWS: forged}, []string{kid})
	if got.Valid {
		t.Fatal("a receipt whose payload changed after signing verified")
	}
	if !got.Checked {
		t.Error("a signature that was checked and failed must be marked checked")
	}
}

func TestAnEmptyKeyringTrustsNothing(t *testing.T) {
	priv, kid := newKey(t)
	jws := signJWS(t, priv, kid, sampleClaims())

	got := VerifyReceipt(Receipt{TradeID: "trade-1", JWS: jws}, nil)
	if got.Valid {
		t.Fatal("a receipt verified against an empty keyring")
	}
	if got.Checked {
		t.Error("nothing was checkable with no key, so nothing was checked")
	}
}

func TestAReceiptWithAnUnsupportedAlgorithmDoesNotVerify(t *testing.T) {
	// A token claiming "none" must not be talked into verifying: the algorithm is
	// the one named and only EdDSA is accepted.
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","kid":"x"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{}`))
	jws := header + "." + payload + "."
	got := VerifyReceipt(Receipt{TradeID: "t", JWS: jws}, []string{"x"})
	if got.Valid {
		t.Fatal("a token with alg none verified")
	}
	if !strings.Contains(got.Reason, "algorithm") {
		t.Errorf("reason should name the algorithm, got %q", got.Reason)
	}
}

func TestATradeWithOneOfOurAgentsIsTestTraffic(t *testing.T) {
	if !IsProbeTrade(&ReceiptClaims{Trade: ReceiptTrade{Buyer: ProbeAgents[0], Seller: "stranger"}}) {
		t.Error("a trade with our agent as buyer should be test traffic")
	}
	if !IsProbeTrade(&ReceiptClaims{Trade: ReceiptTrade{Buyer: "stranger", Seller: ProbeAgents[1]}}) {
		t.Error("a trade with our agent as seller should be test traffic")
	}
	if IsProbeTrade(&ReceiptClaims{Trade: ReceiptTrade{Buyer: "a", Seller: "b"}}) {
		t.Error("a trade between strangers is not our test traffic")
	}
	if IsProbeTrade(nil) {
		t.Error("no claims is no evidence of our traffic")
	}
}

func TestValidateReceiptsRefusesSomethingThatIsNotAListing(t *testing.T) {
	if err := ValidateReceipts([]byte(`{"count":0,"receipts":[]}`)); err != nil {
		t.Errorf("a valid empty listing was refused: %v", err)
	}
	if err := ValidateReceipts([]byte(`not json`)); err == nil {
		t.Error("a body that is not JSON was accepted")
	}
}

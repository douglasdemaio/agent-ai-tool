package live

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/douglasdemaio/agent-ai-tool/internal/base58"
)

// ReceiptsResponse is vtessera's public listing of delivered trades' signed
// receipts, newest first.
//
// The listing carries the whole keyring beside the receipts so a reader can name
// the key that signed each one, but this directory does not verify against the
// keyring in here: a signature and the key it names, arriving in the same
// response, prove only that whoever wrote the response can sign. The key is taken
// from /healthz instead, and the keyring in the listing is used for nothing the
// page claims.
type ReceiptsResponse struct {
	Genesis          string    `json:"genesis,omitempty"`
	Count            int       `json:"count"`
	Truncated        bool      `json:"truncated"`
	Receipts         []Receipt `json:"receipts"`
	VerificationKey  string    `json:"verificationKey,omitempty"`
	VerificationKeys []string  `json:"verificationKeys,omitempty"`
	SigningAlgorithm string    `json:"signingAlgorithm,omitempty"`
	KeyEncoding      string    `json:"keyEncoding,omitempty"`
}

// Receipt is one delivered trade as the marketplace publishes it. Claims is what
// the marketplace reads out of the JWS; this directory re-derives it from the
// signature rather than rendering this field, which is the marketplace's own
// account of its own receipt.
type Receipt struct {
	TradeID         string         `json:"tradeId"`
	JWS             string         `json:"jws"`
	VerificationKey string         `json:"verificationKey"`
	Valid           bool           `json:"valid"`
	Claims          *ReceiptClaims `json:"claims,omitempty"`
	IssuedAt        time.Time      `json:"issuedAt"`
}

// ReceiptClaims is the payload of the tessera JWS, narrowed to what the gallery
// renders. The claims are inside the signature, so a page that shows them is
// showing the signed content rather than a description of it.
type ReceiptClaims struct {
	Trade      ReceiptTrade      `json:"trade"`
	Settlement ReceiptSettlement `json:"settlement"`
}

type ReceiptTrade struct {
	TradeID     string    `json:"tradeId"`
	Buyer       string    `json:"buyer"`
	Seller      string    `json:"seller"`
	Description string    `json:"description,omitempty"`
	Amount      string    `json:"amount"`
	Mint        string    `json:"mint"`
	Mode        string    `json:"mode"`
	State       string    `json:"state"`
	RecordedAt  time.Time `json:"recordedAt"`
}

type ReceiptSettlement struct {
	Mode            string `json:"mode"`
	Signature       string `json:"signature,omitempty"`
	LedgerSequence  int64  `json:"ledgerSequence"`
	LedgerEntryHash string `json:"ledgerEntryHash,omitempty"`
	// Cluster is absent on a receipt issued before cluster awareness, which is a
	// statement about when it was issued rather than which chain it settled on.
	Cluster string `json:"cluster,omitempty"`
}

func ValidateReceipts(body []byte) error {
	var response ReceiptsResponse
	if err := probe(body, "receipts", &response); err != nil {
		return err
	}
	if response.Count < 0 {
		return errors.New("count is negative")
	}
	return nil
}

func (r ReceiptsResponse) Normalized() ReceiptsResponse {
	if r.Receipts == nil {
		r.Receipts = []Receipt{}
	}
	return r
}

// ReceiptVerdict is the directory's own answer about one receipt. The
// marketplace's valid flag is never rendered: a page that displayed it would be
// printing the marketplace's assessment of the marketplace's work, which is the
// trust this gallery exists to replace with a check of its own.
type ReceiptVerdict struct {
	TradeID string
	// Checked records that the signature was actually put through the verifier,
	// so "cannot check" and "checked and failed" do not look alike.
	Checked bool
	Valid   bool
	Reason  string
	KeyID   string
	Claims  *ReceiptClaims
}

// VerifyReceipt re-derives the tessera signature from the JWS itself.
//
// trustedKeys is the marketplace's published keyring, current first. The key that
// made the signature is named in the JWS header, and it is only used if it is one
// of these: a signature that named its own key and was believed would let any key
// vouch for itself, which is the whole reason the keyring is passed in from
// /healthz rather than read from the receipt.
func VerifyReceipt(receipt Receipt, trustedKeys []string) ReceiptVerdict {
	out := ReceiptVerdict{TradeID: receipt.TradeID}
	claims, keyID, checked, err := verifyTessera(receipt.JWS, trustedKeys)
	out.Checked = checked
	out.KeyID = keyID
	if err != nil {
		out.Reason = err.Error()
		return out
	}
	out.Valid = true
	out.Claims = claims
	return out
}

// verifyTessera checks a compact JWS: EdDSA only, the header's kid must be a
// trusted key, and the signature must cover the exact header.payload bytes as
// written. The signing input is sliced from the JWS rather than re-encoded, so a
// header this directory reserialised differently cannot change what was signed.
//
// checked reports whether the signature was actually evaluated, which is a
// different fact from whether it held: a token that named an unpublished key was
// never checked, and the page must not print it as a failed signature. checked is
// true only once verify has run, so a reader can tell "not our key" from "our key
// and the bytes do not match".
func verifyTessera(jws string, trustedKeys []string) (claims *ReceiptClaims, kid string, checked bool, err error) {
	parts := strings.Split(jws, ".")
	if len(parts) != 3 {
		return nil, "", false, errors.New("receipt is not a compact JWS")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, "", false, errors.New("JWS header is not base64url")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, "", false, errors.New("JWS header is not JSON")
	}
	// The algorithm is the one named, not inferred from the signature: a verifier
	// that guessed could be talked into a weaker one.
	if header.Alg != "EdDSA" {
		return nil, "", false, fmt.Errorf("unsupported JWS algorithm %q", header.Alg)
	}
	if !trusted(header.Kid, trustedKeys) {
		if header.Kid == "" {
			return nil, "", false, errors.New("JWS header names no signing key")
		}
		return nil, header.Kid, false, fmt.Errorf("signed by %s, which is not in the published keyring", short(header.Kid))
	}
	key, err := base58.Decode(header.Kid)
	if err != nil {
		return nil, header.Kid, false, err
	}
	if len(key) != ed25519.PublicKeySize {
		return nil, header.Kid, false, fmt.Errorf("key %s is %d bytes, want %d", short(header.Kid), len(key), ed25519.PublicKeySize)
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, header.Kid, false, errors.New("JWS signature is not base64url")
	}
	if len(signature) != ed25519.SignatureSize {
		return nil, header.Kid, false, fmt.Errorf("signature is %d bytes, want %d", len(signature), ed25519.SignatureSize)
	}
	signingInput := jws[:len(jws)-len(parts[2])-1]
	// Past this line the signature is being checked, so every failure below is a
	// signature that was evaluated and did not hold.
	if !ed25519.Verify(ed25519.PublicKey(key), []byte(signingInput), signature) {
		return nil, header.Kid, true, fmt.Errorf("signature does not verify under %s", short(header.Kid))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, header.Kid, true, errors.New("JWS payload is not base64url")
	}
	var decoded ReceiptClaims
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, header.Kid, true, fmt.Errorf("JWS payload is not a tessera: %w", err)
	}
	if decoded.Trade.TradeID == "" {
		return nil, header.Kid, true, errors.New("tessera is missing trade.tradeId")
	}
	return &decoded, header.Kid, true, nil
}

// trusted reports whether key is one of the marketplace's published keys. An
// empty keyring trusts nothing, which fails closed rather than open.
func trusted(key string, keyring []string) bool {
	if key == "" {
		return false
	}
	for _, k := range keyring {
		if k == key {
			return true
		}
	}
	return false
}

// IsProbeTrade reports whether a receipt describes a trade between this
// repository's own test agents. A trade where either party is ours is our
// traffic: the marketplace's totals include it, and a gallery that presented it
// as outside usage would be making the same claim the metrics banner refuses to
// make.
func IsProbeTrade(claims *ReceiptClaims) bool {
	if claims == nil {
		return false
	}
	return IsProbeAgent(claims.Trade.Buyer) || IsProbeAgent(claims.Trade.Seller)
}

// Package base58 decodes the Bitcoin alphabet, which is how a Solana or Ed25519
// identity is written in the vtessera API. It lives in its own package because
// two others need it and neither should depend on the other.
//
// The alphabet excludes 0, O, I and l, so a transcription slip is usually a
// rejection rather than a different key. That is the property worth having: a
// mistyped address must not decode to a valid-looking key belonging to someone
// else.
package base58

import (
	"errors"
	"fmt"
	"math/big"
)

const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

var decodeTable = func() [256]int8 {
	var table [256]int8
	for i := range table {
		table[i] = -1
	}
	for i := 0; i < len(alphabet); i++ {
		table[alphabet[i]] = int8(i)
	}
	return table
}()

// Decode returns the bytes an address encodes, or an error naming the first
// character it could not read.
func Decode(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("is empty")
	}
	number := new(big.Int)
	radix := big.NewInt(58)
	for i := 0; i < len(s); i++ {
		value := decodeTable[s[i]]
		if value < 0 {
			return nil, fmt.Errorf("contains %q, which is not in the base58 alphabet", s[i])
		}
		number.Mul(number, radix)
		number.Add(number, big.NewInt(int64(value)))
	}
	raw := number.Bytes()
	// A leading '1' is a leading zero byte, which big.Int cannot represent.
	leading := 0
	for leading < len(s) && s[leading] == '1' {
		leading++
	}
	out := make([]byte, leading+len(raw))
	copy(out[leading:], raw)
	return out, nil
}

// Encode is the inverse, used to build a fixture key rather than to invent one:
// a test that hard-codes a key nobody can regenerate is a test nobody can extend.
func Encode(raw []byte) string {
	number := new(big.Int).SetBytes(raw)
	radix := big.NewInt(58)
	mod := new(big.Int)
	var out []byte
	for number.Sign() > 0 {
		number.DivMod(number, radix, mod)
		out = append(out, alphabet[mod.Int64()])
	}
	for _, b := range raw {
		if b != 0 {
			break
		}
		out = append(out, alphabet[0])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// DecodeLen is Decode with the length required by a 32-byte identity, for the
// many addresses that must be exactly that.
func DecodeLen(s string, want int) error {
	raw, err := Decode(s)
	if err != nil {
		return err
	}
	if len(raw) != want {
		return fmt.Errorf("must decode to %d bytes, got %d", want, len(raw))
	}
	return nil
}

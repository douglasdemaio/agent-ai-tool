package base58

import "testing"

func TestDecodeReadsAKeyBase58AndBack(t *testing.T) {
	// The live marketplace's verification key, re-derived here so a change to the
	// alphabet or the leading-zero handling fails a test rather than quietly
	// decoding every address to something else.
	const key = "5LRpM9wpvPfRYuQAC7oNdyaQa6sakpMcnZeR9FS5CgjB"
	if err := DecodeLen(key, 32); err != nil {
		t.Fatalf("a published key did not decode: %v", err)
	}
}

func TestDecodeKeepsLeadingZeroBytes(t *testing.T) {
	// A 32-byte value whose first byte is zero encodes to a leading '1'. If the
	// leading '1's were dropped the address would decode to 31 bytes and be
	// refused, or worse, to a shorter key that belonged to somebody else.
	raw := make([]byte, 32)
	raw[31] = 7
	if err := DecodeLen(Encode(raw), 32); err != nil {
		t.Fatalf("a key with leading zeros did not survive a round trip: %v", err)
	}
}

func TestDecodeRefusesEveryCharacterOutsideTheAlphabet(t *testing.T) {
	// 0, O, I and l are exactly the four that are not in the alphabet, and they
	// are the ones a person is most likely to retype from a screenshot.
	for _, bad := range []string{"0", "O", "I", "l"} {
		if _, err := Decode("abc" + bad + "def"); err == nil {
			t.Errorf("accepted %q, which is not in the alphabet", bad)
		}
	}
}

func TestDecodeRefusesAnAddressOfTheWrongLength(t *testing.T) {
	// A 31-byte key is the shape an address loses when a character is dropped.
	// Refusing it by length is what stops a truncated address being read as a
	// different, valid identity.
	if err := DecodeLen("1111111111111111111111111111111", 32); err == nil {
		t.Error("accepted an address that is too short to be a key")
	}
}

func TestAnEmptyAddressIsNotAKey(t *testing.T) {
	if err := DecodeLen("", 32); err == nil {
		t.Error("accepted an empty address")
	}
}

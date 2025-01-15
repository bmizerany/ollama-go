package blob

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidDigest = errors.New("invalid digest")
)

// Digest is a blob identifier that is the SHA-256 hash of a blob's content.
//
// It is comparable and can be used as a map key.
type Digest struct {
	sum [32]byte
}

// PaseDigest parses a digest from a string. If the string is not a valid
// digest, a call to the returned digest's IsValid method will return false.
//
// The input string may be in one of two forms:
//
//   - ("sha256-<hex>"), where <hex> is a 64-character hexadecimal string.
//   - ("sha256:<hex>"), where <hex> is a 64-character hexadecimal string.
//
// The [Digest.String] method will return the canonical form of the
// digest, "sha256:<hex>".
func ParseDigest(s string) Digest {
	i := strings.IndexAny(s, ":-")
	if i < 0 {
		return Digest{}
	}

	prefix, sum := s[:i], s[i+1:]
	if prefix != "sha256" || len(sum) != 64 {
		return Digest{}
	}

	var d Digest
	_, err := hex.Decode(d.sum[:], []byte(sum))
	if err != nil {
		return Digest{}
	}
	return d
}

// IsValid returns whether the digest is valid. A digest is valid if it is not
// the zero value.
//
// To obtain a valid digest, use [ParseDigest].
func (d Digest) IsValid() bool {
	return d != Digest{}
}

// String returns the string representation of the digest. If the digest is
// invalid, it returns "<invalid>", otherwise it returns "sha256:<sum>".
func (d Digest) String() string {
	if !d.IsValid() {
		return "<invalid>"
	}
	return fmt.Sprintf("sha256:%x", d.sum[:])
}

// MarshalText implements the encoding.TextMarshaler interface. It returns an
// error if [Digest.IsValid] returns false.
func (d Digest) MarshalText() ([]byte, error) {
	if !d.IsValid() {
		return nil, ErrInvalidDigest
	}
	return []byte(d.String()), nil
}

// UnmarshalText implements the encoding.TextUnmarshaler interface, and only
// works for a zero digest. If [Digest.IsValid] returns true, it returns an
// error.
func (d *Digest) UnmarshalText(text []byte) error {
	if d.IsValid() {
		return errors.New("digest: illegal UnmarshalText on valid digest")
	}
	*d = ParseDigest(string(text))
	if !d.IsValid() {
		return ErrInvalidDigest
	}
	return nil
}

func isHex(s string) bool {
	for _, r := range s {
		if !('0' <= r && r <= '9' || 'a' <= r && r <= 'f') {
			return false
		}
	}
	return true
}

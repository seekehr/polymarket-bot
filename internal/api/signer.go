package api

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"golang.org/x/crypto/sha3"
)

// Address is a 20-byte Ethereum address.
type Address [20]byte

// ParseAddress parses a 0x-prefixed hex address.
func ParseAddress(s string) (Address, error) {
	var a Address
	b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(s), "0x"))
	if err != nil || len(b) != len(a) {
		return a, fmt.Errorf("invalid address %q", s)
	}
	copy(a[:], b)
	return a, nil
}

// String returns the EIP-55 checksummed form, which is what Polymarket expects.
func (a Address) String() string {
	lower := hex.EncodeToString(a[:])
	hash := keccak256([]byte(lower))
	out := []byte("0x" + lower)
	for i := 0; i < len(lower); i++ {
		nibble := hash[i/2] >> 4
		if i%2 == 1 {
			nibble = hash[i/2] & 0x0f
		}
		if lower[i] >= 'a' && nibble >= 8 {
			out[i+2] = lower[i] - 32
		}
	}
	return string(out)
}

// Signer holds a wallet private key and signs 32-byte digests the way
// Ethereum does (secp256k1, 65-byte r||s||v with v = 27 or 28).
type Signer struct {
	key     *secp256k1.PrivateKey
	address Address
}

// NewSigner parses a hex private key, with or without 0x.
func NewSigner(hexKey string) (*Signer, error) {
	b, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(hexKey), "0x"))
	if err != nil || len(b) != 32 {
		return nil, fmt.Errorf("private key must be 32 bytes of hex")
	}
	key := secp256k1.PrivKeyFromBytes(b)
	pub := key.PubKey().SerializeUncompressed() // 0x04 || X || Y
	var addr Address
	copy(addr[:], keccak256(pub[1:])[12:])
	return &Signer{key: key, address: addr}, nil
}

// Address returns the signer's wallet address.
func (s *Signer) Address() Address { return s.address }

// Sign signs a digest, returning r || s || v.
func (s *Signer) Sign(digest [32]byte) []byte {
	compact := ecdsa.SignCompact(s.key, digest[:], false) // v || r || s, v = 27 + recovery id
	return append(compact[1:], compact[0])
}

func keccak256(parts ...[]byte) []byte {
	h := sha3.NewLegacyKeccak256()
	for _, p := range parts {
		h.Write(p)
	}
	return h.Sum(nil)
}

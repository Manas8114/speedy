package crypto

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/curve25519"
)

// KeySize is 32 bytes for X25519 and ChaCha20-Poly1305 keys
const KeySize = 32

// Key represents a 32-byte cryptographic key (X25519 or symmetric)
type Key [KeySize]byte

func (k Key) String() string {
	return hex.EncodeToString(k[:])
}

// KeyFromHex parses a hex-encoded 32-byte key
func KeyFromHex(s string) (Key, error) {
	b, err := hex.DecodeString(s)
	if err != nil {
		return Key{}, err
	}
	if len(b) != KeySize {
		return Key{}, fmt.Errorf("invalid key length %d, expected %d", len(b), KeySize)
	}
	var k Key
	copy(k[:], b)
	return k, nil
}

// KeyPair represents an X25519 private/public key pair
type KeyPair struct {
	Private Key
	Public  Key
}

// GenerateKeyPair generates a new random X25519 key pair
func GenerateKeyPair() (KeyPair, error) {
	var priv Key
	if _, err := io.ReadFull(rand.Reader, priv[:]); err != nil {
		return KeyPair{}, err
	}
	// Clamp private key according to RFC 7748
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	var pub Key
	curve25519.ScalarBaseMult((*[32]byte)(&pub), (*[32]byte)(&priv))

	return KeyPair{
		Private: priv,
		Public:  pub,
	}, nil
}

// DH performs Diffie-Hellman on Curve25519
func DH(privateKey, publicKey Key) (Key, error) {
	var shared Key
	curve25519.ScalarMult((*[32]byte)(&shared), (*[32]byte)(&privateKey), (*[32]byte)(&publicKey))

	// Check for all-zero output (low order points)
	var allZero Key
	if shared == allZero {
		return Key{}, errors.New("diffie-hellman produced low-order point")
	}
	return shared, nil
}

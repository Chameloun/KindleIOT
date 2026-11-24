package ble

import (
	"crypto/aes"
	"crypto/cipher"

	"kindleiot/internal/config"
)

// IRKMatcher resolves Bluetooth LE Resolvable Private Addresses (RPAs) against an IRK.
// It checks both standard byte order and reversed byte order so users can paste keys
// exported from either macOS Keychain or BlueZ/Home Assistant without manual flipping.
type IRKMatcher struct {
	normal   cipher.Block
	reversed cipher.Block
}

// NewIRKMatcher builds an AES-128 IRK matcher from a hex or Base64 IRK string.
func NewIRKMatcher(raw string) (*IRKMatcher, error) {
	key, err := config.DecodeIRK(raw)
	if err != nil {
		return nil, err
	}
	normal, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	var rev [16]byte
	for i := 0; i < 16; i++ {
		rev[i] = key[15-i]
	}
	reversed, err := aes.NewCipher(rev[:])
	if err != nil {
		return nil, err
	}
	return &IRKMatcher{
		normal:   normal,
		reversed: reversed,
	}, nil
}

// Matches reports whether addrLE (6 bytes little-endian as received over HCI) is an RPA
// generated with this IRK according to Bluetooth Core Spec Vol 3 Part H Sec 2.2.2 ah(k, r).
func (m *IRKMatcher) Matches(addrLE [6]byte) bool {
	if m == nil || (addrLE[5]&0xC0) != 0x40 {
		return false
	}
	var rPrime [16]byte
	rPrime[13] = addrLE[5]
	rPrime[14] = addrLE[4]
	rPrime[15] = addrLE[3]
	return ahMatch(m.normal, rPrime, addrLE) || ahMatch(m.reversed, rPrime, addrLE)
}

// ResolveRPA checks whether addrLE matches a single 16-byte IRK in standard Core Spec byte order.
func ResolveRPA(addrLE [6]byte, irk [16]byte) bool {
	if (addrLE[5] & 0xC0) != 0x40 {
		return false
	}
	block, err := aes.NewCipher(irk[:])
	if err != nil {
		return false
	}
	var rPrime [16]byte
	rPrime[13] = addrLE[5]
	rPrime[14] = addrLE[4]
	rPrime[15] = addrLE[3]
	return ahMatch(block, rPrime, addrLE)
}

func ahMatch(block cipher.Block, rPrime [16]byte, addrLE [6]byte) bool {
	if block == nil {
		return false
	}
	var out [16]byte
	block.Encrypt(out[:], rPrime[:])
	return out[13] == addrLE[2] && out[14] == addrLE[1] && out[15] == addrLE[0]
}

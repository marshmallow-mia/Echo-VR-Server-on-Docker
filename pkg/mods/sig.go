package mods

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
)

// IndexKeys are the Ed25519 public keys release.echovr.de's index.json may be
// signed with (echo-cdn-index on release.echo signs every index). They are the
// launcher's (core::sources SIGNING_KEYS): a new key is added here before the
// server signs with it.
var IndexKeys = [][]byte{{
	0xce, 0x04, 0x7f, 0xc8, 0x2a, 0x3d, 0x30, 0x8a, 0xa1, 0x6b, 0x6e, 0xe1, 0x1d, 0xcf, 0xe7, 0xb1,
	0xf5, 0x57, 0xf0, 0x46, 0x59, 0xa4, 0x89, 0x59, 0xa0, 0xe7, 0x07, 0x84, 0xf3, 0x4e, 0x15, 0x95,
}}

// signed reports whether sigs (an index.json.sig) holds a valid signature of
// exactly text by one of keys. Each signature names its key by the first 16
// hex digits of the key's SHA-256 and the text by its SHA-256.
func signed(text, sigs []byte, keys [][]byte) bool {
	var f struct {
		Signatures []struct {
			Key    string `json:"key"`
			SHA256 string `json:"sha256"`
			Sig    string `json:"sig"`
		} `json:"signatures"`
	}
	if json.Unmarshal(sigs, &f) != nil {
		return false
	}
	sum := sha256Hex(text)
	for _, s := range f.Signatures {
		if !strings.EqualFold(s.SHA256, sum) {
			continue
		}
		sig, err := base64.StdEncoding.DecodeString(s.Sig)
		if err != nil || len(sig) != ed25519.SignatureSize {
			continue
		}
		for _, k := range keys {
			if len(k) == ed25519.PublicKeySize && strings.EqualFold(sha256Hex(k)[:16], s.Key) &&
				ed25519.Verify(ed25519.PublicKey(k), text, sig) {
				return true
			}
		}
	}
	return false
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

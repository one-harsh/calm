// Copyright 2026 The CALM Authors
// SPDX-License-Identifier: Apache-2.0

package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

// NewRandomToken returns a 32-byte crypto-random string, base64url-encoded
// (43 chars). 256 bits of entropy.
func NewRandomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashToken: namespace acts as a domain separator so the same raw token in
// two namespaces hashes to two distinct bytea values.
func HashToken(namespace, rawToken string) []byte {
	h := sha256.New()
	h.Write([]byte(namespace))
	h.Write([]byte{0})
	h.Write([]byte(rawToken))
	return h.Sum(nil)
}

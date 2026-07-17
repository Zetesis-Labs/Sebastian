package session

import "crypto/sha256"

func DigestSecret(secret string) []byte {
	digest := sha256.Sum256([]byte(secret))
	return digest[:]
}

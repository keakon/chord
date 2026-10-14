package llm

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
)

func diagnosticDomain(secret []byte, fields ...string) [32]byte {
	h := hmac.New(sha256.New, secret)
	for _, s := range fields {
		var size [8]byte
		binary.LittleEndian.PutUint64(size[:], uint64(len(s)))
		h.Write(size[:])
		h.Write([]byte(s))
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum
}

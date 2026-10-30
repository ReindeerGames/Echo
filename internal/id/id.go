package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

func New(prefix string) string {
	var raw [12]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(fmt.Sprintf("secure random identifier: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(raw[:])
}

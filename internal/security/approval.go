package security

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
)

func NewApprovalNonce() (plain, hash string, err error) {
	var raw [5]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	plain = strings.ToUpper(hex.EncodeToString(raw[:]))
	hash = HashNonce(plain)
	return plain, hash, nil
}

func HashNonce(value string) string {
	sum := sha256.Sum256([]byte(strings.ToUpper(strings.TrimSpace(value))))
	return hex.EncodeToString(sum[:])
}

func VerifyNonce(value, expectedHash string) bool {
	actual, errA := hex.DecodeString(HashNonce(value))
	expected, errB := hex.DecodeString(expectedHash)
	if errA != nil || errB != nil || len(actual) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func DescribeApproval(actionType, incidentID, nonce string) string {
	action := actionType
	if actionType == "merge_production" {
		action = "merge the exact approved pull-request commit and run the repository's existing production deployment workflow"
	}
	return fmt.Sprintf("Approval requested to %s for %s.\n\nTo authorize only this action, reply:\nAPPROVE %s\n\nTo decline it, reply:\nREJECT %s\n\nThis one-time code expires automatically.", action, incidentID, nonce, nonce)
}

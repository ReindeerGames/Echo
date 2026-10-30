package security

import (
	"strings"
	"testing"
)

func TestApprovalNonceRoundTrip(t *testing.T) {
	plain, hash, err := NewApprovalNonce()
	if err != nil {
		t.Fatal(err)
	}
	if len(plain) != 10 {
		t.Fatalf("unexpected nonce length %d", len(plain))
	}
	if !VerifyNonce(plain, hash) {
		t.Fatal("nonce should verify")
	}
	if VerifyNonce(plain+"0", hash) {
		t.Fatal("modified nonce must not verify")
	}
}

func TestDescribeApprovalIncludesExplicitReplyOptions(t *testing.T) {
	message := DescribeApproval("prepare_patch", "inc_test", "ABC123")
	for _, expected := range []string{"APPROVE ABC123", "REJECT ABC123", "one-time"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("approval message is missing %q: %s", expected, message)
		}
	}
}

func TestDescribeRepositoryProductionApprovalIsExplicit(t *testing.T) {
	message := DescribeApproval("merge_production", "inc_test", "ABC123")
	for _, expected := range []string{"merge the exact approved pull-request commit", "existing production deployment workflow", "APPROVE ABC123", "REJECT ABC123"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("production approval message is missing %q: %s", expected, message)
		}
	}
}

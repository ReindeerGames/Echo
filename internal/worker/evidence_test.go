package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectEvidencePrioritizesRelevantFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "lead-form.php"), []byte("function lead_form() { return 'blank'; }"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "other.php"), []byte("function other() {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := collectEvidence(root, "owner/repo", "lead form blank", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "lead-form.php") {
		t.Fatal("expected relevant file in evidence")
	}
}

func TestImmutableArtifact(t *testing.T) {
	if !immutableArtifact(strings.Repeat("a", 40)) {
		t.Fatal("commit SHA should be accepted")
	}
	if immutableArtifact("main") {
		t.Fatal("mutable ref must be rejected")
	}
}

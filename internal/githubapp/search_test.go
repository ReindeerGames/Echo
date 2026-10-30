package githubapp

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSearchRepositoryDirectoryReturnsRedactedBoundedEvidence(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "internal/auth/session.go", `package auth

const apiKey = "must-not-leak"

func ValidateSessionToken(token string) bool {
	return token != "" && strings.HasPrefix(token, "sess_")
}
`)
	writeSearchFixture(t, root, ".env", "SESSION_TOKEN=environment-secret\n")
	writeSearchFixture(t, root, "config/credentials.json", `{"token":"credential-secret"}`)
	writeSearchFixture(t, root, "config/secrets.yaml", "token: structured-secret\n")
	writeSearchFixture(t, root, "node_modules/auth/index.js", "function validateSessionToken() { return 'dependency-secret' }")

	query := "how are auth session tokens validated"
	result, err := searchRepositoryDirectory(context.Background(), root, "ReindeerGames/invoices", "main", query, repositorySearchTokens(query))
	if err != nil {
		t.Fatal(err)
	}
	if result.Repository != "ReindeerGames/invoices" || result.DefaultBranch != "main" || result.Query != query {
		t.Fatalf("unexpected result identity: %#v", result)
	}
	if len(result.Matches) != 1 || result.Matches[0].Path != "internal/auth/session.go" {
		t.Fatalf("unexpected search matches: %#v", result.Matches)
	}
	match := result.Matches[0]
	if match.LineStart < 1 || match.LineEnd < match.LineStart || !strings.Contains(match.Snippet, "ValidateSessionToken") || !strings.Contains(match.Snippet, `apiKey = "<redacted>"`) {
		t.Fatalf("missing useful redacted evidence: %#v", match)
	}
	for _, forbidden := range []string{"must-not-leak", "environment-secret", "credential-secret", "structured-secret", "dependency-secret", ".env", "node_modules"} {
		if strings.Contains(match.Snippet, forbidden) || strings.Contains(match.Path, forbidden) {
			t.Fatalf("search leaked excluded evidence %q: %#v", forbidden, match)
		}
	}
}

func TestSearchRepositoryDirectoryCapsMatches(t *testing.T) {
	root := t.TempDir()
	for index := 0; index < maxRepositorySearchMatches+5; index++ {
		writeSearchFixture(t, root, filepath.Join("internal", "auth", "handler"+strconv.Itoa(index)+".go"), "package auth\nfunc validateSessionToken() bool { return true }\n")
	}
	query := "validate session token"
	result, err := searchRepositoryDirectory(context.Background(), root, "Org/repo", "main", query, repositorySearchTokens(query))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != maxRepositorySearchMatches || !result.Truncated {
		t.Fatalf("unbounded matches: count=%d truncated=%v", len(result.Matches), result.Truncated)
	}
}

func TestRedactRepositoryLineCoversCommonSecretForms(t *testing.T) {
	input := `token := raw-secret password: hunter2 Authorization: Bearer abcdefghijkl github_pat_abcdefghijkl`
	redacted := redactRepositoryLine(input)
	for _, forbidden := range []string{"raw-secret", "hunter2", "abcdefghijkl"} {
		if strings.Contains(redacted, forbidden) {
			t.Fatalf("secret form %q was not redacted: %s", forbidden, redacted)
		}
	}
	if !strings.Contains(redacted, "<redacted>") {
		t.Fatalf("redaction marker missing: %s", redacted)
	}
}

func TestSearchRepositoryDownloadsConfiguredDefaultBranchAndRemovesWorkspace(t *testing.T) {
	temporaryRoot := t.TempDir()
	t.Setenv("TMPDIR", temporaryRoot)
	archive := repositoryArchive(t, map[string]string{
		"source/internal/auth/session.go": "package auth\nfunc validateSessionToken() bool { return true }\n",
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/ReindeerGames/invoices/tarball/main" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer github_pat_test" {
			t.Error("repository search omitted GitHub authentication")
		}
		w.Header().Set("Content-Type", "application/x-gzip")
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	client := newAuthenticatedTestClient(t, server)
	client.maxBytes = 1 << 20
	result, err := client.SearchRepository(context.Background(), testRepository(), "validate session token")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 1 || result.Matches[0].Path != "internal/auth/session.go" || !strings.Contains(result.Matches[0].Snippet, "validateSessionToken") {
		t.Fatalf("unexpected downloaded search result: %#v", result)
	}
	entries, err := os.ReadDir(temporaryRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("repository investigation workspace was retained: %#v", entries)
	}
}

func writeSearchFixture(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func repositoryArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

package githubapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/security"
	"github.com/ReindeerGames/echo/internal/store"
)

func TestValidateChangeProtectsControlFiles(t *testing.T) {
	bad := []string{"../secret", "/etc/passwd", ".github/workflows/deploy.yml", ".env", "package-lock.json", "config/secrets.json"}
	for _, path := range bad {
		if err := validateChange(FileChange{Path: path, Content: "x"}); err == nil {
			t.Errorf("expected %s to be rejected", path)
		}
	}
	if err := validateChange(FileChange{Path: "src/forms.php", Content: "<?php\n"}); err != nil {
		t.Fatalf("safe source path rejected: %v", err)
	}
}

func TestSafeJoinRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	if _, err := safeJoin(root, "../../escape"); err == nil {
		t.Fatal("expected traversal rejection")
	}
	if _, err := safeJoin(root, "src/app.go"); err != nil {
		t.Fatal(err)
	}
}

type tokenStore struct {
	value    []byte
	metadata json.RawMessage
}

func (s *tokenStore) SaveEncryptedSetting(_ context.Context, _ string, value []byte, metadata json.RawMessage) (time.Time, error) {
	s.value, s.metadata = append([]byte(nil), value...), append(json.RawMessage(nil), metadata...)
	return time.Unix(10, 0), nil
}
func (s *tokenStore) GetEncryptedSetting(_ context.Context, _ string) ([]byte, json.RawMessage, time.Time, error) {
	if s.value == nil {
		return nil, nil, time.Time{}, store.ErrNotFound
	}
	return s.value, s.metadata, time.Unix(10, 0), nil
}
func (s *tokenStore) DeleteEncryptedSetting(_ context.Context, _ string) error {
	s.value = nil
	return nil
}

func TestConfigureTokenValidatesAndStoresFineGrainedToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer github_pat_test" {
			t.Errorf("unexpected authorization header")
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"login": "echo-owner"})
	}))
	defer server.Close()

	backend := &tokenStore{}
	vault, err := security.NewVault(backend, "a-long-enough-test-encryption-key-for-github")
	if err != nil {
		t.Fatal(err)
	}
	client := New(vault, server.URL, 1024)
	client.http = server.Client()
	status, err := client.ConfigureToken(context.Background(), "github_pat_test")
	if err != nil || !status.Configured || status.Account != "echo-owner" {
		t.Fatalf("status=%#v err=%v", status, err)
	}
	stored, err := client.TokenStatus(context.Background())
	if err != nil || stored.Account != "echo-owner" {
		t.Fatalf("stored=%#v err=%v", stored, err)
	}
}

func TestConfigureTokenRejectsClassicToken(t *testing.T) {
	backend := &tokenStore{}
	vault, err := security.NewVault(backend, "a-long-enough-test-encryption-key-for-github")
	if err != nil {
		t.Fatal(err)
	}
	client := New(vault, "https://api.github.invalid", 1024)
	if _, err := client.ConfigureToken(context.Background(), "ghp_classic"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected invalid token error, got %v", err)
	}
}

func TestListAccessibleRepositoriesIsBoundedAndUsesTokenScope(t *testing.T) {
	var pages []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/repos" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer github_pat_test" {
			t.Errorf("authorization header missing")
		}
		if r.URL.Query().Get("affiliation") != "owner,collaborator,organization_member" || r.URL.Query().Get("per_page") != "50" || r.URL.Query().Get("sort") != "full_name" {
			t.Errorf("unsafe repository discovery query: %s", r.URL.RawQuery)
		}
		page := r.URL.Query().Get("page")
		pages = append(pages, page)
		if page == "1" {
			items := make([]map[string]any, 50)
			for index := range items {
				items[index] = map[string]any{"name": fmt.Sprintf("repo-%03d", index), "default_branch": "main", "private": true, "owner": map[string]string{"login": "ReindeerGames"}, "permissions": map[string]bool{"push": false}}
			}
			writeJSON(t, w, items)
			return
		}
		writeJSON(t, w, []map[string]any{{"name": "shared", "default_branch": "develop", "private": false, "archived": true, "fork": true, "owner": map[string]string{"login": "Partner"}, "permissions": map[string]bool{"maintain": true}}})
	}))
	defer server.Close()

	client := newAuthenticatedTestClient(t, server)
	discovery, err := client.ListAccessibleRepositories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Repositories) != 51 || discovery.Truncated || strings.Join(pages, ",") != "1,2" {
		t.Fatalf("unexpected discovery: count=%d truncated=%v pages=%v", len(discovery.Repositories), discovery.Truncated, pages)
	}
	last := discovery.Repositories[50]
	if last.FullName != "Partner/shared" || last.DefaultBranch != "develop" || !last.Archived || !last.Fork || !last.CanWrite {
		t.Fatalf("unexpected discovered repository: %#v", last)
	}
}

func TestListAccessibleRepositoriesStopsAtFiveHundred(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		items := make([]map[string]any, 50)
		for index := range items {
			items[index] = map[string]any{"name": fmt.Sprintf("repo-%s-%02d", r.URL.Query().Get("page"), index), "default_branch": "main", "owner": map[string]string{"login": "LargeOrg"}}
		}
		writeJSON(t, w, items)
	}))
	defer server.Close()

	discovery, err := newAuthenticatedTestClient(t, server).ListAccessibleRepositories(context.Background())
	if err != nil || len(discovery.Repositories) != 500 || !discovery.Truncated || requests != 10 {
		t.Fatalf("unbounded discovery: count=%d truncated=%v requests=%d err=%v", len(discovery.Repositories), discovery.Truncated, requests, err)
	}
}

func newAuthenticatedTestClient(t *testing.T, server *httptest.Server) *Client {
	t.Helper()
	backend := &tokenStore{}
	vault, err := security.NewVault(backend, "a-long-enough-test-encryption-key-for-github")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Set(context.Background(), tokenSetting, "github_pat_test", map[string]string{"account": "echo-owner"}); err != nil {
		t.Fatal(err)
	}
	client := New(vault, server.URL, 1024)
	client.http = server.Client()
	client.pollInterval = time.Millisecond
	client.workflowTimeout = 50 * time.Millisecond
	return client
}

func testRepository() domain.Repository {
	return domain.Repository{Owner: "ReindeerGames", Name: "invoices", DefaultBranch: "main", Enabled: true}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatal(err)
	}
}

func TestProductionCandidateRequiresExactHeadAndSuccessfulChecks(t *testing.T) {
	const head = "a75cdbdaad7917a104299753c32f8358b90a50e6"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer github_pat_test" {
			t.Errorf("authorization header missing")
		}
		switch r.URL.Path {
		case "/repos/ReindeerGames/invoices/pulls/1":
			writeJSON(t, w, map[string]any{
				"number": 1, "html_url": "https://github.test/pr/1", "state": "open", "draft": true,
				"head": map[string]string{"sha": head}, "base": map[string]string{"ref": "main"},
			})
		case "/repos/ReindeerGames/invoices/actions/runs":
			if r.URL.Query().Get("head_sha") != head {
				t.Errorf("exact head filter missing: %s", r.URL.RawQuery)
			}
			writeJSON(t, w, map[string]any{"workflow_runs": []map[string]string{
				{"name": "test", "head_sha": head, "status": "completed", "conclusion": "success"},
				{"name": "lint", "head_sha": head, "status": "completed", "conclusion": "success"},
			}})
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newAuthenticatedTestClient(t, server)
	candidate, err := client.ProductionCandidate(context.Background(), testRepository(), 1, head)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.HeadSHA != head || candidate.BaseBranch != "main" || candidate.Checks != 2 || !candidate.Draft {
		t.Fatalf("unexpected candidate: %#v", candidate)
	}
	if _, err := client.ProductionCandidate(context.Background(), testRepository(), 1, strings.Repeat("b", 40)); err == nil || !strings.Contains(err.Error(), "head changed") {
		t.Fatalf("expected changed head rejection, got %v", err)
	}
}

func TestProductionCandidateRejectsPendingOrFailedCheck(t *testing.T) {
	const head = "a75cdbdaad7917a104299753c32f8358b90a50e6"
	for _, check := range []map[string]string{
		{"name": "test", "status": "in_progress", "conclusion": ""},
		{"name": "test", "status": "completed", "conclusion": "failure"},
	} {
		t.Run(check["status"]+check["conclusion"], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/pulls/1"):
					writeJSON(t, w, map[string]any{"number": 1, "state": "open", "head": map[string]string{"sha": head}, "base": map[string]string{"ref": "main"}})
				case strings.HasSuffix(r.URL.Path, "/actions/runs"):
					check["head_sha"] = head
					writeJSON(t, w, map[string]any{"workflow_runs": []map[string]string{check}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			client := newAuthenticatedTestClient(t, server)
			if _, err := client.ProductionCandidate(context.Background(), testRepository(), 1, head); err == nil {
				t.Fatal("expected unsafe check state to be rejected")
			}
		})
	}
}

func TestMergePullRequestMarksDraftReadyAndBindsExactSHA(t *testing.T) {
	const head = "a75cdbdaad7917a104299753c32f8358b90a50e6"
	var pullReads, readyCalls, mergeCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/ReindeerGames/invoices/pulls/1":
			pullReads++
			writeJSON(t, w, map[string]any{
				"number": 1, "node_id": "PR_node", "state": "open", "draft": true,
				"head": map[string]string{"sha": head}, "base": map[string]string{"ref": "main"},
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/actions/runs"):
			writeJSON(t, w, map[string]any{"workflow_runs": []map[string]string{{"name": "CI", "head_sha": head, "status": "completed", "conclusion": "success"}}})
		case r.Method == http.MethodPost && r.URL.Path == "/graphql":
			readyCalls++
			writeJSON(t, w, map[string]any{"data": map[string]any{"markPullRequestReadyForReview": map[string]any{"pullRequest": map[string]bool{"isDraft": false}}}})
		case r.Method == http.MethodPut && r.URL.Path == "/repos/ReindeerGames/invoices/pulls/1/merge":
			mergeCalls++
			var input map[string]any
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input["sha"] != head || input["merge_method"] != "squash" {
				t.Errorf("merge was not exact-SHA bound: %#v", input)
			}
			writeJSON(t, w, map[string]any{"sha": strings.Repeat("c", 40), "merged": true})
		default:
			http.Error(w, fmt.Sprintf("unexpected %s %s", r.Method, r.URL.Path), http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := newAuthenticatedTestClient(t, server)
	mergeSHA, err := client.MergePullRequest(context.Background(), testRepository(), 1, head)
	if err != nil {
		t.Fatal(err)
	}
	if mergeSHA != strings.Repeat("c", 40) || pullReads != 2 || readyCalls != 1 || mergeCalls != 1 {
		t.Fatalf("mergeSHA=%s pullReads=%d readyCalls=%d mergeCalls=%d", mergeSHA, pullReads, readyCalls, mergeCalls)
	}
}

func TestWaitForCommitWorkflowsReturnsVerifiedAndFailedResults(t *testing.T) {
	const commit = "cccccccccccccccccccccccccccccccccccccccc"
	for _, conclusion := range []string{"success", "failure"} {
		t.Run(conclusion, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/ReindeerGames/invoices/actions/runs" || r.URL.Query().Get("event") != "push" || r.URL.Query().Get("head_sha") != commit {
					http.Error(w, "unexpected workflow query", http.StatusBadRequest)
					return
				}
				writeJSON(t, w, map[string]any{"workflow_runs": []map[string]any{
					{"id": 42, "name": "Build and deploy", "head_sha": commit, "head_branch": "main", "status": "completed", "conclusion": conclusion, "html_url": "https://github.test/actions/42"},
					{"id": 43, "name": "Unrelated", "head_sha": commit, "head_branch": "other", "status": "completed", "conclusion": "failure"},
				}})
			}))
			defer server.Close()
			client := newAuthenticatedTestClient(t, server)
			result, err := client.WaitForCommitWorkflows(context.Background(), testRepository(), commit)
			if err != nil {
				t.Fatal(err)
			}
			if result.Success != (conclusion == "success") || result.RunID != 42 || result.URL == "" || len(result.Workflows) != 1 {
				t.Fatalf("unexpected workflow result: %#v", result)
			}
		})
	}
}

func TestInspectRepositoryReturnsBoundedDefaultBranchWorkflowState(t *testing.T) {
	const head = "a75cdbdaad7917a104299753c32f8358b90a50e6"
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/ReindeerGames/invoices/commits/main":
			writeJSON(t, w, map[string]any{"sha": head, "commit": map[string]any{"author": map[string]any{"date": now}}})
		case "/repos/ReindeerGames/invoices/actions/runs":
			if r.URL.Query().Get("branch") != "main" || r.URL.Query().Get("per_page") != "10" {
				t.Errorf("unexpected workflow query: %s", r.URL.RawQuery)
			}
			writeJSON(t, w, map[string]any{"workflow_runs": []map[string]any{
				{"name": "Build and deploy", "event": "push", "status": "completed", "conclusion": "success", "head_sha": head, "head_branch": "main", "created_at": now, "updated_at": now.Add(time.Minute), "html_url": "https://github.test/actions/42"},
				{"name": "Wrong branch", "event": "push", "status": "completed", "conclusion": "failure", "head_sha": "other", "head_branch": "develop", "created_at": now, "updated_at": now},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	status, err := newAuthenticatedTestClient(t, server).InspectRepository(context.Background(), testRepository())
	if err != nil {
		t.Fatal(err)
	}
	if status.Repository != "ReindeerGames/invoices" || status.HeadSHA != head || !status.HeadCommittedAt.Equal(now) || len(status.WorkflowRuns) != 1 || status.WorkflowRuns[0].Conclusion != "success" {
		t.Fatalf("unexpected repository status: %#v", status)
	}
}

func TestInspectRepositoryRejectsDisabledRepositoryWithoutGitHubCall(t *testing.T) {
	client := New(nil, "https://api.github.invalid", 1024)
	if _, err := client.InspectRepository(context.Background(), domain.Repository{}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled repository rejection, got %v", err)
	}
}

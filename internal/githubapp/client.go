package githubapp

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/security"
)

type FileChange struct {
	Path    string
	Content string
}
type PullRequest struct {
	Number    int64
	URL       string
	CommitSHA string
	Branch    string
}

type ProductionCandidate struct {
	Number     int64  `json:"number"`
	URL        string `json:"url"`
	HeadSHA    string `json:"headSha"`
	BaseBranch string `json:"baseBranch"`
	Draft      bool   `json:"draft"`
	Checks     int    `json:"checks"`
}

type WorkflowResult struct {
	Success   bool
	RunID     int64
	URL       string
	Workflows []string
}

type Client struct {
	vault           *security.Vault
	baseURL         string
	http            *http.Client
	maxBytes        int64
	pollInterval    time.Duration
	workflowTimeout time.Duration
}

type TokenStatus struct {
	Configured bool      `json:"configured"`
	Account    string    `json:"account,omitempty"`
	UpdatedAt  time.Time `json:"updatedAt,omitempty"`
}

const tokenSetting = "github.token"

var ErrInvalidToken = errors.New("invalid GitHub token")

func New(vault *security.Vault, baseURL string, maxBytes int64) *Client {
	return &Client{vault: vault, baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: 2 * time.Minute}, maxBytes: maxBytes, pollInterval: 5 * time.Second, workflowTimeout: 30 * time.Minute}
}

func (c *Client) ConfigureToken(ctx context.Context, token string) (TokenStatus, error) {
	token = strings.TrimSpace(token)
	if !strings.HasPrefix(token, "github_pat_") {
		return TokenStatus{}, fmt.Errorf("%w: use a fine-grained token beginning with github_pat_", ErrInvalidToken)
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := c.doJSON(ctx, token, http.MethodGet, "/user", nil, &user); err != nil {
		return TokenStatus{}, fmt.Errorf("%w: GitHub rejected the token", ErrInvalidToken)
	}
	if strings.TrimSpace(user.Login) == "" {
		return TokenStatus{}, fmt.Errorf("%w: GitHub did not return an account", ErrInvalidToken)
	}
	updated, err := c.vault.Set(ctx, tokenSetting, token, map[string]string{"account": user.Login, "type": "fine_grained"})
	if err != nil {
		return TokenStatus{}, err
	}
	return TokenStatus{Configured: true, Account: user.Login, UpdatedAt: updated}, nil
}

func (c *Client) TokenStatus(ctx context.Context) (TokenStatus, error) {
	_, metadata, updated, err := c.vault.Get(ctx, tokenSetting)
	if errors.Is(err, security.ErrSecretNotFound) {
		return TokenStatus{}, nil
	}
	if err != nil {
		return TokenStatus{}, err
	}
	var data struct {
		Account string `json:"account"`
	}
	if err := json.Unmarshal(metadata, &data); err != nil {
		return TokenStatus{}, err
	}
	return TokenStatus{Configured: true, Account: data.Account, UpdatedAt: updated}, nil
}

func (c *Client) RemoveToken(ctx context.Context) error {
	if err := c.vault.Delete(ctx, tokenSetting); errors.Is(err, security.ErrSecretNotFound) {
		return nil
	} else {
		return err
	}
}

func (c *Client) token(ctx context.Context) (string, error) {
	token, _, _, err := c.vault.Get(ctx, tokenSetting)
	if errors.Is(err, security.ErrSecretNotFound) {
		return "", errors.New("GitHub token is not configured in application settings")
	}
	return token, err
}

func (c *Client) FetchRepository(ctx context.Context, repo domain.Repository, destination string) error {
	token, err := c.token(ctx)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("%s/repos/%s/%s/tarball/%s", c.baseURL, url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(repo.DefaultBranch))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("download repository archive: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	limited := io.LimitReader(resp.Body, c.maxBytes+1)
	gz, err := gzip.NewReader(limited)
	if err != nil {
		return fmt.Errorf("open repository archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var extracted int64
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(header.Name), "/")
		if len(parts) < 2 {
			continue
		}
		rel := filepath.FromSlash(strings.Join(parts[1:], "/"))
		target, err := safeJoin(destination, rel)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if header.Size < 0 || extracted+header.Size > c.maxBytes {
				return errors.New("repository archive exceeds configured extraction limit")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyN(file, tr, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			extracted += header.Size
		}
	}
	return nil
}

func (c *Client) CreatePullRequest(ctx context.Context, repo domain.Repository, branch, title, body string, changes []FileChange) (PullRequest, error) {
	if repo.ReadOnly {
		return PullRequest{}, errors.New("repository is configured read-only")
	}
	if len(changes) == 0 || len(changes) > 8 {
		return PullRequest{}, errors.New("patch must contain one to eight files")
	}
	for _, change := range changes {
		if err := validateChange(change); err != nil {
			return PullRequest{}, err
		}
	}
	token, err := c.token(ctx)
	if err != nil {
		return PullRequest{}, err
	}
	prefix := fmt.Sprintf("/repos/%s/%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	var ref struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	if err := c.doJSON(ctx, token, http.MethodGet, prefix+"/git/ref/heads/"+url.PathEscape(repo.DefaultBranch), nil, &ref); err != nil {
		return PullRequest{}, err
	}
	var commit struct {
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := c.doJSON(ctx, token, http.MethodGet, prefix+"/git/commits/"+ref.Object.SHA, nil, &commit); err != nil {
		return PullRequest{}, err
	}
	treeEntries := make([]map[string]any, 0, len(changes))
	for _, change := range changes {
		var blob struct {
			SHA string `json:"sha"`
		}
		if err := c.doJSON(ctx, token, http.MethodPost, prefix+"/git/blobs", map[string]any{"content": change.Content, "encoding": "utf-8"}, &blob); err != nil {
			return PullRequest{}, err
		}
		treeEntries = append(treeEntries, map[string]any{"path": change.Path, "mode": "100644", "type": "blob", "sha": blob.SHA})
	}
	var tree struct {
		SHA string `json:"sha"`
	}
	if err := c.doJSON(ctx, token, http.MethodPost, prefix+"/git/trees", map[string]any{"base_tree": commit.Tree.SHA, "tree": treeEntries}, &tree); err != nil {
		return PullRequest{}, err
	}
	var newCommit struct {
		SHA string `json:"sha"`
	}
	if err := c.doJSON(ctx, token, http.MethodPost, prefix+"/git/commits", map[string]any{"message": title, "tree": tree.SHA, "parents": []string{ref.Object.SHA}}, &newCommit); err != nil {
		return PullRequest{}, err
	}
	if err := c.doJSON(ctx, token, http.MethodPost, prefix+"/git/refs", map[string]any{"ref": "refs/heads/" + branch, "sha": newCommit.SHA}, nil); err != nil {
		return PullRequest{}, err
	}
	var pr struct {
		Number  int64  `json:"number"`
		HTMLURL string `json:"html_url"`
	}
	if err := c.doJSON(ctx, token, http.MethodPost, prefix+"/pulls", map[string]any{"title": title, "head": branch, "base": repo.DefaultBranch, "body": body, "draft": true}, &pr); err != nil {
		return PullRequest{}, err
	}
	return PullRequest{Number: pr.Number, URL: pr.HTMLURL, CommitSHA: newCommit.SHA, Branch: branch}, nil
}

func (c *Client) DispatchWorkflow(ctx context.Context, repo domain.Repository, workflow, ref string, inputs map[string]string) error {
	token, err := c.token(ctx)
	if err != nil {
		return err
	}
	endpoint := fmt.Sprintf("/repos/%s/%s/actions/workflows/%s/dispatches", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.PathEscape(workflow))
	return c.doJSON(ctx, token, http.MethodPost, endpoint, map[string]any{"ref": ref, "inputs": inputs}, nil)
}

type pullRequestState struct {
	Number         int64  `json:"number"`
	HTMLURL        string `json:"html_url"`
	NodeID         string `json:"node_id"`
	State          string `json:"state"`
	Draft          bool   `json:"draft"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	Head           struct {
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (c *Client) ProductionCandidate(ctx context.Context, repo domain.Repository, number int64, expectedHead string) (ProductionCandidate, error) {
	token, err := c.token(ctx)
	if err != nil {
		return ProductionCandidate{}, err
	}
	pr, err := c.getPullRequest(ctx, token, repo, number)
	if err != nil {
		return ProductionCandidate{}, err
	}
	if pr.State != "open" || pr.Merged {
		return ProductionCandidate{}, errors.New("pull request is not open")
	}
	if pr.Head.SHA != expectedHead {
		return ProductionCandidate{}, errors.New("pull request head changed after Echo prepared the approved change")
	}
	if pr.Base.Ref != repo.DefaultBranch {
		return ProductionCandidate{}, errors.New("pull request base does not match the configured default branch")
	}
	prefix := fmt.Sprintf("/repos/%s/%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	var workflows struct {
		Runs []struct {
			Name       string `json:"name"`
			HeadSHA    string `json:"head_sha"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
		} `json:"workflow_runs"`
	}
	if err := c.doJSON(ctx, token, http.MethodGet, prefix+"/actions/runs?head_sha="+url.QueryEscape(expectedHead)+"&per_page=100", nil, &workflows); err != nil {
		return ProductionCandidate{}, err
	}
	matching := workflows.Runs[:0]
	for _, run := range workflows.Runs {
		if run.HeadSHA == expectedHead {
			matching = append(matching, run)
		}
	}
	if len(matching) == 0 {
		return ProductionCandidate{}, errors.New("the approved commit has no GitHub Actions workflow results")
	}
	for _, run := range matching {
		if run.Status != "completed" {
			return ProductionCandidate{}, fmt.Errorf("GitHub workflow %q is still %s", run.Name, run.Status)
		}
		if run.Conclusion != "success" {
			return ProductionCandidate{}, fmt.Errorf("GitHub workflow %q concluded %s", run.Name, run.Conclusion)
		}
	}
	return ProductionCandidate{Number: pr.Number, URL: pr.HTMLURL, HeadSHA: pr.Head.SHA, BaseBranch: pr.Base.Ref, Draft: pr.Draft, Checks: len(matching)}, nil
}

func (c *Client) MergePullRequest(ctx context.Context, repo domain.Repository, number int64, expectedHead string) (string, error) {
	token, err := c.token(ctx)
	if err != nil {
		return "", err
	}
	pr, err := c.getPullRequest(ctx, token, repo, number)
	if err != nil {
		return "", err
	}
	if pr.Head.SHA != expectedHead || pr.Base.Ref != repo.DefaultBranch {
		return "", errors.New("pull request no longer matches the approved head and base")
	}
	if pr.Merged {
		if pr.MergeCommitSHA == "" {
			return "", errors.New("merged pull request has no merge commit SHA")
		}
		return pr.MergeCommitSHA, nil
	}
	if _, err := c.ProductionCandidate(ctx, repo, number, expectedHead); err != nil {
		return "", err
	}
	if pr.Draft {
		var ready struct {
			Data struct {
				Mark struct {
					PullRequest struct {
						Draft bool `json:"isDraft"`
					} `json:"pullRequest"`
				} `json:"markPullRequestReadyForReview"`
			} `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		input := map[string]any{
			"query":     "mutation($id:ID!){markPullRequestReadyForReview(input:{pullRequestId:$id}){pullRequest{isDraft}}}",
			"variables": map[string]string{"id": pr.NodeID},
		}
		if err := c.doJSON(ctx, token, http.MethodPost, "/graphql", input, &ready); err != nil {
			return "", err
		}
		if len(ready.Errors) > 0 {
			return "", fmt.Errorf("GitHub could not mark the draft ready: %s", ready.Errors[0].Message)
		}
		if ready.Data.Mark.PullRequest.Draft {
			return "", errors.New("pull request remained a draft")
		}
	}
	prefix := fmt.Sprintf("/repos/%s/%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	var merged struct {
		SHA     string `json:"sha"`
		Merged  bool   `json:"merged"`
		Message string `json:"message"`
	}
	if err := c.doJSON(ctx, token, http.MethodPut, fmt.Sprintf("%s/pulls/%d/merge", prefix, number), map[string]any{"sha": expectedHead, "merge_method": "squash"}, &merged); err != nil {
		return "", err
	}
	if !merged.Merged || merged.SHA == "" {
		return "", fmt.Errorf("GitHub did not merge the pull request: %s", merged.Message)
	}
	return merged.SHA, nil
}

func (c *Client) WaitForCommitWorkflows(ctx context.Context, repo domain.Repository, commitSHA string) (WorkflowResult, error) {
	token, err := c.token(ctx)
	if err != nil {
		return WorkflowResult{}, err
	}
	deadline := time.Now().Add(c.workflowTimeout)
	prefix := fmt.Sprintf("/repos/%s/%s/actions/runs?event=push&head_sha=%s&per_page=100", url.PathEscape(repo.Owner), url.PathEscape(repo.Name), url.QueryEscape(commitSHA))
	for {
		var response struct {
			Runs []struct {
				ID         int64  `json:"id"`
				Name       string `json:"name"`
				HeadSHA    string `json:"head_sha"`
				HeadBranch string `json:"head_branch"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
				HTMLURL    string `json:"html_url"`
			} `json:"workflow_runs"`
		}
		if err := c.doJSON(ctx, token, http.MethodGet, prefix, nil, &response); err != nil {
			return WorkflowResult{}, err
		}
		matching := response.Runs[:0]
		for _, run := range response.Runs {
			if run.HeadSHA == commitSHA && run.HeadBranch == repo.DefaultBranch {
				matching = append(matching, run)
			}
		}
		if len(matching) > 0 {
			result := WorkflowResult{Success: true, RunID: matching[0].ID, URL: matching[0].HTMLURL}
			complete := true
			for _, run := range matching {
				result.Workflows = append(result.Workflows, run.Name)
				if run.Status != "completed" {
					complete = false
					continue
				}
				if run.Conclusion != "success" {
					result.Success = false
				}
			}
			if complete {
				return result, nil
			}
		}
		if time.Now().After(deadline) {
			return WorkflowResult{}, errors.New("timed out waiting for the repository's main-branch workflows")
		}
		timer := time.NewTimer(c.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return WorkflowResult{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) getPullRequest(ctx context.Context, token string, repo domain.Repository, number int64) (pullRequestState, error) {
	var pr pullRequestState
	prefix := fmt.Sprintf("/repos/%s/%s", url.PathEscape(repo.Owner), url.PathEscape(repo.Name))
	err := c.doJSON(ctx, token, http.MethodGet, fmt.Sprintf("%s/pulls/%d", prefix, number), nil, &pr)
	return pr, err
}

func (c *Client) doJSON(ctx context.Context, token, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		raw, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = strings.NewReader(string(raw))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiError(resp)
	}
	if output == nil || resp.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(output)
}
func validateChange(change FileChange) error {
	path := filepath.ToSlash(filepath.Clean(change.Path))
	if path == "." || strings.HasPrefix(path, "../") || strings.HasPrefix(path, "/") || strings.Contains(path, "/.git/") || path == ".git" {
		return fmt.Errorf("unsafe patch path %q", change.Path)
	}
	lower := strings.ToLower(path)
	if strings.Contains(lower, ".github/workflows/") || strings.HasSuffix(lower, "package-lock.json") || strings.HasSuffix(lower, "go.sum") || strings.Contains(lower, "secret") || strings.HasSuffix(lower, ".env") {
		return fmt.Errorf("patch path %q is protected", change.Path)
	}
	if len(change.Content) > 1<<20 {
		return fmt.Errorf("patch content for %q exceeds 1 MiB", change.Path)
	}
	return nil
}

func safeJoin(root, rel string) (string, error) {
	target := filepath.Join(root, rel)
	cleanRoot := filepath.Clean(root) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), cleanRoot) {
		return "", errors.New("repository archive contains an unsafe path")
	}
	return target, nil
}
func apiError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("GitHub API status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
}

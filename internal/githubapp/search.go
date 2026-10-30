package githubapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ReindeerGames/echo/internal/domain"
)

const (
	maxRepositorySearchCandidates = 5000
	maxRepositorySearchEntries    = 20000
	maxRepositorySearchFiles      = 200
	maxRepositorySearchBytes      = 8 << 20
	maxRepositorySearchFileBytes  = 128 << 10
	maxRepositorySearchMatches    = 12
	maxRepositorySnippetRunes     = 1200
)

type RepositorySearchMatch struct {
	Path      string `json:"path"`
	LineStart int    `json:"lineStart"`
	LineEnd   int    `json:"lineEnd"`
	Snippet   string `json:"snippet"`
	score     int
}

type RepositorySearchResult struct {
	Repository      string                  `json:"repository"`
	DefaultBranch   string                  `json:"defaultBranch"`
	Query           string                  `json:"query"`
	Matches         []RepositorySearchMatch `json:"matches"`
	FilesConsidered int                     `json:"filesConsidered"`
	FilesScanned    int                     `json:"filesScanned"`
	BytesScanned    int64                   `json:"bytesScanned"`
	Truncated       bool                    `json:"truncated"`
}

type repositorySearchCandidate struct {
	path      string
	relative  string
	size      int64
	pathScore int
}

var secretAssignment = regexp.MustCompile(`(?i)(password|passwd|secret|token|api[_-]?key|private[_-]?key)(\s*[:=]\s*)["'][^"']+["']`)
var unquotedSecretAssignment = regexp.MustCompile(`(?i)\b(password|passwd|secret|token|api[_-]?key|private[_-]?key)(\s*(?::=|=|:)\s*)([^\s"',;#}]+)`)
var secretLiteral = regexp.MustCompile(`(?i)(github_pat_|gh[pousr]_|sk-)[A-Za-z0-9_-]{8,}`)
var bearerLiteral = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/-]{8,}`)

var errRepositorySearchWalkLimit = errors.New("repository search walk limit reached")

func (c *Client) SearchRepository(ctx context.Context, repo domain.Repository, query string) (RepositorySearchResult, error) {
	if !repo.Enabled {
		return RepositorySearchResult{}, errors.New("repository is disabled")
	}
	query = strings.TrimSpace(query)
	if len([]rune(query)) < 3 || len([]rune(query)) > 240 {
		return RepositorySearchResult{}, errors.New("repository investigation query must contain 3 to 240 characters")
	}
	tokens := repositorySearchTokens(query)
	if len(tokens) == 0 {
		return RepositorySearchResult{}, errors.New("repository investigation query needs at least one specific search term")
	}
	workspace, err := os.MkdirTemp("", "echo-repository-investigation-*")
	if err != nil {
		return RepositorySearchResult{}, err
	}
	defer os.RemoveAll(workspace)
	if err := c.FetchRepository(ctx, repo, workspace); err != nil {
		return RepositorySearchResult{}, err
	}
	result, err := searchRepositoryDirectory(ctx, workspace, repo.Owner+"/"+repo.Name, repo.DefaultBranch, query, tokens)
	if err != nil {
		return RepositorySearchResult{}, err
	}
	return result, nil
}

func searchRepositoryDirectory(ctx context.Context, root, repository, branch, query string, tokens []string) (RepositorySearchResult, error) {
	result := RepositorySearchResult{Repository: repository, DefaultBranch: branch, Query: query, Matches: make([]RepositorySearchMatch, 0)}
	candidates := make([]repositorySearchCandidate, 0)
	entries := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		entries++
		if entries > maxRepositorySearchEntries {
			result.Truncated = true
			return errRepositorySearchWalkLimit
		}
		if walkErr != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if entry.IsDir() {
			if path != root && ignoredRepositoryDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if len(candidates) == maxRepositorySearchCandidates {
			result.Truncated = true
			return errRepositorySearchWalkLimit
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maxRepositorySearchFileBytes {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if !searchableRepositoryPath(relative) || sensitiveRepositoryPath(relative) {
			return nil
		}
		candidates = append(candidates, repositorySearchCandidate{path: path, relative: relative, size: info.Size(), pathScore: tokenScore(strings.ToLower(relative), tokens)})
		return nil
	})
	if errors.Is(err, errRepositorySearchWalkLimit) {
		err = nil
	}
	if err != nil {
		return RepositorySearchResult{}, err
	}
	result.FilesConsidered = len(candidates)
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].pathScore != candidates[j].pathScore {
			return candidates[i].pathScore > candidates[j].pathScore
		}
		if candidates[i].size != candidates[j].size {
			return candidates[i].size < candidates[j].size
		}
		return candidates[i].relative < candidates[j].relative
	})
	for _, candidate := range candidates {
		if result.FilesScanned == maxRepositorySearchFiles || result.BytesScanned+candidate.size > maxRepositorySearchBytes {
			result.Truncated = true
			break
		}
		select {
		case <-ctx.Done():
			return RepositorySearchResult{}, ctx.Err()
		default:
		}
		raw, err := os.ReadFile(candidate.path)
		if err != nil || !utf8.Valid(raw) || containsNUL(raw) {
			continue
		}
		result.FilesScanned++
		result.BytesScanned += int64(len(raw))
		match, ok := repositoryFileMatch(candidate, string(raw), tokens)
		if ok {
			result.Matches = append(result.Matches, match)
		}
	}
	sort.Slice(result.Matches, func(i, j int) bool {
		if result.Matches[i].score != result.Matches[j].score {
			return result.Matches[i].score > result.Matches[j].score
		}
		return result.Matches[i].Path < result.Matches[j].Path
	})
	if len(result.Matches) > maxRepositorySearchMatches {
		result.Matches = result.Matches[:maxRepositorySearchMatches]
		result.Truncated = true
	}
	return result, nil
}

func repositoryFileMatch(candidate repositorySearchCandidate, content string, tokens []string) (RepositorySearchMatch, bool) {
	lines := strings.Split(content, "\n")
	bestLine, bestLineScore, contentScore := -1, 0, 0
	for index, line := range lines {
		score := tokenScore(strings.ToLower(line), tokens)
		contentScore += score
		if score > bestLineScore {
			bestLine, bestLineScore = index, score
		}
	}
	if bestLine < 0 {
		if candidate.pathScore == 0 {
			return RepositorySearchMatch{}, false
		}
		bestLine = 0
	}
	start := max(0, bestLine-2)
	end := min(len(lines), bestLine+3)
	var snippet strings.Builder
	for index := start; index < end; index++ {
		line := redactRepositoryLine(strings.TrimRight(lines[index], "\r"))
		fmt.Fprintf(&snippet, "%d: %s\n", index+1, truncateRepositoryRunes(line, 320))
	}
	return RepositorySearchMatch{Path: candidate.relative, LineStart: start + 1, LineEnd: end, Snippet: truncateRepositoryRunes(strings.TrimSpace(snippet.String()), maxRepositorySnippetRunes), score: candidate.pathScore*20 + min(contentScore, 100)}, true
}

func repositorySearchTokens(query string) []string {
	stop := map[string]bool{"about": true, "does": true, "from": true, "have": true, "how": true, "into": true, "that": true, "the": true, "this": true, "what": true, "when": true, "where": true, "which": true, "with": true, "work": true, "works": true}
	seen := map[string]bool{}
	var tokens []string
	for _, token := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' }) {
		token = strings.TrimSpace(token)
		if len([]rune(token)) < 3 || stop[token] || seen[token] {
			continue
		}
		seen[token] = true
		tokens = append(tokens, token)
		if len(tokens) == 12 {
			break
		}
	}
	return tokens
}

func tokenScore(value string, tokens []string) int {
	score := 0
	for _, token := range tokens {
		if repositoryTokenMatches(value, token) {
			score++
		}
	}
	return score
}

func repositoryTokenMatches(value, token string) bool {
	if strings.Contains(value, token) {
		return true
	}
	if len(token) > 4 && strings.HasSuffix(token, "s") && strings.Contains(value, strings.TrimSuffix(token, "s")) {
		return true
	}
	if len(token) > 5 && strings.HasSuffix(token, "ed") {
		stem := strings.TrimSuffix(token, "ed")
		return strings.Contains(value, stem) || strings.Contains(value, stem+"e")
	}
	return false
}

func ignoredRepositoryDirectory(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case ".git", ".idea", ".next", ".nuxt", ".terraform", ".venv", "build", "coverage", "dist", "node_modules", "target", "tmp", "vendor":
		return true
	default:
		return false
	}
}

func searchableRepositoryPath(path string) bool {
	extension := strings.ToLower(filepath.Ext(path))
	switch extension {
	case ".c", ".cc", ".cpp", ".cs", ".css", ".go", ".graphql", ".h", ".html", ".java", ".js", ".json", ".jsx", ".kt", ".md", ".php", ".proto", ".py", ".rb", ".rs", ".scss", ".sh", ".sql", ".swift", ".toml", ".ts", ".tsx", ".txt", ".xml", ".yaml", ".yml":
		return true
	}
	base := strings.ToLower(filepath.Base(path))
	return base == "dockerfile" || base == "gemfile" || base == "makefile" || strings.HasPrefix(base, "readme")
}

func sensitiveRepositoryPath(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	extension := strings.ToLower(filepath.Ext(path))
	if base == ".env" || strings.HasPrefix(base, ".env.") || base == ".dockercfg" || base == ".npmrc" || base == ".pypirc" || base == "id_rsa" || base == "id_ed25519" {
		return true
	}
	switch extension {
	case ".jks", ".key", ".p12", ".pem", ".pfx":
		return true
	}
	structuredSecret := extension == ".json" || extension == ".toml" || extension == ".yaml" || extension == ".yml"
	return structuredSecret && (strings.Contains(base, "credentials") || strings.Contains(base, "secrets"))
}

func redactRepositoryLine(line string) string {
	if strings.Contains(strings.ToUpper(line), "BEGIN ") && strings.Contains(strings.ToUpper(line), "PRIVATE KEY") {
		return "<redacted private key material>"
	}
	line = secretAssignment.ReplaceAllString(line, `$1$2"<redacted>"`)
	line = unquotedSecretAssignment.ReplaceAllString(line, `$1$2<redacted>`)
	line = secretLiteral.ReplaceAllString(line, "<redacted token>")
	line = bearerLiteral.ReplaceAllString(line, "Bearer <redacted>")
	return line
}

func containsNUL(value []byte) bool {
	for _, current := range value {
		if current == 0 {
			return true
		}
	}
	return false
}

func truncateRepositoryRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

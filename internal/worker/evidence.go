package worker

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

type evidenceFile struct {
	path  string
	score int64
	size  int64
}

func collectEvidence(root, repository, incident string, maxFileBytes int64) (string, error) {
	tokens := keywords(incident)
	var files []evidenceFile
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if entry.IsDir() {
			name := entry.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || name == "dist" || name == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > maxFileBytes || info.Size() == 0 {
			return nil
		}
		if !supported(path) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		score := int64(1)
		lower := strings.ToLower(rel)
		for _, token := range tokens {
			if strings.Contains(lower, token) {
				score += 10
			}
		}
		files = append(files, evidenceFile{path: path, score: score, size: info.Size()})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].score == files[j].score {
			return files[i].size < files[j].size
		}
		return files[i].score > files[j].score
	})
	if len(files) > 24 {
		files = files[:24]
	}
	var out strings.Builder
	budget := int64(120000)
	for _, file := range files {
		if budget <= 0 {
			break
		}
		content, err := readText(file.path, min64(maxFileBytes, budget))
		if err != nil || strings.TrimSpace(content) == "" {
			continue
		}
		rel, _ := filepath.Rel(root, file.path)
		fmt.Fprintf(&out, "\n--- %s:%s ---\n%s\n", repository, filepath.ToSlash(rel), content)
		budget -= int64(len(content))
	}
	return out.String(), nil
}

func readText(path string, limit int64) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	reader := bufio.NewReader(io.LimitReader(file, limit))
	raw, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	for _, b := range raw {
		if b == 0 {
			return "", nil
		}
	}
	return string(raw), nil
}
func supported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".js", ".ts", ".tsx", ".jsx", ".php", ".py", ".rb", ".java", ".sql", ".yml", ".yaml", ".json", ".md", ".css", ".scss", ".html", ".sh", ".xml":
		return true
	}
	base := strings.ToLower(filepath.Base(path))
	return base == "dockerfile" || base == "makefile" || strings.HasPrefix(base, "readme")
}
func keywords(value string) []string {
	seen := map[string]bool{}
	var out []string
	for _, part := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len(part) < 4 || seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
	}
	return out
}
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

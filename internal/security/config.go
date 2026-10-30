package security

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/ReindeerGames/echo/internal/domain"
)

func RepositoryConfigHash(repository domain.Repository) string {
	raw, _ := json.Marshal(struct {
		ProjectID     string `json:"projectId"`
		Owner         string `json:"owner"`
		Name          string `json:"name"`
		DefaultBranch string `json:"defaultBranch"`
		ReadOnly      bool   `json:"readOnly"`
		Enabled       bool   `json:"enabled"`
	}{repository.ProjectID, repository.Owner, repository.Name, repository.DefaultBranch, repository.ReadOnly, repository.Enabled})
	return ConfigHash(raw)
}

func ConfigHash(raw []byte) string {
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("%x", digest)
}

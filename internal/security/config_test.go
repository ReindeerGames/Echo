package security

import (
	"testing"

	"github.com/ReindeerGames/echo/internal/domain"
)

func TestRepositoryConfigHashBindsSecurityRelevantFields(t *testing.T) {
	repository := domain.Repository{ProjectID: "project", Owner: "owner", Name: "site", DefaultBranch: "main", Enabled: true}
	original := RepositoryConfigHash(repository)
	repository.Name = "another-site"
	if RepositoryConfigHash(repository) == original {
		t.Fatal("repository identity change did not change the approval hash")
	}
	repository.Name = "site"
	repository.ReadOnly = true
	if RepositoryConfigHash(repository) == original {
		t.Fatal("repository access change did not change the approval hash")
	}
}

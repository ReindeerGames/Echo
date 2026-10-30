package githubmonitor

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/githubapp"
)

type fakeStore struct {
	repositories []domain.Repository
	observations []observation
}

type observation struct {
	repositoryID string
	state        string
	headSHA      string
	summary      string
	failed       []string
	ownerJID     string
}

func (f *fakeStore) ListRepositories(context.Context, string) ([]domain.Repository, error) {
	return append([]domain.Repository(nil), f.repositories...), nil
}
func (f *fakeStore) ReconcileRepositoryWorkflowAlert(_ context.Context, repositoryID, state, headSHA, summary string, failed []string, _ time.Time, ownerJID string) (*domain.RepositoryWorkflowTransition, error) {
	f.observations = append(f.observations, observation{repositoryID: repositoryID, state: state, headSHA: headSHA, summary: summary, failed: append([]string(nil), failed...), ownerJID: ownerJID})
	return &domain.RepositoryWorkflowTransition{Kind: "opened", Alert: domain.RepositoryWorkflowAlert{RepositoryID: repositoryID}}, nil
}

type fakeInspector struct {
	configured bool
	statuses   map[string]githubapp.RepositoryStatus
	errors     map[string]error
}

func (f fakeInspector) TokenStatus(context.Context) (githubapp.TokenStatus, error) {
	return githubapp.TokenStatus{Configured: f.configured}, nil
}
func (f fakeInspector) InspectRepository(_ context.Context, repository domain.Repository) (githubapp.RepositoryStatus, error) {
	name := repository.Owner + "/" + repository.Name
	return f.statuses[name], f.errors[name]
}

type fakeOwner struct{ jid string }

func (f fakeOwner) JID(context.Context) (string, error) { return f.jid, nil }

func TestSweepOpensFailuresResolvesHealthAndIgnoresInconclusiveRuns(t *testing.T) {
	repositories := []domain.Repository{
		{ID: "repo_0123456789abcdef012345", Owner: "ReindeerGames", Name: "failed", Enabled: true},
		{ID: "repo_0123456789abcdef012346", Owner: "ReindeerGames", Name: "healthy", Enabled: true},
		{ID: "repo_0123456789abcdef012347", Owner: "ReindeerGames", Name: "running", Enabled: true},
		{ID: "repo_0123456789abcdef012348", Owner: "ReindeerGames", Name: "disabled", Enabled: false},
	}
	store := &fakeStore{repositories: repositories}
	inspector := fakeInspector{configured: true, statuses: map[string]githubapp.RepositoryStatus{
		"ReindeerGames/failed":  {HeadSHA: strings.Repeat("a", 40), WorkflowRuns: []githubapp.RepositoryWorkflowRun{{Name: "Build", Status: "completed", Conclusion: "failure"}}},
		"ReindeerGames/healthy": {HeadSHA: strings.Repeat("b", 40), WorkflowRuns: []githubapp.RepositoryWorkflowRun{{Name: "Build", Status: "completed", Conclusion: "success"}}},
		"ReindeerGames/running": {HeadSHA: strings.Repeat("c", 40), WorkflowRuns: []githubapp.RepositoryWorkflowRun{{Name: "Build", Status: "in_progress"}}},
	}, errors: map[string]error{}}
	watchdog := New(store, inspector, fakeOwner{jid: "owner@s.whatsapp.net"}, 15*time.Minute)
	transitions, err := watchdog.Sweep(context.Background(), time.Now())
	if err != nil || len(transitions) != 3 || len(store.observations) != 3 {
		t.Fatalf("transitions=%#v observations=%#v err=%v", transitions, store.observations, err)
	}
	states := map[string]string{}
	for _, item := range store.observations {
		states[item.repositoryID] = item.state
		if item.ownerJID != "owner@s.whatsapp.net" {
			t.Fatalf("owner not propagated: %#v", item)
		}
	}
	if states[repositories[0].ID] != "attention" || states[repositories[1].ID] != "healthy" || states[repositories[3].ID] != "disabled" {
		t.Fatalf("unexpected observations: %#v", store.observations)
	}
	if _, exists := states[repositories[2].ID]; exists {
		t.Fatalf("running workflow cleared or opened an alert: %#v", store.observations)
	}
}

func TestSweepClearsAlertsWhenGitHubIsDisconnected(t *testing.T) {
	store := &fakeStore{repositories: []domain.Repository{{ID: "repo_0123456789abcdef012345", Owner: "ReindeerGames", Name: "echo", Enabled: true}}}
	watchdog := New(store, fakeInspector{configured: false}, nil, 15*time.Minute)
	if _, err := watchdog.Sweep(context.Background(), time.Now()); err != nil || len(store.observations) != 1 || store.observations[0].state != "disabled" {
		t.Fatalf("observations=%#v err=%v", store.observations, err)
	}
}

func TestSweepPreservesPriorFailureWhenInspectionFails(t *testing.T) {
	store := &fakeStore{repositories: []domain.Repository{{ID: "repo_0123456789abcdef012345", Owner: "ReindeerGames", Name: "echo", Enabled: true}}}
	watchdog := New(store, fakeInspector{configured: true, statuses: map[string]githubapp.RepositoryStatus{}, errors: map[string]error{"ReindeerGames/echo": errors.New("unavailable")}}, nil, 15*time.Minute)
	if _, err := watchdog.Sweep(context.Background(), time.Now()); err == nil || len(store.observations) != 0 {
		t.Fatalf("observations=%#v err=%v", store.observations, err)
	}
}

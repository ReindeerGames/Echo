package githubapp

import (
	"reflect"
	"testing"
	"time"
)

func TestAssessWorkflowHealthUsesLatestRunPerWorkflow(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	status := RepositoryStatus{WorkflowRuns: []RepositoryWorkflowRun{
		{Name: "Build", Status: "completed", Conclusion: "failure", UpdatedAt: now.Add(-time.Hour)},
		{Name: "Build", Status: "completed", Conclusion: "success", UpdatedAt: now},
		{Name: "Deploy", Status: "completed", Conclusion: "failure", UpdatedAt: now},
		{Name: "Security", Status: "in_progress", UpdatedAt: now},
	}}
	health := AssessWorkflowHealth(status)
	if health.State != "attention" || health.Summary != "1 latest workflow(s) failed; 1 running; 1 healthy" || !reflect.DeepEqual(health.FailedWorkflows, []string{"Deploy"}) || !reflect.DeepEqual(health.RunningWorkflows, []string{"Security"}) {
		t.Fatalf("unexpected health: %#v", health)
	}
}

func TestAssessWorkflowHealthRequiresCompletedHealthyEvidence(t *testing.T) {
	tests := []struct {
		name    string
		runs    []RepositoryWorkflowRun
		state   string
		summary string
	}{
		{name: "absent", state: "unknown", summary: "no recent default-branch workflows returned"},
		{name: "running", runs: []RepositoryWorkflowRun{{Name: "Build", Status: "queued"}}, state: "running", summary: "1 workflow(s) running; 0 healthy"},
		{name: "healthy", runs: []RepositoryWorkflowRun{{Name: "Build", Status: "completed", Conclusion: "success"}, {Name: "Lint", Status: "completed", Conclusion: "skipped"}}, state: "healthy", summary: "2 latest workflow(s) healthy"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			health := AssessWorkflowHealth(RepositoryStatus{WorkflowRuns: test.runs})
			if health.State != test.state || health.Summary != test.summary {
				t.Fatalf("unexpected health: %#v", health)
			}
		})
	}
}

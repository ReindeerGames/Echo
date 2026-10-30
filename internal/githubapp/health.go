package githubapp

import (
	"fmt"
	"sort"
	"strings"
)

type WorkflowHealth struct {
	State            string
	Summary          string
	FailedWorkflows  []string
	RunningWorkflows []string
}

// AssessWorkflowHealth reduces recent default-branch runs to the newest result
// for each workflow. It deliberately treats running and absent results as
// inconclusive rather than recovery evidence.
func AssessWorkflowHealth(status RepositoryStatus) WorkflowHealth {
	if len(status.WorkflowRuns) == 0 {
		return WorkflowHealth{State: "unknown", Summary: "no recent default-branch workflows returned"}
	}
	latest := map[string]RepositoryWorkflowRun{}
	for _, run := range status.WorkflowRuns {
		name := strings.TrimSpace(run.Name)
		if name == "" {
			name = "unnamed workflow"
		}
		previous, ok := latest[name]
		if !ok || run.UpdatedAt.After(previous.UpdatedAt) {
			run.Name = name
			latest[name] = run
		}
	}
	health := WorkflowHealth{}
	healthy := 0
	for name, run := range latest {
		if run.Status != "completed" {
			health.RunningWorkflows = append(health.RunningWorkflows, name)
			continue
		}
		if workflowConclusionHealthy(run.Conclusion) {
			healthy++
		} else {
			health.FailedWorkflows = append(health.FailedWorkflows, name)
		}
	}
	sort.Strings(health.FailedWorkflows)
	sort.Strings(health.RunningWorkflows)
	if len(health.FailedWorkflows) > 0 {
		health.State = "attention"
		health.Summary = fmt.Sprintf("%d latest workflow(s) failed; %d running; %d healthy", len(health.FailedWorkflows), len(health.RunningWorkflows), healthy)
		return health
	}
	if len(health.RunningWorkflows) > 0 {
		health.State = "running"
		health.Summary = fmt.Sprintf("%d workflow(s) running; %d healthy", len(health.RunningWorkflows), healthy)
		return health
	}
	health.State = "healthy"
	health.Summary = fmt.Sprintf("%d latest workflow(s) healthy", healthy)
	return health
}

func workflowConclusionHealthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "success", "neutral", "skipped":
		return true
	default:
		return false
	}
}

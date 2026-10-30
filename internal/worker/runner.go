package worker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/ai"
	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/githubapp"
	"github.com/ReindeerGames/echo/internal/id"
	"github.com/ReindeerGames/echo/internal/owner"
	"github.com/ReindeerGames/echo/internal/security"
	"github.com/ReindeerGames/echo/internal/store"
)

type Runner struct {
	store        *store.Store
	github       *githubapp.Client
	ai           *ai.Client
	owners       *owner.Service
	workspace    string
	poll         time.Duration
	lease        time.Duration
	heartbeat    time.Duration
	maxFileBytes int64
	workerID     string
}

func New(st *store.Store, github *githubapp.Client, aiClient *ai.Client, owners *owner.Service, workspace string, poll, lease, heartbeat time.Duration, maxFileBytes int64) *Runner {
	return &Runner{store: st, github: github, ai: aiClient, owners: owners, workspace: workspace, poll: poll, lease: lease, heartbeat: heartbeat, maxFileBytes: maxFileBytes, workerID: id.New("worker")}
}

func (r *Runner) Run(ctx context.Context) error {
	if err := os.MkdirAll(r.workspace, 0o700); err != nil {
		return err
	}
	ticker := time.NewTicker(r.poll)
	defer ticker.Stop()
	nextHeartbeat := time.Time{}
	recordHeartbeat := func() {
		now := time.Now().UTC()
		if now.Before(nextHeartbeat) {
			return
		}
		if r.recordHeartbeat(ctx, now) {
			nextHeartbeat = now.Add(r.heartbeat)
		}
	}
	recordHeartbeat()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			job, err := r.store.ClaimJob(ctx, r.workerID, r.lease)
			if errors.Is(err, store.ErrNotFound) {
				recordHeartbeat()
				continue
			}
			if err != nil {
				slog.Error("claim worker job", "error", err)
				continue
			}
			recordHeartbeat()
			jobErr := r.executeWithLease(ctx, job)
			notification := r.terminalFailureNotification(ctx, job, jobErr)
			if err := r.store.CompleteJob(ctx, job.ID, job.Attempts, r.workerID, jobErr, notification); err != nil {
				slog.Error("complete worker job", "error", err)
			}
			if jobErr != nil {
				slog.Error("worker job failed", "job_id", job.ID, "job_type", job.Type, "error", jobErr)
			}
		}
	}
}

func (r *Runner) recordHeartbeat(ctx context.Context, now time.Time) bool {
	if err := r.store.RecordWorkerHeartbeat(ctx, r.workerID, now); err != nil {
		if ctx.Err() != nil {
			return false
		}
		slog.Error("record worker heartbeat", "worker_id", r.workerID, "error", err)
		return false
	}
	return true
}

func (r *Runner) executeWithLease(ctx context.Context, job domain.Job) error {
	jobCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	leaseFailure := make(chan error, 1)
	go func() {
		defer close(done)
		ticker := time.NewTicker(leaseHeartbeatInterval(r.lease))
		defer ticker.Stop()
		for {
			select {
			case <-jobCtx.Done():
				return
			case <-ticker.C:
				renewed, err := r.store.RenewJobLease(jobCtx, job.ID, job.Attempts, r.workerID, r.lease)
				if err != nil {
					slog.Error("renew worker job lease", "job_id", job.ID, "error", err)
					leaseFailure <- fmt.Errorf("renew job lease: %w", err)
					cancel()
					return
				}
				if !renewed {
					slog.Warn("worker job lease ownership lost", "job_id", job.ID, "attempt", job.Attempts)
					leaseFailure <- errors.New("job lease ownership lost")
					cancel()
					return
				}
			}
		}
	}()
	jobErr := r.execute(jobCtx, job)
	cancel()
	<-done
	select {
	case leaseErr := <-leaseFailure:
		if jobErr == nil || errors.Is(jobErr, context.Canceled) {
			return leaseErr
		}
	default:
	}
	return jobErr
}

func leaseHeartbeatInterval(lease time.Duration) time.Duration {
	interval := lease / 3
	if interval < time.Second {
		return time.Second
	}
	if interval > 30*time.Second {
		return 30 * time.Second
	}
	return interval
}

func (r *Runner) terminalFailureNotification(ctx context.Context, job domain.Job, jobErr error) *domain.OutboundMessage {
	if jobErr == nil || job.Attempts < job.MaxAttempts || r.owners == nil {
		return nil
	}
	incidentID := domain.JobIncidentID(job.Payload)
	if incidentID == "" {
		return nil
	}
	ownerJID, err := r.owners.JID(ctx)
	if err != nil || ownerJID == "" {
		if err != nil {
			slog.Error("resolve owner for terminal job failure", "job_id", job.ID, "error", err)
		}
		return nil
	}
	return &domain.OutboundMessage{ChatJID: ownerJID, Body: terminalFailureMessage(job, incidentID, jobErr)}
}

func terminalFailureMessage(job domain.Job, incidentID string, jobErr error) string {
	category := strings.ReplaceAll(domain.JobErrorCategory(jobErr.Error()), "_", " ")
	return fmt.Sprintf("Echo could not complete %s for %s after %d attempts.\n\nFailure category: %s.\n\nThe action has stopped and will not continue automatically. Review the incident in the control plane before retrying.", strings.ReplaceAll(job.Type, "_", " "), incidentID, job.MaxAttempts, category)
}

func (r *Runner) execute(ctx context.Context, job domain.Job) error {
	switch job.Type {
	case "analyze_incident":
		return r.analyze(ctx, job)
	case "prepare_patch":
		return r.preparePatch(ctx, job)
	case "deploy_staging", "deploy_production":
		return r.deploy(ctx, job)
	case "merge_production":
		return r.mergeProduction(ctx, job)
	case "rollback":
		return r.deploy(ctx, job)
	default:
		return fmt.Errorf("unknown job type %q", job.Type)
	}
}

func (r *Runner) mergeProduction(ctx context.Context, job domain.Job) error {
	if r.github == nil {
		return errors.New("repository-native production requires GitHub configuration")
	}
	var approved approvedJob
	if err := json.Unmarshal(job.Payload, &approved); err != nil {
		return err
	}
	var action struct {
		ChangeSetID            string `json:"changeSetId"`
		RepositoryID           string `json:"repositoryId"`
		RepositoryFullName     string `json:"repositoryFullName"`
		RepositoryConfigSHA256 string `json:"repositoryConfigSha256"`
		PullRequestNumber      int64  `json:"pullRequestNumber"`
		HeadSHA                string `json:"headSha"`
		BaseBranch             string `json:"baseBranch"`
	}
	if err := json.Unmarshal(approved.Action, &action); err != nil {
		return err
	}
	if !immutableArtifact(action.HeadSHA) || action.PullRequestNumber <= 0 {
		return errors.New("production approval is not bound to a valid pull request commit")
	}
	incident, err := r.store.GetIncident(ctx, approved.IncidentID)
	if err != nil {
		return err
	}
	environments, err := r.store.ListEnvironments(ctx, incident.ProjectID)
	if err != nil {
		return err
	}
	for _, environment := range environments {
		if strings.EqualFold(environment.Name, "staging") {
			return errors.New("repository-native production is no longer eligible because this project now has a staging environment")
		}
	}
	change, err := r.store.GetChangeSet(ctx, action.ChangeSetID)
	if err != nil {
		return err
	}
	if change.IncidentID != incident.ID || change.RepositoryID != action.RepositoryID || change.PullRequestNumber != action.PullRequestNumber || change.CommitSHA != action.HeadSHA || (change.Status != "testing" && change.Status != "ready" && change.Status != "merged") {
		return errors.New("change set no longer matches the approved production action")
	}
	repository, err := r.store.GetRepository(ctx, action.RepositoryID)
	if err != nil {
		return err
	}
	if repository.ProjectID != incident.ProjectID || !repository.Enabled || repository.ReadOnly || repository.Owner+"/"+repository.Name != action.RepositoryFullName || repository.DefaultBranch != action.BaseBranch || security.RepositoryConfigHash(repository) != action.RepositoryConfigSHA256 {
		return errors.New("repository configuration changed after production approval; request a new approval")
	}

	deployment, err := r.store.FindDeploymentByApproval(ctx, approved.ApprovalID)
	if err == nil {
		if deployment.Status == "verified" || deployment.Status == "failed" {
			return nil
		}
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	} else {
		mergeSHA, err := r.github.MergePullRequest(ctx, repository, action.PullRequestNumber, action.HeadSHA)
		if err != nil {
			return err
		}
		if !immutableArtifact(mergeSHA) {
			return errors.New("GitHub returned an invalid merge commit SHA")
		}
		if err := r.store.MarkChangeSetMerged(ctx, change.ID); err != nil {
			return err
		}
		workflowURL := fmt.Sprintf("https://github.com/%s/%s/actions", repository.Owner, repository.Name)
		deployment, err = r.store.CreateDeployment(ctx, domain.Deployment{
			IncidentID: incident.ID, ApprovalID: approved.ApprovalID, ProjectID: incident.ProjectID,
			Environment: "production", Operation: "deploy", ArtifactRef: mergeSHA, Status: "running",
			WorkflowURL: workflowURL, Repository: repository.Owner + "/" + repository.Name, Workflow: "repository-native-main",
		})
		if err != nil {
			return err
		}
		_ = r.store.Audit(ctx, "worker", r.workerID, "production.pull_request_merged", "deployment", deployment.ID, "succeeded", map[string]any{"pullRequest": action.PullRequestNumber, "approvedHead": action.HeadSHA, "mergeCommit": mergeSHA, "approvalId": approved.ApprovalID})
	}

	result, err := r.github.WaitForCommitWorkflows(ctx, repository, deployment.ArtifactRef)
	if err != nil {
		return err
	}
	if err := r.store.CompleteRepositoryNativeDeployment(ctx, deployment.ID, result.RunID, result.URL, result.Success); err != nil {
		return err
	}
	if result.Success {
		_ = r.store.Audit(ctx, "worker", r.workerID, "production.repository_workflow_verified", "deployment", deployment.ID, "succeeded", map[string]any{"mergeCommit": deployment.ArtifactRef, "workflowRun": result.URL, "workflows": result.Workflows})
		return r.notifyOwner(ctx, fmt.Sprintf("Echo merged and verified production deployment for %s.\n\nCommit: %s\nWorkflow: %s\n\nThe repository's existing main-branch workflow completed successfully, including its own deployment verification.", incident.ID, deployment.ArtifactRef, result.URL))
	}
	_ = r.store.Audit(ctx, "worker", r.workerID, "production.repository_workflow_verified", "deployment", deployment.ID, "failed", map[string]any{"mergeCommit": deployment.ArtifactRef, "workflowRun": result.URL, "workflows": result.Workflows})
	return r.notifyOwner(ctx, fmt.Sprintf("Production deployment failed for %s after the approved pull request was merged.\n\nCommit: %s\nWorkflow: %s\n\nEcho marked the incident failed. Review the repository workflow before taking further action.", incident.ID, deployment.ArtifactRef, result.URL))
}

type approvedJob struct {
	IncidentID string          `json:"incidentId"`
	ApprovalID string          `json:"approvalId"`
	Action     json.RawMessage `json:"action"`
}

func (r *Runner) preparePatch(ctx context.Context, job domain.Job) error {
	if r.github == nil || r.ai == nil {
		return errors.New("patch preparation requires both GitHub and AI configuration")
	}
	aiStatus, err := r.ai.Status(ctx)
	if err != nil || !aiStatus.Configured {
		return errors.New("patch preparation requires AI configuration")
	}
	var approved approvedJob
	if err := json.Unmarshal(job.Payload, &approved); err != nil {
		return err
	}
	var action struct {
		RepositoryID           string `json:"repositoryId"`
		RepositoryFullName     string `json:"repositoryFullName"`
		RepositoryConfigSHA256 string `json:"repositoryConfigSha256"`
	}
	if err := json.Unmarshal(approved.Action, &action); err != nil {
		return err
	}
	incident, err := r.store.GetIncident(ctx, approved.IncidentID)
	if err != nil {
		return err
	}
	repository, err := r.store.GetRepository(ctx, action.RepositoryID)
	if err != nil {
		return err
	}
	if repository.ReadOnly || !repository.Enabled || repository.ProjectID != incident.ProjectID {
		return errors.New("approved repository is not write-enabled for this incident")
	}
	if action.RepositoryFullName != repository.Owner+"/"+repository.Name || action.RepositoryConfigSHA256 != security.RepositoryConfigHash(repository) {
		return errors.New("repository configuration changed after approval; request a new approval")
	}
	analysis, err := r.store.LatestAnalysis(ctx, incident.ID)
	if err != nil {
		return err
	}
	analysisRaw, _ := json.Marshal(analysis)
	workspace := filepath.Join(r.workspace, job.ID)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(workspace)
	if err := r.github.FetchRepository(ctx, repository, workspace); err != nil {
		return err
	}
	evidence, err := collectEvidence(workspace, repository.Owner+"/"+repository.Name, incident.SourceBody, r.maxFileBytes)
	if err != nil {
		return err
	}
	plan, err := r.ai.PreparePatch(ctx, incident.SourceBody, string(analysisRaw), evidence)
	if err != nil {
		return err
	}
	if len(plan.Changes) == 0 {
		return fmt.Errorf("AI declined to prepare a safe patch: %s", plan.Summary)
	}
	changes := make([]githubapp.FileChange, 0, len(plan.Changes))
	for _, change := range plan.Changes {
		target, err := workerSafePath(workspace, change.Path)
		if err != nil {
			return err
		}
		if _, err := os.Stat(target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		changes = append(changes, githubapp.FileChange{Path: change.Path, Content: change.Content})
	}
	branch := fmt.Sprintf("echo/%s-%s", strings.TrimPrefix(incident.ID, "inc_"), strings.TrimPrefix(approved.ApprovalID, "apr_"))
	title := fmt.Sprintf("fix: %s [%s]", truncateText(incident.Summary, 72), incident.ID)
	body := fmt.Sprintf("## Echo incident\n\n%s\n\n## Proposed change\n\n%s\n\n## Validation\n\n%s\n\nThis is a draft pull request created after explicit owner approval. It must pass repository CI and staging verification before production promotion.", incident.SourceBody, plan.Summary, bulletList(plan.Tests))
	pr, err := r.github.CreatePullRequest(ctx, repository, branch, title, body, changes)
	if err != nil {
		return err
	}
	changeRaw, _ := json.Marshal(plan.Changes)
	digest := sha256.Sum256(changeRaw)
	changeSet, err := r.store.CreateChangeSet(ctx, domain.ChangeSet{IncidentID: incident.ID, RepositoryID: repository.ID, BranchName: pr.Branch, CommitSHA: pr.CommitSHA, PullRequestNumber: pr.Number, PullRequestURL: pr.URL, ArtifactRef: pr.CommitSHA, Status: "testing", DiffSHA256: fmt.Sprintf("%x", digest)})
	if err != nil {
		return err
	}
	_ = r.store.Audit(ctx, "worker", r.workerID, "patch.pull_request_created", "change_set", changeSet.ID, "succeeded", map[string]any{"pullRequest": pr.URL, "commit": pr.CommitSHA, "approvalId": approved.ApprovalID})
	return r.notifyOwner(ctx, fmt.Sprintf("Echo prepared a draft patch for %s.\n\n%s\n\nPull request: %s\nCommit: %s\n\nRepository CI must pass before any deployment approval. If this project has no staging environment, review the pull request in Echo and request the separate merge and production deployment approval.", incident.ID, plan.Summary, pr.URL, pr.CommitSHA))
}

func (r *Runner) deploy(ctx context.Context, job domain.Job) error {
	if r.github == nil {
		return errors.New("deployment requires GitHub configuration")
	}
	var approved approvedJob
	if err := json.Unmarshal(job.Payload, &approved); err != nil {
		return err
	}
	var action struct {
		EnvironmentID           string `json:"environmentId"`
		ArtifactRef             string `json:"artifactRef"`
		StagingDeploymentID     string `json:"stagingDeploymentId"`
		SourceDeploymentID      string `json:"sourceDeploymentId"`
		RepositoryID            string `json:"repositoryId"`
		RepositoryConfigSHA256  string `json:"repositoryConfigSha256"`
		EnvironmentConfigSHA256 string `json:"environmentConfigSha256"`
		BranchName              string `json:"branchName"`
	}
	if err := json.Unmarshal(approved.Action, &action); err != nil {
		return err
	}
	if !immutableArtifact(action.ArtifactRef) {
		return errors.New("deployment artifactRef must be a 40-character commit SHA or sha256 digest")
	}
	incident, err := r.store.GetIncident(ctx, approved.IncidentID)
	if err != nil {
		return err
	}
	environment, err := r.store.GetEnvironment(ctx, action.EnvironmentID)
	if err != nil {
		return err
	}
	if environment.ProjectID != incident.ProjectID || !environment.Protected {
		return errors.New("environment is not a protected environment for this project")
	}
	if action.EnvironmentConfigSHA256 != security.ConfigHash(environment.Config) {
		return errors.New("environment configuration changed after approval; request a new approval")
	}
	var cfg struct {
		RepositoryID string            `json:"repositoryId"`
		Workflow     string            `json:"workflow"`
		Inputs       map[string]string `json:"inputs"`
	}
	if err := json.Unmarshal(environment.Config, &cfg); err != nil {
		return err
	}
	if cfg.RepositoryID == "" || cfg.Workflow == "" {
		return errors.New("environment requires repositoryId and workflow")
	}
	repository, err := r.store.GetRepository(ctx, cfg.RepositoryID)
	if err != nil {
		return err
	}
	if repository.ID != action.RepositoryID || action.RepositoryConfigSHA256 != security.RepositoryConfigHash(repository) {
		return errors.New("deployment repository configuration changed after approval; request a new approval")
	}
	var change domain.ChangeSet
	if job.Type == "rollback" {
		source, err := r.store.GetDeployment(ctx, action.SourceDeploymentID)
		if err != nil || source.ProjectID != incident.ProjectID || source.Status != "verified" || source.ArtifactRef != action.ArtifactRef {
			return errors.New("rollback source is no longer a verified deployment of this artifact")
		}
		change, err = r.store.FindProjectChangeSetByArtifact(ctx, incident.ProjectID, action.ArtifactRef)
		if err != nil {
			return err
		}
	} else {
		change, err = r.store.FindChangeSetByArtifact(ctx, incident.ID, action.ArtifactRef)
		if err != nil {
			return err
		}
	}
	if change.RepositoryID != repository.ID || change.BranchName == "" || change.BranchName != action.BranchName {
		return errors.New("approved artifact branch does not match the deployment repository")
	}
	if job.Type == "deploy_production" {
		staging, err := r.store.GetDeployment(ctx, action.StagingDeploymentID)
		if err != nil || staging.IncidentID != incident.ID || staging.ProjectID != incident.ProjectID || !strings.EqualFold(staging.Environment, "staging") || staging.Status != "verified" || staging.ArtifactRef != action.ArtifactRef {
			return errors.New("verified staging evidence is no longer valid for this production action")
		}
	}
	if existing, err := r.store.FindDeploymentByApproval(ctx, approved.ApprovalID); err == nil {
		if existing.Status == "dispatching" {
			return errors.New("deployment dispatch outcome is ambiguous; waiting for a signed workflow webhook or operator reconciliation")
		}
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	workflowURL := fmt.Sprintf("https://github.com/%s/%s/actions/workflows/%s", repository.Owner, repository.Name, cfg.Workflow)
	operation := "deploy"
	if job.Type == "rollback" {
		operation = "rollback"
	}
	deployment, err := r.store.CreateDeployment(ctx, domain.Deployment{IncidentID: incident.ID, ApprovalID: approved.ApprovalID, ProjectID: incident.ProjectID, Environment: environment.Name, Operation: operation, ArtifactRef: action.ArtifactRef, SourceDeploymentID: action.SourceDeploymentID, Status: "dispatching", WorkflowURL: workflowURL, Repository: repository.Owner + "/" + repository.Name, Workflow: cfg.Workflow})
	if err != nil {
		return err
	}
	if cfg.Inputs == nil {
		cfg.Inputs = map[string]string{}
	}
	cfg.Inputs["echo_deployment_id"] = deployment.ID
	cfg.Inputs["echo_incident_id"] = incident.ID
	cfg.Inputs["echo_artifact_ref"] = action.ArtifactRef
	cfg.Inputs["echo_environment"] = environment.Name
	if err := r.github.DispatchWorkflow(ctx, repository, cfg.Workflow, change.BranchName, cfg.Inputs); err != nil {
		_ = r.store.Audit(ctx, "worker", r.workerID, "deployment.dispatch", "deployment", deployment.ID, "failed", map[string]any{"error": err.Error(), "approvalId": approved.ApprovalID, "outcome": "ambiguous"})
		return err
	}
	if err := r.store.MarkDeploymentQueued(ctx, deployment.ID); err != nil {
		return err
	}
	_ = r.store.Audit(ctx, "worker", r.workerID, "deployment.dispatched", "deployment", deployment.ID, "succeeded", map[string]any{"environment": environment.Name, "artifactRef": action.ArtifactRef, "approvalId": approved.ApprovalID})
	return r.notifyOwner(ctx, fmt.Sprintf("Echo dispatched %s for %s.\nArtifact: %s\nWorkflow: %s\n\nEcho will not treat this as verified until the CI result is recorded.", environment.Name, incident.ID, action.ArtifactRef, workflowURL))
}

func workerSafePath(root, rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || filepath.IsAbs(clean) {
		return "", fmt.Errorf("unsafe patch path %q", rel)
	}
	target := filepath.Join(root, clean)
	rootPrefix := filepath.Clean(root) + string(os.PathSeparator)
	if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), rootPrefix) {
		return "", fmt.Errorf("unsafe patch path %q", rel)
	}
	return target, nil
}
func immutableArtifact(value string) bool {
	return regexp.MustCompile(`^[a-fA-F0-9]{40}$|^sha256:[a-fA-F0-9]{64}$`).MatchString(value)
}
func truncateText(value string, max int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max-1]) + "…"
}
func bulletList(values []string) string {
	if len(values) == 0 {
		return "Repository CI and configured staging checks."
	}
	return "- " + strings.Join(values, "\n- ")
}

func (r *Runner) analyze(ctx context.Context, job domain.Job) error {
	var payload struct {
		IncidentID string `json:"incidentId"`
	}
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return err
	}
	release, err := r.store.AcquireNamedLock(ctx, "echo_incident_analysis_"+payload.IncidentID, 2*time.Second)
	if err != nil {
		return err
	}
	defer release()
	incident, err := r.store.GetIncident(ctx, payload.IncidentID)
	if err != nil {
		return err
	}
	if latest, latestErr := r.store.LatestAnalysis(ctx, incident.ID); latestErr == nil && latest.EvidenceRevision >= incident.EvidenceRevision {
		return nil
	} else if latestErr != nil && !errors.Is(latestErr, store.ErrNotFound) {
		return latestErr
	}
	messages, err := r.store.ListIncidentMessages(ctx, incident.ID, 20)
	if err != nil {
		return err
	}
	incidentText := incidentThreadText(incident, messages)
	repos, err := r.store.ListRepositories(ctx, incident.ProjectID)
	if err != nil {
		return err
	}
	workspace := filepath.Join(r.workspace, job.ID)
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(workspace)
	evidence := ""
	for _, repo := range repos {
		if !repo.Enabled {
			continue
		}
		if r.github == nil {
			evidence += fmt.Sprintf("\nRepository %s/%s could not be fetched: GitHub is not configured.\n", repo.Owner, repo.Name)
			continue
		}
		repoDir := filepath.Join(workspace, repo.Owner+"_"+repo.Name)
		if err := r.github.FetchRepository(ctx, repo, repoDir); err != nil {
			evidence += fmt.Sprintf("\nRepository %s/%s fetch failed: %s\n", repo.Owner, repo.Name, err)
			continue
		}
		chunk, err := collectEvidence(repoDir, repo.Owner+"/"+repo.Name, incidentText, r.maxFileBytes)
		if err != nil {
			return err
		}
		evidence += chunk
	}
	aiConfigured := false
	if r.ai != nil {
		status, statusErr := r.ai.Status(ctx)
		if statusErr != nil {
			return statusErr
		}
		aiConfigured = status.Configured
	}
	if !aiConfigured {
		analysis := domain.Analysis{IncidentID: incident.ID, EvidenceRevision: incident.EvidenceRevision, Status: "insufficient_evidence", Confidence: "none", Summary: "The report was captured, but AI analysis is disabled. Configure the AI provider to produce an evidence-based diagnosis.", Evidence: json.RawMessage(`[]`), Proposal: json.RawMessage(`{"action":"configure_ai"}`)}
		_, err := r.store.SaveAnalysis(ctx, analysis)
		return err
	}
	attachments, err := r.store.LoadIncidentAttachments(ctx, incident.ID)
	if err != nil {
		return err
	}
	diagnosis, err := r.ai.Diagnose(ctx, incidentText, evidence, attachments...)
	if err != nil {
		return err
	}
	evidenceJSON, _ := json.Marshal(diagnosis.Evidence)
	proposalJSON, _ := json.Marshal(diagnosis.Proposal)
	analysis, err := r.store.SaveAnalysis(ctx, domain.Analysis{IncidentID: incident.ID, EvidenceRevision: incident.EvidenceRevision, Status: diagnosis.Status, Confidence: diagnosis.Confidence, Summary: diagnosis.Summary, Evidence: evidenceJSON, Proposal: proposalJSON})
	if err != nil {
		return err
	}
	current, err := r.store.GetIncident(ctx, incident.ID)
	if err != nil {
		return err
	}
	if current.EvidenceRevision != incident.EvidenceRevision {
		_ = r.store.Audit(ctx, "worker", r.workerID, "incident.analysis_superseded", "incident", incident.ID, "succeeded", map[string]any{"analysisId": analysis.ID, "analysisRevision": analysis.EvidenceRevision, "currentRevision": current.EvidenceRevision})
		return nil
	}
	_ = r.store.Audit(ctx, "worker", r.workerID, "incident.analyzed", "incident", incident.ID, "succeeded", map[string]any{"analysisId": analysis.ID, "confidence": analysis.Confidence})
	body := analysisOwnerMessage(incident, diagnosis, repos)
	return r.notifyOwner(ctx, body)
}

func incidentThreadText(incident domain.Incident, messages []domain.IncidentMessage) string {
	if len(messages) == 0 {
		return incident.SourceBody
	}
	var thread strings.Builder
	thread.WriteString("Incident conversation (untrusted WhatsApp evidence):\n")
	for _, message := range messages {
		label := "Follow-up"
		if message.WhatsAppMessageID == incident.SourceMessageID {
			label = "Original report"
		}
		name := strings.TrimSpace(message.SenderName)
		if name == "" {
			name = "group participant"
		}
		fmt.Fprintf(&thread, "\n[%s · %s · %s]\n%s\n", label, name, message.MessageTimestamp.UTC().Format(time.RFC3339), truncateText(message.Body, 4000))
		if thread.Len() >= 16000 {
			break
		}
	}
	return truncateText(thread.String(), 16000)
}

func analysisOwnerMessage(incident domain.Incident, diagnosis ai.Diagnosis, repositories []domain.Repository) string {
	body := fmt.Sprintf("Echo analysis for %s (%s confidence):\n\n%s", incident.ID, diagnosis.Confidence, diagnosis.Summary)
	if strings.TrimSpace(diagnosis.Proposal.Description) != "" {
		body += "\n\nProposed action:\n" + strings.TrimSpace(diagnosis.Proposal.Description)
	}

	var proposed *domain.Repository
	for index := range repositories {
		fullName := repositories[index].Owner + "/" + repositories[index].Name
		if strings.EqualFold(strings.TrimSpace(diagnosis.Proposal.Repository), fullName) {
			proposed = &repositories[index]
			break
		}
	}
	if proposed == nil && len(repositories) == 1 {
		proposed = &repositories[0]
	}

	switch {
	case proposed == nil:
		body += "\n\nNext step: open Echo → Incidents → this incident to review the evidence. Link the proposed repository before requesting a patch."
	case !proposed.Enabled:
		body += fmt.Sprintf("\n\nAction locked: %s/%s is disabled in Echo. Enable it under Repositories, then open this incident and request patch approval.", proposed.Owner, proposed.Name)
	case proposed.ReadOnly:
		body += fmt.Sprintf("\n\nAction locked: %s/%s is configured read-only. If you want Echo to prepare a draft fix, edit it under Repositories and enable approved changes. Then open this incident and request patch approval.", proposed.Owner, proposed.Name)
	default:
		body += "\n\nNext step: open Echo → Incidents → this incident → Request patch approval. Echo will then send you a one-time code with explicit APPROVE and REJECT reply options."
	}
	return body
}

func (r *Runner) notifyOwner(ctx context.Context, body string) error {
	if r.owners == nil {
		return nil
	}
	ownerJID, err := r.owners.JID(ctx)
	if err != nil || ownerJID == "" {
		return err
	}
	return r.store.EnqueueOutbound(ctx, ownerJID, body)
}

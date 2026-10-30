package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/owner"
	"github.com/ReindeerGames/echo/internal/policy"
	"github.com/ReindeerGames/echo/internal/security"
	"github.com/ReindeerGames/echo/internal/store"
)

type Service struct {
	store       *store.Store
	policy      *policy.Engine
	owners      *owner.Service
	approvalTTL time.Duration
}

func New(st *store.Store, policies *policy.Engine, owners *owner.Service, approvalTTL time.Duration) *Service {
	return &Service{store: st, policy: policies, owners: owners, approvalTTL: approvalTTL}
}

func (s *Service) ReceiveGroupReport(ctx context.Context, selfJIDs []string, msg domain.InboundMessage) (domain.Incident, bool, error) {
	binding, err := s.store.FindGroupBinding(ctx, msg.ChatJID)
	if errors.Is(err, store.ErrNotFound) {
		slog.Info("ignored WhatsApp group message", "reason", "group_not_bound", "group_jid", msg.ChatJID, "message_id", msg.MessageID)
		return domain.Incident{}, false, nil
	}
	if err != nil {
		return domain.Incident{}, false, err
	}
	accepted, reason := s.policy.InboundDecision(binding, msg, selfJIDs)
	if !accepted {
		metadata := map[string]any{
			"reason": reason, "groupJid": msg.ChatJID, "messageId": msg.MessageID,
			"mentionCount": len(msg.MentionedJIDs), "echoIdentityCount": len(selfJIDs),
		}
		slog.Info("ignored WhatsApp group message", "reason", reason, "group_jid", msg.ChatJID, "message_id", msg.MessageID, "mention_count", len(msg.MentionedJIDs))
		if auditErr := s.store.Audit(ctx, "whatsapp", msg.SenderJID, "message.ignored", "group_binding", binding.ID, "denied", metadata); auditErr != nil {
			slog.Error("audit ignored WhatsApp message", "error", auditErr, "message_id", msg.MessageID)
		}
		return domain.Incident{}, false, nil
	}
	incident, created, err := s.store.CreateIncident(ctx, binding, msg)
	if err != nil || !created {
		return incident, created, err
	}
	ownerJID, ownerErr := s.ownerJID(ctx)
	if ownerErr != nil {
		return incident, true, fmt.Errorf("resolve owner notification target: %w", ownerErr)
	}
	if ownerJID != "" {
		body := fmt.Sprintf("New Echo incident %s in %s from %s:\n\n%s\n\nI am collecting repository evidence now.", incident.ID, binding.ProjectName, msg.SenderName, msg.Body)
		if err := s.store.EnqueueOutbound(ctx, ownerJID, body); err != nil {
			return incident, true, fmt.Errorf("enqueue owner notification: %w", err)
		}
	}
	return incident, true, nil
}

func (s *Service) RequestApproval(ctx context.Context, incidentID, actionType, actor string, action map[string]any) (domain.Approval, error) {
	incident, err := s.store.GetIncident(ctx, incidentID)
	if err != nil {
		return domain.Approval{}, err
	}
	if err := s.policy.ValidateApprovalRequest(incident, actionType, action); err != nil {
		_ = s.store.Audit(ctx, "admin", actor, "approval.request", "incident", incidentID, "denied", map[string]any{"actionType": actionType, "reason": err.Error()})
		return domain.Approval{}, err
	}
	if actionType == "prepare_patch" {
		repositoryID := fmt.Sprint(action["repositoryId"])
		repository, err := s.store.GetRepository(ctx, repositoryID)
		if err != nil {
			return domain.Approval{}, err
		}
		if repository.ProjectID != incident.ProjectID || !repository.Enabled || repository.ReadOnly {
			return domain.Approval{}, fmt.Errorf("%w: repository is not an enabled write-gated repository for this project", policy.ErrDenied)
		}
		action["repositoryFullName"] = repository.Owner + "/" + repository.Name
		action["repositoryConfigSha256"] = security.RepositoryConfigHash(repository)
	}
	if actionType == "merge_production" {
		environments, err := s.store.ListEnvironments(ctx, incident.ProjectID)
		if err != nil {
			return domain.Approval{}, err
		}
		for _, environment := range environments {
			if strings.EqualFold(environment.Name, "staging") {
				return domain.Approval{}, fmt.Errorf("%w: repository-native production is available only when the project has no staging environment", policy.ErrDenied)
			}
		}
		change, err := s.store.GetChangeSet(ctx, fmt.Sprint(action["changeSetId"]))
		if err != nil {
			return domain.Approval{}, err
		}
		if change.IncidentID != incident.ID || (change.Status != "testing" && change.Status != "ready") || change.PullRequestNumber <= 0 || change.CommitSHA == "" {
			return domain.Approval{}, fmt.Errorf("%w: change set is not an eligible tested pull request for this incident", policy.ErrDenied)
		}
		repository, err := s.store.GetRepository(ctx, change.RepositoryID)
		if err != nil {
			return domain.Approval{}, err
		}
		if repository.ProjectID != incident.ProjectID || !repository.Enabled || repository.ReadOnly {
			return domain.Approval{}, fmt.Errorf("%w: production repository is not enabled for approved changes", policy.ErrDenied)
		}
		action = map[string]any{
			"changeSetId":            change.ID,
			"repositoryId":           repository.ID,
			"repositoryFullName":     repository.Owner + "/" + repository.Name,
			"repositoryConfigSha256": security.RepositoryConfigHash(repository),
			"pullRequestNumber":      change.PullRequestNumber,
			"headSha":                change.CommitSHA,
			"baseBranch":             repository.DefaultBranch,
		}
	}
	if actionType == "deploy_staging" || actionType == "deploy_production" || actionType == "rollback" {
		environment, err := s.store.GetEnvironment(ctx, fmt.Sprint(action["environmentId"]))
		if err != nil {
			return domain.Approval{}, err
		}
		if environment.ProjectID != incident.ProjectID || !environment.Protected {
			return domain.Approval{}, fmt.Errorf("%w: deployments require a protected environment belonging to the incident project", policy.ErrDenied)
		}
		artifact := fmt.Sprint(action["artifactRef"])
		change, err := s.store.FindChangeSetByArtifact(ctx, incident.ID, artifact)
		if actionType == "rollback" {
			change, err = s.store.FindProjectChangeSetByArtifact(ctx, incident.ProjectID, artifact)
		}
		if err != nil {
			return domain.Approval{}, fmt.Errorf("%w: artifact is not a known Echo change set for this project", policy.ErrDenied)
		}
		var environmentConfig struct {
			RepositoryID string `json:"repositoryId"`
		}
		if err := json.Unmarshal(environment.Config, &environmentConfig); err != nil || environmentConfig.RepositoryID == "" || environmentConfig.RepositoryID != change.RepositoryID {
			return domain.Approval{}, fmt.Errorf("%w: deployment workflow repository must match the approved change repository", policy.ErrDenied)
		}
		deploymentRepository, err := s.store.GetRepository(ctx, change.RepositoryID)
		if err != nil || deploymentRepository.ProjectID != incident.ProjectID || !deploymentRepository.Enabled || deploymentRepository.ReadOnly {
			return domain.Approval{}, fmt.Errorf("%w: deployment repository is not enabled for this project", policy.ErrDenied)
		}
		action["environmentConfigSha256"] = security.ConfigHash(environment.Config)
		action["repositoryId"] = change.RepositoryID
		action["repositoryConfigSha256"] = security.RepositoryConfigHash(deploymentRepository)
		action["branchName"] = change.BranchName
		if actionType == "deploy_production" {
			staging, err := s.store.GetDeployment(ctx, fmt.Sprint(action["stagingDeploymentId"]))
			if err != nil || staging.IncidentID != incident.ID || staging.ProjectID != incident.ProjectID || !strings.EqualFold(staging.Environment, "staging") || staging.Status != "verified" || staging.ArtifactRef != artifact {
				return domain.Approval{}, fmt.Errorf("%w: production requires the same artifact from this incident's verified staging deployment", policy.ErrDenied)
			}
		}
		if actionType == "rollback" {
			source, err := s.store.GetDeployment(ctx, fmt.Sprint(action["sourceDeploymentId"]))
			if err != nil || source.ProjectID != incident.ProjectID || source.Status != "verified" || source.ArtifactRef != artifact {
				return domain.Approval{}, fmt.Errorf("%w: rollback source must be a verified deployment of the exact artifact in this project", policy.ErrDenied)
			}
		}
	}
	raw, err := json.Marshal(action)
	if err != nil {
		return domain.Approval{}, err
	}
	plain, nonceHash, err := security.NewApprovalNonce()
	if err != nil {
		return domain.Approval{}, err
	}
	item, err := s.store.CreateApproval(ctx, domain.Approval{
		IncidentID:  incidentID,
		ActionType:  actionType,
		Action:      raw,
		RequestedBy: actor,
		ExpiresAt:   time.Now().UTC().Add(s.approvalTTL),
	}, nonceHash)
	if err != nil {
		return domain.Approval{}, err
	}
	item.DisplayNonce = plain
	_ = s.store.Audit(ctx, "admin", actor, "approval.request", "approval", item.ID, "succeeded", map[string]any{"actionType": actionType, "actionHash": item.ActionHash})
	if ownerJID, ownerErr := s.ownerJID(ctx); ownerErr == nil && ownerJID != "" {
		_ = s.store.EnqueueOutbound(ctx, ownerJID, security.DescribeApproval(actionType, incidentID, plain))
	}
	return item, nil
}

func (s *Service) ResolveApproval(ctx context.Context, approvalID, actor, resolution string) (domain.Approval, error) {
	item, err := s.store.ResolveApproval(ctx, approvalID, actor, resolution)
	outcome := "succeeded"
	if err != nil {
		outcome = "failed"
	}
	_ = s.store.Audit(ctx, "admin", actor, "approval."+resolution, "approval", approvalID, outcome, map[string]any{"error": errorString(err)})
	return item, err
}

func (s *Service) ReceiveOwnerCommand(ctx context.Context, msg domain.InboundMessage) (string, bool) {
	ownerJID, err := s.ownerJID(ctx)
	if err != nil || ownerJID == "" || (!sameJID(msg.SenderJID, ownerJID) && !sameJID(msg.SenderAltJID, ownerJID)) {
		return "", false
	}
	parts := strings.Fields(strings.TrimSpace(msg.Body))
	if len(parts) != 2 {
		return "", false
	}
	resolution := ""
	switch strings.ToLower(parts[0]) {
	case "approve", "confirm":
		resolution = "approved"
	case "reject", "deny":
		resolution = "rejected"
	default:
		return "", false
	}
	approvalID, err := s.store.FindPendingApprovalByNonceHash(ctx, security.HashNonce(parts[1]))
	if err != nil {
		return "I could not find an active approval for that code. It may be incorrect or expired.", true
	}
	item, err := s.ResolveApproval(ctx, approvalID, msg.SenderJID, resolution)
	if err != nil {
		return "The approval could not be resolved safely. Please review it in the Echo control plane.", true
	}
	return approvalResolutionMessage(item, resolution), true
}

func approvalResolutionMessage(item domain.Approval, resolution string) string {
	base := fmt.Sprintf("Approval %s for %s (%s).", resolution, item.IncidentID, item.ActionType)
	if resolution != "approved" {
		return base + " No action will be taken."
	}
	switch item.ActionType {
	case "prepare_patch":
		return base + " Patch preparation is now queued. Echo is fetching the repository, preparing and validating a bounded change, and creating a draft pull request. This can take a few minutes; I will message you with the result or a terminal failure."
	case "deploy_staging":
		return base + " The staging deployment is now queued. I will message you when verification completes or if it fails."
	case "deploy_production":
		return base + " The production deployment is now queued. I will message you when it completes or if it fails."
	case "merge_production":
		return base + " The exact approved pull-request commit is queued for merge into the repository's default branch. Echo will monitor the existing main-branch deployment workflow and message you only after it verifies successfully or fails."
	case "rollback":
		return base + " The rollback is now queued. I will message you when it completes or if it fails."
	default:
		return base + " Echo will execute only the action bound to this approval and report the result."
	}
}

func (s *Service) ownerJID(ctx context.Context) (string, error) {
	if s.owners == nil {
		return "", nil
	}
	return s.owners.JID(ctx)
}

func sameJID(a, b string) bool { return normalizedJID(a) != "" && normalizedJID(a) == normalizedJID(b) }

func normalizedJID(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	at := strings.LastIndexByte(value, '@')
	if at <= 0 || at == len(value)-1 {
		return value
	}
	user := value[:at]
	if colon := strings.IndexByte(user, ':'); colon >= 0 {
		user = user[:colon]
	}
	return user + value[at:]
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

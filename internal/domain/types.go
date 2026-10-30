package domain

import (
	"encoding/json"
	"time"
)

type Project struct {
	ID          string    `json:"id"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type GroupBinding struct {
	ID              string    `json:"id"`
	ProjectID       string    `json:"projectId"`
	ProjectName     string    `json:"projectName,omitempty"`
	GroupJID        string    `json:"groupJid"`
	GroupName       string    `json:"groupName"`
	MentionRequired bool      `json:"mentionRequired"`
	AckTemplate     string    `json:"ackTemplate"`
	Enabled         bool      `json:"enabled"`
	CreatedAt       time.Time `json:"createdAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type Repository struct {
	ID            string    `json:"id"`
	ProjectID     string    `json:"projectId"`
	Owner         string    `json:"owner"`
	Name          string    `json:"name"`
	Role          string    `json:"role"`
	DefaultBranch string    `json:"defaultBranch"`
	ReadOnly      bool      `json:"readOnly"`
	Enabled       bool      `json:"enabled"`
	TestCommand   string    `json:"testCommand,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type Environment struct {
	ID               string          `json:"id"`
	ProjectID        string          `json:"projectId"`
	Name             string          `json:"name"`
	Kind             string          `json:"kind"`
	RequiresApproval bool            `json:"requiresApproval"`
	Protected        bool            `json:"protected"`
	Config           json.RawMessage `json:"config"`
	CreatedAt        time.Time       `json:"createdAt"`
	UpdatedAt        time.Time       `json:"updatedAt"`
}

type Incident struct {
	ID              string     `json:"id"`
	ProjectID       string     `json:"projectId"`
	ProjectName     string     `json:"projectName,omitempty"`
	Status          string     `json:"status"`
	Severity        string     `json:"severity"`
	Summary         string     `json:"summary"`
	ReporterJID     string     `json:"reporterJid"`
	ReporterName    string     `json:"reporterName"`
	GroupJID        string     `json:"groupJid"`
	SourceMessageID string     `json:"sourceMessageId"`
	SourceBody      string     `json:"sourceBody"`
	AssignedTo      string     `json:"assignedTo,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
	ResolvedAt      *time.Time `json:"resolvedAt,omitempty"`
}

type Analysis struct {
	ID         string          `json:"id"`
	IncidentID string          `json:"incidentId"`
	Status     string          `json:"status"`
	Confidence string          `json:"confidence"`
	Summary    string          `json:"summary"`
	Evidence   json.RawMessage `json:"evidence"`
	Proposal   json.RawMessage `json:"proposal"`
	Model      string          `json:"model,omitempty"`
	CreatedAt  time.Time       `json:"createdAt"`
}

type ChangeSet struct {
	ID                string    `json:"id"`
	IncidentID        string    `json:"incidentId"`
	RepositoryID      string    `json:"repositoryId"`
	BranchName        string    `json:"branchName"`
	CommitSHA         string    `json:"commitSha"`
	PullRequestNumber int64     `json:"pullRequestNumber"`
	PullRequestURL    string    `json:"pullRequestUrl"`
	ArtifactRef       string    `json:"artifactRef"`
	Status            string    `json:"status"`
	DiffSHA256        string    `json:"diffSha256"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type Approval struct {
	ID           string          `json:"id"`
	IncidentID   string          `json:"incidentId"`
	ActionType   string          `json:"actionType"`
	ActionHash   string          `json:"actionHash"`
	Action       json.RawMessage `json:"action"`
	Status       string          `json:"status"`
	RequestedBy  string          `json:"requestedBy"`
	ResolvedBy   string          `json:"resolvedBy,omitempty"`
	ExpiresAt    time.Time       `json:"expiresAt"`
	CreatedAt    time.Time       `json:"createdAt"`
	ResolvedAt   *time.Time      `json:"resolvedAt,omitempty"`
	DisplayNonce string          `json:"displayNonce,omitempty"`
}

type Deployment struct {
	ID                 string     `json:"id"`
	IncidentID         string     `json:"incidentId"`
	ApprovalID         string     `json:"approvalId,omitempty"`
	ProjectID          string     `json:"projectId"`
	Environment        string     `json:"environment"`
	Operation          string     `json:"operation"`
	ArtifactRef        string     `json:"artifactRef"`
	SourceDeploymentID string     `json:"sourceDeploymentId,omitempty"`
	Status             string     `json:"status"`
	WorkflowURL        string     `json:"workflowUrl,omitempty"`
	Repository         string     `json:"repository"`
	Workflow           string     `json:"workflow"`
	ExternalRunID      int64      `json:"externalRunId,omitempty"`
	StartedAt          *time.Time `json:"startedAt,omitempty"`
	FinishedAt         *time.Time `json:"finishedAt,omitempty"`
	CreatedAt          time.Time  `json:"createdAt"`
}

type AuditEvent struct {
	ID           int64           `json:"id"`
	ActorType    string          `json:"actorType"`
	ActorID      string          `json:"actorId"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resourceType"`
	ResourceID   string          `json:"resourceId"`
	Outcome      string          `json:"outcome"`
	Metadata     json.RawMessage `json:"metadata"`
	CreatedAt    time.Time       `json:"createdAt"`
}

type Dashboard struct {
	OpenIncidents     int `json:"openIncidents"`
	PendingApprovals  int `json:"pendingApprovals"`
	RunningJobs       int `json:"runningJobs"`
	FailedDeployments int `json:"failedDeployments"`
	EnabledProjects   int `json:"enabledProjects"`
}

type InboundMessage struct {
	MessageID     string
	ChatJID       string
	SenderJID     string
	SenderAltJID  string
	SenderName    string
	Body          string
	IsGroup       bool
	MentionedJIDs []string
	Timestamp     time.Time
}

type WhatsAppGroup struct {
	JID              string `json:"jid"`
	Name             string `json:"name"`
	ParticipantCount int    `json:"participantCount"`
	Suspended        bool   `json:"suspended"`
}

type WhatsAppContact struct {
	ID           string    `json:"id"`
	JID          string    `json:"jid"`
	AlternateJID string    `json:"alternateJid,omitempty"`
	DisplayName  string    `json:"displayName"`
	MessageCount uint64    `json:"messageCount"`
	FirstSeenAt  time.Time `json:"firstSeenAt"`
	LastSeenAt   time.Time `json:"lastSeenAt"`
}

type OwnerSettings struct {
	Configured  bool      `json:"configured"`
	ContactID   string    `json:"contactId,omitempty"`
	DisplayName string    `json:"displayName,omitempty"`
	JID         string    `json:"jid,omitempty"`
	Source      string    `json:"source,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt,omitempty"`
}

type OutboundMessage struct {
	ID        string
	ChatJID   string
	Body      string
	CreatedAt time.Time
}

type Job struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Payload     json.RawMessage `json:"payload"`
	Status      string          `json:"status"`
	Attempts    int             `json:"attempts"`
	MaxAttempts int             `json:"maxAttempts"`
	LockedBy    string          `json:"lockedBy,omitempty"`
	LockedUntil *time.Time      `json:"lockedUntil,omitempty"`
	CreatedAt   time.Time       `json:"createdAt"`
}

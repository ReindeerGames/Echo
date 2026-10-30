package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ReindeerGames/echo/internal/ai"
	"github.com/ReindeerGames/echo/internal/briefing"
	"github.com/ReindeerGames/echo/internal/config"
	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/githubapp"
	"github.com/ReindeerGames/echo/internal/monitor"
	"github.com/ReindeerGames/echo/internal/owner"
	"github.com/ReindeerGames/echo/internal/policy"
	runtimestate "github.com/ReindeerGames/echo/internal/runtime"
	"github.com/ReindeerGames/echo/internal/store"
	"github.com/ReindeerGames/echo/internal/workflow"
	"github.com/ReindeerGames/echo/webui"
	"github.com/skip2/go-qrcode"
)

type Server struct {
	cfg         config.Config
	store       *store.Store
	readiness   readinessStore
	workflows   *workflow.Service
	owners      *owner.Service
	runtime     *runtimestate.State
	groupLister WhatsAppGroupLister
	pairer      WhatsAppPairer
	github      *githubapp.Client
	ai          *ai.Client
	auth        *auth
	http        *http.Server
}

type readinessStore interface {
	Ping(context.Context) error
	SchemaStatus(context.Context) (domain.SchemaStatus, error)
}

type WhatsAppGroupLister interface {
	ListGroups(context.Context) ([]domain.WhatsAppGroup, error)
}

type WhatsAppPairer interface {
	RequestPairing() error
}

func New(cfg config.Config, st *store.Store, workflows *workflow.Service, owners *owner.Service, state *runtimestate.State, github *githubapp.Client, aiClient *ai.Client, groups WhatsAppGroupLister) *Server {
	s := &Server{cfg: cfg, store: st, readiness: st, workflows: workflows, owners: owners, runtime: state, github: github, ai: aiClient, groupLister: groups, auth: newAuth(cfg.Security.AdminToken, cfg.Security.SessionSecret, cfg.Security.CookieSecure, cfg.Security.DevMode)}
	if pairer, ok := groups.(WhatsAppPairer); ok {
		s.pairer = pairer
	}
	mux := http.NewServeMux()
	s.routes(mux)
	s.http = &http.Server{Addr: cfg.HTTP.Address, Handler: securityHeaders(requestLog(mux)), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: cfg.HTTP.ReadTimeout, WriteTimeout: cfg.HTTP.WriteTimeout, IdleTimeout: cfg.HTTP.IdleTimeout}
	return s
}
func (s *Server) Run() error {
	slog.Info("Echo control plane listening", "address", s.cfg.HTTP.Address)
	err := s.http.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }

func (s *Server) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /readyz", s.ready)
	mux.HandleFunc("POST /api/v1/auth/login", s.auth.login)
	mux.HandleFunc("POST /webhooks/github", s.githubWebhook)
	mux.HandleFunc("POST /webhooks/monitor", s.monitorWebhook)
	protected := http.NewServeMux()
	protected.HandleFunc("POST /api/v1/auth/logout", s.auth.logout)
	protected.HandleFunc("GET /api/v1/bootstrap", s.bootstrap)
	protected.HandleFunc("GET /api/v1/runtime", s.runtimeStatus)
	protected.HandleFunc("GET /api/v1/dashboard", s.dashboard)
	protected.HandleFunc("GET /api/v1/settings/github", s.githubSettings)
	protected.HandleFunc("PUT /api/v1/settings/github", s.githubSettings)
	protected.HandleFunc("DELETE /api/v1/settings/github", s.githubSettings)
	protected.HandleFunc("GET /api/v1/github/repositories", s.githubRepositories)
	protected.HandleFunc("GET /api/v1/settings/ai", s.aiSettings)
	protected.HandleFunc("PUT /api/v1/settings/ai", s.aiSettings)
	protected.HandleFunc("DELETE /api/v1/settings/ai", s.aiSettings)
	protected.HandleFunc("GET /api/v1/settings/owner", s.ownerSettings)
	protected.HandleFunc("PUT /api/v1/settings/owner", s.ownerSettings)
	protected.HandleFunc("DELETE /api/v1/settings/owner", s.ownerSettings)
	protected.HandleFunc("GET /api/v1/settings/briefing", s.briefingSettings)
	protected.HandleFunc("PUT /api/v1/settings/briefing", s.briefingSettings)
	protected.HandleFunc("GET /api/v1/projects", s.projects)
	protected.HandleFunc("POST /api/v1/projects", s.projects)
	protected.HandleFunc("GET /api/v1/groups", s.groups)
	protected.HandleFunc("POST /api/v1/groups", s.groups)
	protected.HandleFunc("GET /api/v1/repositories", s.repositories)
	protected.HandleFunc("POST /api/v1/repositories", s.repositories)
	protected.HandleFunc("GET /api/v1/environments", s.environments)
	protected.HandleFunc("POST /api/v1/environments", s.environments)
	protected.HandleFunc("GET /api/v1/services", s.services)
	protected.HandleFunc("POST /api/v1/services", s.services)
	protected.HandleFunc("GET /api/v1/monitor/events", s.monitorEvents)
	protected.HandleFunc("GET /api/v1/monitor/alerts", s.serviceAlerts)
	protected.HandleFunc("GET /api/v1/assistant/conversations", s.conversations)
	protected.HandleFunc("GET /api/v1/assistant/conversations/{id}", s.conversation)
	protected.HandleFunc("GET /api/v1/jobs", s.jobs)
	protected.HandleFunc("GET /api/v1/deployments", s.deployments)
	protected.HandleFunc("GET /api/v1/incidents", s.incidents)
	protected.HandleFunc("GET /api/v1/incidents/{id}", s.incident)
	protected.HandleFunc("GET /api/v1/approvals", s.approvals)
	protected.HandleFunc("POST /api/v1/incidents/{id}/approvals", s.requestApproval)
	protected.HandleFunc("POST /api/v1/approvals/{id}/{resolution}", s.resolveApproval)
	protected.HandleFunc("GET /api/v1/audit", s.audit)
	protected.HandleFunc("GET /api/v1/whatsapp", s.whatsapp)
	protected.HandleFunc("POST /api/v1/whatsapp/link", s.whatsappLink)
	protected.HandleFunc("GET /api/v1/whatsapp/groups", s.whatsappGroups)
	protected.HandleFunc("GET /api/v1/whatsapp/contacts", s.whatsappContacts)
	protected.HandleFunc("GET /api/v1/whatsapp/qr.png", s.whatsappQR)
	mux.Handle("/api/", s.auth.require(protected))
	mux.Handle("/", webui.Handler())
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "service": "echo-v2"})
}
func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	checks := map[string]any{"database": "unavailable", "schema": "unavailable", "components": map[string]string{}}
	ready := true
	if s.readiness == nil || s.readiness.Ping(ctx) != nil {
		ready = false
	} else {
		checks["database"] = "ready"
		schema, err := s.readiness.SchemaStatus(ctx)
		if err != nil {
			ready = false
		} else {
			checks["schema"] = schema
			if !schema.Current {
				ready = false
			}
		}
	}
	componentReady := false
	if s.runtime != nil {
		var componentChecks map[string]string
		componentReady, componentChecks = s.runtime.ComponentReadiness(s.expectedComponents())
		checks["components"] = componentChecks
	}
	if !componentReady {
		ready = false
	}
	status := http.StatusOK
	state := "ready"
	if !ready {
		status = http.StatusServiceUnavailable
		state = "not_ready"
	}
	writeJSON(w, status, map[string]any{"status": state, "checks": checks})
}

func (s *Server) expectedComponents() []string {
	expected := []string{"monitor-watchdog", "worker-watchdog"}
	if s.cfg.Worker.Enabled {
		expected = append(expected, "worker")
	}
	if s.cfg.WhatsApp.Enabled {
		expected = append(expected, "whatsapp-gateway", "daily-brief-scheduler")
	}
	return expected
}

func (s *Server) runtimeStatus(w http.ResponseWriter, _ *http.Request) {
	components := []runtimestate.ComponentState{}
	ready := false
	checks := map[string]string{}
	if s.runtime != nil {
		components = s.runtime.Components()
		ready, checks = s.runtime.ComponentReadiness(s.expectedComponents())
	}
	writeJSON(w, http.StatusOK, map[string]any{"ready": ready, "checks": checks, "components": components})
}
func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	githubConfigured := false
	if s.github != nil {
		status, err := s.github.TokenStatus(r.Context())
		githubConfigured = err == nil && status.Configured
	}
	ownerConfigured := false
	if s.owners != nil {
		status, ownerErr := s.owners.Current(r.Context())
		ownerConfigured = ownerErr == nil && status.Configured
	}
	aiConfigured := false
	if s.ai != nil {
		status, aiErr := s.ai.Status(r.Context())
		aiConfigured = aiErr == nil && status.Configured
	}
	writeJSON(w, http.StatusOK, map[string]any{"actor": actorFrom(r.Context()), "environment": s.cfg.Environment, "features": map[string]any{"whatsapp": s.cfg.WhatsApp.Enabled, "ai": aiConfigured, "github": githubConfigured, "owner": ownerConfigured, "worker": s.cfg.Worker.Enabled, "monitor": s.cfg.Monitor.WebhookSecret != ""}, "csrf": csrfFromRequest(r)})
}
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.Dashboard(r.Context())
	respond(w, item, err)
}

func (s *Server) githubSettings(w http.ResponseWriter, r *http.Request) {
	if s.github == nil {
		writeError(w, http.StatusServiceUnavailable, "github_unavailable", "GitHub integration is unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		status, err := s.github.TokenStatus(r.Context())
		respond(w, status, err)
	case http.MethodPut:
		var input struct {
			Token string `json:"token"`
		}
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_github_token", err.Error())
			return
		}
		status, err := s.github.ConfigureToken(r.Context(), input.Token)
		if errors.Is(err, githubapp.ErrInvalidToken) {
			writeError(w, http.StatusBadRequest, "github_token_rejected", err.Error())
			return
		}
		if err == nil {
			_ = s.store.Audit(r.Context(), "admin", actorFrom(r.Context()), "github.token_saved", "setting", "github", "succeeded", map[string]any{"account": status.Account})
		}
		respond(w, status, err)
	case http.MethodDelete:
		err := s.github.RemoveToken(r.Context())
		if err == nil {
			_ = s.store.Audit(r.Context(), "admin", actorFrom(r.Context()), "github.token_removed", "setting", "github", "succeeded", nil)
		}
		if err != nil {
			respond(w, nil, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) githubRepositories(w http.ResponseWriter, r *http.Request) {
	if s.github == nil {
		writeError(w, http.StatusServiceUnavailable, "github_unavailable", "GitHub integration is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	discovery, err := s.github.ListAccessibleRepositories(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, "github_discovery_failed", "GitHub repository discovery failed; verify the token and try again")
		return
	}
	writeJSON(w, http.StatusOK, discovery)
}

func (s *Server) aiSettings(w http.ResponseWriter, r *http.Request) {
	if s.ai == nil {
		writeError(w, http.StatusServiceUnavailable, "ai_settings_unavailable", "AI settings are unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		status, err := s.ai.Status(r.Context())
		respond(w, status, err)
	case http.MethodPut:
		var input struct {
			APIKey string `json:"apiKey"`
			Model  string `json:"model"`
		}
		if err := decodeJSON(r, &input); err != nil || strings.TrimSpace(input.APIKey) == "" {
			writeError(w, http.StatusBadRequest, "invalid_ai_settings", "OpenAI API key is required")
			return
		}
		status, err := s.ai.Configure(r.Context(), input.APIKey, "https://api.openai.com/v1", input.Model)
		if err != nil {
			writeError(w, http.StatusBadRequest, "openai_settings_rejected", err.Error())
			return
		}
		_ = s.store.Audit(r.Context(), "admin", actorFrom(r.Context()), "ai.settings_saved", "setting", "openai", "succeeded", map[string]any{"model": status.Model})
		writeJSON(w, http.StatusOK, status)
	case http.MethodDelete:
		err := s.ai.Remove(r.Context())
		if err == nil {
			_ = s.store.Audit(r.Context(), "admin", actorFrom(r.Context()), "ai.settings_removed", "setting", "openai", "succeeded", nil)
		}
		if err != nil {
			respond(w, nil, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) whatsappContacts(w http.ResponseWriter, r *http.Request) {
	contacts, err := s.store.ListWhatsAppContacts(r.Context())
	respond(w, contacts, err)
}

func (s *Server) ownerSettings(w http.ResponseWriter, r *http.Request) {
	if s.owners == nil {
		writeError(w, http.StatusServiceUnavailable, "owner_settings_unavailable", "owner settings are unavailable")
		return
	}
	switch r.Method {
	case http.MethodGet:
		settings, err := s.owners.Current(r.Context())
		respond(w, settings, err)
	case http.MethodPut:
		var input struct {
			ContactID string `json:"contactId"`
		}
		if err := decodeJSON(r, &input); err != nil || strings.TrimSpace(input.ContactID) == "" {
			writeError(w, http.StatusBadRequest, "invalid_owner_contact", "contactId is required")
			return
		}
		settings, err := s.owners.Set(r.Context(), input.ContactID, actorFrom(r.Context()))
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "unknown_owner_contact", "select a contact that has messaged Echo directly")
			return
		}
		if err == nil {
			_ = s.store.Audit(r.Context(), "admin", actorFrom(r.Context()), "owner.selected", "whatsapp_contact", input.ContactID, "succeeded", map[string]any{"displayName": settings.DisplayName})
		}
		respond(w, settings, err)
	case http.MethodDelete:
		err := s.owners.Clear(r.Context())
		if err == nil {
			_ = s.store.Audit(r.Context(), "admin", actorFrom(r.Context()), "owner.cleared", "setting", "owner", "succeeded", nil)
		}
		if err != nil {
			respond(w, nil, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) briefingSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		settings, err := s.store.GetBriefingSettings(r.Context())
		respond(w, settings, err)
	case http.MethodPut:
		var input domain.BriefingSettings
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_briefing_settings", err.Error())
			return
		}
		if err := briefing.Validate(input); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_briefing_settings", err.Error())
			return
		}
		settings, err := s.store.SaveBriefingSettings(r.Context(), input)
		if err == nil {
			_ = s.store.Audit(r.Context(), "admin", actorFrom(r.Context()), "briefing.settings_saved", "setting", "daily_brief", "succeeded", map[string]any{"enabled": settings.Enabled, "localTime": settings.LocalTime, "timezone": settings.Timezone})
		}
		respond(w, settings, err)
	}
}

func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		items, err := s.store.ListProjects(r.Context())
		respond(w, items, err)
		return
	}
	var item domain.Project
	if err := decodeJSON(r, &item); err != nil {
		writeError(w, 400, "invalid_project", err.Error())
		return
	}
	item.Key = slug(item.Key)
	if item.Key == "" || strings.TrimSpace(item.Name) == "" {
		writeError(w, 400, "invalid_project", "key and name are required")
		return
	}
	saved, err := s.store.SaveProject(r.Context(), item)
	if err == nil {
		_ = s.store.Audit(r.Context(), "admin", actorFrom(r.Context()), "project.saved", "project", saved.ID, "succeeded", map[string]any{"key": saved.Key})
	}
	respondStatus(w, http.StatusCreated, saved, err)
}
func (s *Server) groups(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		items, err := s.store.ListGroupBindings(r.Context())
		respond(w, items, err)
		return
	}
	var item domain.GroupBinding
	if err := decodeJSON(r, &item); err != nil {
		writeError(w, 400, "invalid_group", err.Error())
		return
	}
	if item.ProjectID == "" || item.GroupJID == "" || item.GroupName == "" || !strings.HasSuffix(strings.ToLower(item.GroupJID), "@g.us") {
		writeError(w, 400, "invalid_group", "projectId, groupName and a WhatsApp group JID ending in @g.us are required")
		return
	}
	saved, err := s.store.SaveGroupBinding(r.Context(), item)
	if err == nil {
		_ = s.store.Audit(r.Context(), "admin", actorFrom(r.Context()), "group.saved", "group_binding", saved.ID, "succeeded", map[string]any{"groupJid": saved.GroupJID})
	}
	respondStatus(w, http.StatusCreated, saved, err)
}
func (s *Server) repositories(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		items, err := s.store.ListRepositories(r.Context(), r.URL.Query().Get("projectId"))
		respond(w, items, err)
		return
	}
	var item domain.Repository
	if err := decodeJSON(r, &item); err != nil {
		writeError(w, 400, "invalid_repository", err.Error())
		return
	}
	if item.ProjectID == "" || item.Owner == "" || item.Name == "" {
		writeError(w, 400, "invalid_repository", "projectId, owner and name are required")
		return
	}
	saved, err := s.store.SaveRepository(r.Context(), item)
	if err == nil {
		_ = s.store.Audit(r.Context(), "admin", actorFrom(r.Context()), "repository.saved", "repository", saved.ID, "succeeded", map[string]any{"repository": saved.Owner + "/" + saved.Name, "readOnly": saved.ReadOnly})
	}
	respondStatus(w, http.StatusCreated, saved, err)
}
func (s *Server) environments(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		items, err := s.store.ListEnvironments(r.Context(), r.URL.Query().Get("projectId"))
		respond(w, items, err)
		return
	}
	var item domain.Environment
	if err := decodeJSON(r, &item); err != nil {
		writeError(w, 400, "invalid_environment", err.Error())
		return
	}
	if item.ProjectID == "" || item.Name == "" || !json.Valid(item.Config) {
		writeError(w, 400, "invalid_environment", "projectId, name and valid config are required")
		return
	}
	if item.Kind != "" && item.Kind != "github_actions" {
		writeError(w, 400, "invalid_environment", "only the github_actions deployment adapter is currently supported")
		return
	}
	if item.Kind == "" || item.Kind == "github_actions" {
		var adapter struct {
			RepositoryID string            `json:"repositoryId"`
			Workflow     string            `json:"workflow"`
			Inputs       map[string]string `json:"inputs"`
		}
		if err := json.Unmarshal(item.Config, &adapter); err != nil || adapter.RepositoryID == "" || adapter.Workflow == "" || strings.ContainsAny(adapter.Workflow, `/\\`) || (!strings.HasSuffix(adapter.Workflow, ".yml") && !strings.HasSuffix(adapter.Workflow, ".yaml")) {
			writeError(w, 400, "invalid_environment", "GitHub Actions config requires repositoryId and a workflow filename ending in .yml or .yaml")
			return
		}
		repository, err := s.store.GetRepository(r.Context(), adapter.RepositoryID)
		if err != nil || repository.ProjectID != item.ProjectID || !repository.Enabled {
			writeError(w, 400, "invalid_environment", "deployment repository must be enabled and belong to the selected project")
			return
		}
	}
	saved, err := s.store.SaveEnvironment(r.Context(), item)
	if err == nil {
		_ = s.store.Audit(r.Context(), "admin", actorFrom(r.Context()), "environment.saved", "environment", saved.ID, "succeeded", map[string]any{"name": saved.Name, "protected": saved.Protected})
	}
	respondStatus(w, http.StatusCreated, saved, err)
}

func (s *Server) services(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		items, err := s.store.ListServices(r.Context(), r.URL.Query().Get("projectId"))
		respond(w, items, err)
		return
	}
	var item domain.Service
	if err := decodeJSON(r, &item); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_service", err.Error())
		return
	}
	item.Key = slug(item.Key)
	if item.ProjectID == "" || item.Key == "" || strings.TrimSpace(item.Name) == "" || !json.Valid(item.Metadata) {
		writeError(w, http.StatusBadRequest, "invalid_service", "projectId, key, name, and valid metadata are required")
		return
	}
	if !oneOf(item.Kind, "", "application", "worker", "database", "platform", "external") {
		writeError(w, http.StatusBadRequest, "invalid_service", "kind must be application, worker, database, platform, or external")
		return
	}
	if !oneOf(item.Criticality, "", "low", "medium", "high", "critical") {
		writeError(w, http.StatusBadRequest, "invalid_service", "criticality must be low, medium, high, or critical")
		return
	}
	if item.HeartbeatTimeoutSeconds != 0 && (item.HeartbeatTimeoutSeconds < 60 || item.HeartbeatTimeoutSeconds > 86400) {
		writeError(w, http.StatusBadRequest, "invalid_service", "heartbeatTimeoutSeconds must be zero (disabled) or between 60 and 86400")
		return
	}
	saved, err := s.store.SaveService(r.Context(), item)
	if err == nil {
		_ = s.store.Audit(r.Context(), "admin", actorFrom(r.Context()), "service.saved", "service", saved.ID, "succeeded", map[string]any{"key": saved.Key, "criticality": saved.Criticality})
	}
	respondStatus(w, http.StatusCreated, saved, err)
}

func (s *Server) monitorEvents(w http.ResponseWriter, r *http.Request) {
	limit, err := monitorEventLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_limit", err.Error())
		return
	}
	items, err := s.store.ListMonitorEvents(r.Context(), r.URL.Query().Get("serviceId"), limit)
	if r.URL.Query().Get("includeDetails") == "false" {
		for index := range items {
			items[index].Details = nil
		}
	}
	respond(w, items, err)
}

func monitorEventLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 50, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 || value > 200 {
		return 0, errors.New("limit must be between 1 and 200")
	}
	return value, nil
}

func (s *Server) serviceAlerts(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListServiceAlerts(r.Context(), r.URL.Query().Get("status"))
	respond(w, items, err)
}

func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status != "" && !oneOf(status, "queued", "running", "failed", "dead", "complete") {
		writeError(w, http.StatusBadRequest, "invalid_status", "status must be queued, running, failed, dead, or complete")
		return
	}
	items, err := s.store.ListJobs(r.Context(), status, 100)
	respond(w, items, err)
}

func (s *Server) conversations(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListConversations(r.Context(), 50)
	respond(w, items, err)
}

func (s *Server) conversation(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetConversation(r.Context(), r.PathValue("id"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	messages, err := s.store.ListConversationMessages(r.Context(), item.ID, 50)
	if err != nil {
		respond(w, nil, err)
		return
	}
	runs, err := s.store.ListToolRuns(r.Context(), item.ID, 200)
	respond(w, map[string]any{"conversation": item, "messages": messages, "toolRuns": runs}, err)
}
func (s *Server) deployments(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListDeployments(r.Context(), r.URL.Query().Get("incidentId"))
	respond(w, items, err)
}
func (s *Server) incidents(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListIncidents(r.Context(), 100)
	respond(w, items, err)
}
func (s *Server) incident(w http.ResponseWriter, r *http.Request) {
	item, err := s.store.GetIncident(r.Context(), r.PathValue("id"))
	if err != nil {
		respond(w, nil, err)
		return
	}
	analysis, analysisErr := s.store.LatestAnalysis(r.Context(), item.ID)
	payload := map[string]any{"incident": item}
	if analysisErr == nil {
		payload["analysis"] = analysis
	}
	if changes, err := s.store.ListChangeSets(r.Context(), item.ID); err == nil {
		payload["changeSets"] = changes
	}
	if deployments, err := s.store.ListDeployments(r.Context(), item.ID); err == nil {
		payload["deployments"] = deployments
	}
	if messages, err := s.store.ListIncidentMessages(r.Context(), item.ID, 50); err == nil {
		payload["messages"] = messages
	}
	if attachments, err := s.store.ListIncidentAttachments(r.Context(), item.ID); err == nil {
		for index := range attachments {
			attachments[index].Data = nil
		}
		payload["attachments"] = attachments
	}
	respond(w, payload, nil)
}
func (s *Server) approvals(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListApprovals(r.Context())
	respond(w, items, err)
}
func (s *Server) requestApproval(w http.ResponseWriter, r *http.Request) {
	var input struct {
		ActionType string         `json:"actionType"`
		Action     map[string]any `json:"action"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, 400, "invalid_approval", err.Error())
		return
	}
	item, err := s.workflows.RequestApproval(r.Context(), r.PathValue("id"), input.ActionType, actorFrom(r.Context()), input.Action)
	respondStatus(w, http.StatusCreated, item, err)
}
func (s *Server) resolveApproval(w http.ResponseWriter, r *http.Request) {
	resolution := r.PathValue("resolution")
	if resolution != "approved" && resolution != "rejected" {
		writeError(w, 400, "invalid_resolution", "resolution must be approved or rejected")
		return
	}
	item, err := s.workflows.ResolveApproval(r.Context(), r.PathValue("id"), actorFrom(r.Context()), resolution)
	respond(w, item, err)
}
func (s *Server) audit(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.ListAudit(r.Context(), 200)
	respond(w, items, err)
}
func (s *Server) whatsapp(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.runtime.WhatsApp())
}
func (s *Server) whatsappLink(w http.ResponseWriter, _ *http.Request) {
	if s.pairer == nil {
		writeError(w, http.StatusServiceUnavailable, "whatsapp_unavailable", "WhatsApp pairing is unavailable")
		return
	}
	if err := s.pairer.RequestPairing(); err != nil {
		writeError(w, http.StatusConflict, "whatsapp_already_linked", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, s.runtime.WhatsApp())
}
func (s *Server) whatsappGroups(w http.ResponseWriter, r *http.Request) {
	if s.groupLister == nil {
		writeError(w, http.StatusServiceUnavailable, "whatsapp_unavailable", "WhatsApp group discovery is unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	groups, err := s.groupLister.ListGroups(ctx)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "whatsapp_unavailable", "WhatsApp must be connected before groups can be discovered")
		return
	}
	writeJSON(w, http.StatusOK, groups)
}
func (s *Server) whatsappQR(w http.ResponseWriter, _ *http.Request) {
	value := s.runtime.WhatsApp().QRCode
	if value == "" {
		writeError(w, http.StatusNotFound, "qr_unavailable", "no pairing QR code is currently active")
		return
	}
	png, err := qrcode.Encode(value, qrcode.Medium, 420)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "qr_error", "could not render pairing QR code")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
}

func (s *Server) githubWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.GitHub.WebhookSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "webhook_disabled", "GitHub webhook secret is not configured")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		writeError(w, 400, "invalid_body", "could not read webhook")
		return
	}
	if !verifyGitHubSignature(raw, r.Header.Get("X-Hub-Signature-256"), s.cfg.GitHub.WebhookSecret) {
		writeError(w, http.StatusUnauthorized, "invalid_signature", "webhook signature is invalid")
		return
	}
	if r.Header.Get("X-GitHub-Event") != "workflow_run" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	var event struct {
		Action     string `json:"action"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		WorkflowRun struct {
			ID         int64  `json:"id"`
			HeadSHA    string `json:"head_sha"`
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HTMLURL    string `json:"html_url"`
			Path       string `json:"path"`
			Name       string `json:"name"`
		} `json:"workflow_run"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		writeError(w, 400, "invalid_payload", "invalid workflow_run payload")
		return
	}
	if event.Action != "completed" || event.WorkflowRun.Status != "completed" {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	workflow := event.WorkflowRun.Path
	if slash := strings.LastIndex(workflow, "/"); slash >= 0 {
		workflow = workflow[slash+1:]
	}
	deployment, err := s.store.CompleteDeploymentFromWorkflow(r.Context(), event.Repository.FullName, workflow, event.WorkflowRun.HeadSHA, event.WorkflowRun.ID, event.WorkflowRun.HTMLURL, event.WorkflowRun.Conclusion == "success")
	if errors.Is(err, store.ErrNotFound) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if err != nil {
		writeError(w, 500, "webhook_processing_failed", "deployment result could not be recorded")
		return
	}
	outcome := "succeeded"
	if deployment.Status != "verified" {
		outcome = "failed"
	}
	_ = s.store.Audit(r.Context(), "system", "github", "deployment.workflow_completed", "deployment", deployment.ID, outcome, map[string]any{"runId": event.WorkflowRun.ID, "conclusion": event.WorkflowRun.Conclusion, "url": event.WorkflowRun.HTMLURL})
	ownerJID := ""
	if s.owners != nil {
		ownerJID, _ = s.owners.JID(r.Context())
	}
	if ownerJID != "" {
		body := fmt.Sprintf("Echo deployment result for %s: %s is %s.\nArtifact: %s\nWorkflow: %s", deployment.IncidentID, deployment.Environment, deployment.Status, deployment.ArtifactRef, event.WorkflowRun.HTMLURL)
		_ = s.store.EnqueueOutbound(r.Context(), ownerJID, body)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) monitorWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Monitor.WebhookSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "monitor_webhook_disabled", "monitor webhook secret is not configured")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, monitor.MaxBodyBytes+1))
	if err != nil || len(raw) > monitor.MaxBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "invalid_monitor_event", "monitor event exceeds the one MiB limit")
		return
	}
	if !monitor.VerifySignature(raw, r.Header.Get("X-Echo-Signature-256"), s.cfg.Monitor.WebhookSecret) {
		writeError(w, http.StatusUnauthorized, "invalid_signature", "monitor event signature is invalid")
		return
	}
	var event domain.MonitorEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_monitor_event", "monitor event is not valid JSON")
		return
	}
	event, err = monitor.Normalize(event, time.Now().UTC())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_monitor_event", err.Error())
		return
	}
	service, err := s.store.GetService(r.Context(), event.ServiceID)
	if err != nil || !service.Enabled {
		writeError(w, http.StatusNotFound, "service_not_found", "enabled service was not found")
		return
	}
	ownerJID := ""
	if s.owners != nil {
		ownerJID, err = s.owners.JID(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, "monitor_event_failed", "monitor notification owner could not be resolved")
			return
		}
	}
	result, err := s.store.RecordMonitorEvent(r.Context(), event, ownerJID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "monitor_event_failed", "monitor event could not be recorded")
		return
	}
	if !result.Duplicate {
		_ = s.store.Audit(r.Context(), "system", event.Source, "monitor.event_recorded", "service", event.ServiceID, "succeeded", map[string]any{"eventId": event.EventID, "status": event.Status, "severity": event.Severity})
	}
	writeJSON(w, http.StatusAccepted, result)
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func verifyGitHubSignature(body []byte, header, secret string) bool {
	if !strings.HasPrefix(header, "sha256=") {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	return hmac.Equal(provided, mac.Sum(nil))
}

func respond(w http.ResponseWriter, value any, err error) {
	respondStatus(w, http.StatusOK, value, err)
}
func respondStatus(w http.ResponseWriter, status int, value any, err error) {
	if err == nil {
		writeJSON(w, status, value)
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, 404, "not_found", "resource not found")
		return
	}
	if errors.Is(err, policy.ErrDenied) {
		writeError(w, http.StatusConflict, "policy_denied", err.Error())
		return
	}
	slog.Error("API request failed", "error", err)
	writeError(w, 500, "internal_error", "the request could not be completed")
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}
func decodeJSON(r *http.Request, value any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, (1<<20)+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("request must contain one JSON object")
	}
	return nil
}
func csrfFromRequest(r *http.Request) string {
	cookie, err := r.Cookie("echo_csrf")
	if err != nil {
		return ""
	}
	return cookie.Value
}
func slug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out strings.Builder
	dash := false
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			out.WriteRune(r)
			dash = false
		} else if !dash && out.Len() > 0 {
			out.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(out.String(), "-")
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}
func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		next.ServeHTTP(w, r)
		slog.Debug("http request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(started))
	})
}

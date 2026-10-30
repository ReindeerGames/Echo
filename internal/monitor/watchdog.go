package monitor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
)

type HeartbeatStore interface {
	ListStaleServiceHeartbeats(context.Context, time.Time) ([]domain.StaleServiceHeartbeat, error)
	ResolveInactiveHeartbeatAlerts(context.Context, time.Time, ...string) ([]domain.AlertTransition, error)
	RecordMonitorEvent(context.Context, domain.MonitorEvent, ...string) (domain.MonitorResult, error)
	QueueDueServiceAlertNotifications(context.Context, time.Time, time.Duration, string, int) ([]domain.AlertTransition, error)
	Audit(context.Context, string, string, string, string, string, string, any) error
}

type OwnerLookup interface {
	JID(context.Context) (string, error)
}

type Watchdog struct {
	store            HeartbeatStore
	owners           OwnerLookup
	interval         time.Duration
	reminderInterval time.Duration
	now              func() time.Time
}

func NewWatchdog(store HeartbeatStore, owners OwnerLookup, interval time.Duration, reminder ...time.Duration) *Watchdog {
	if interval < 5*time.Second || interval > 5*time.Minute {
		interval = 30 * time.Second
	}
	reminderInterval := 4 * time.Hour
	if len(reminder) > 0 {
		reminderInterval = reminder[0]
	}
	if reminderInterval != 0 && (reminderInterval < 15*time.Minute || reminderInterval > 7*24*time.Hour) {
		reminderInterval = 4 * time.Hour
	}
	return &Watchdog{store: store, owners: owners, interval: interval, reminderInterval: reminderInterval, now: func() time.Time { return time.Now().UTC() }}
}

func (w *Watchdog) Run(ctx context.Context) error {
	if _, err := w.Sweep(ctx, w.now()); err != nil {
		return err
	}
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := w.Sweep(ctx, w.now()); err != nil {
				return err
			}
		}
	}
}

func (w *Watchdog) Sweep(ctx context.Context, now time.Time) ([]domain.AlertTransition, error) {
	ownerJID := ""
	if w.owners != nil {
		var err error
		ownerJID, err = w.owners.JID(ctx)
		if err != nil {
			return nil, fmt.Errorf("resolve monitor notification owner: %w", err)
		}
	}
	transitions, err := w.store.ResolveInactiveHeartbeatAlerts(ctx, now, ownerJID)
	if err != nil {
		return nil, fmt.Errorf("resolve inactive heartbeat alerts: %w", err)
	}
	for _, transition := range transitions {
		_ = w.store.Audit(ctx, "system", "echo-watchdog", "monitor.heartbeat_policy_disabled", "service", transition.Alert.ServiceID, "succeeded", map[string]any{"alertId": transition.Alert.ID})
	}
	stale, err := w.store.ListStaleServiceHeartbeats(ctx, now)
	if err != nil {
		return transitions, fmt.Errorf("list stale service heartbeats: %w", err)
	}
	var sweepErrors []error
	for _, heartbeat := range stale {
		event := staleHeartbeatEvent(heartbeat, now)
		result, recordErr := w.store.RecordMonitorEvent(ctx, event, ownerJID)
		if recordErr != nil {
			sweepErrors = append(sweepErrors, fmt.Errorf("record stale heartbeat for %s: %w", heartbeat.ServiceID, recordErr))
			continue
		}
		if result.Duplicate {
			continue
		}
		transitions = append(transitions, result.Transitions...)
		_ = w.store.Audit(ctx, "system", "echo-watchdog", "monitor.heartbeat_stale", "service", heartbeat.ServiceID, "succeeded", map[string]any{"eventId": event.EventID, "lastEventId": heartbeat.LastEventID, "timeoutSeconds": heartbeat.TimeoutSeconds})
	}
	due, reminderErr := w.store.QueueDueServiceAlertNotifications(ctx, now, w.reminderInterval, ownerJID, 20)
	if reminderErr != nil {
		sweepErrors = append(sweepErrors, fmt.Errorf("queue due service alert notifications: %w", reminderErr))
	} else {
		transitions = append(transitions, due...)
		for _, transition := range due {
			_ = w.store.Audit(ctx, "system", "echo-watchdog", "monitor.alert_"+transition.Kind, "service_alert", transition.Alert.ID, "succeeded", map[string]any{"serviceId": transition.Alert.ServiceID, "reminderCount": transition.Alert.ReminderCount})
		}
	}
	return transitions, errors.Join(sweepErrors...)
}

func staleHeartbeatEvent(heartbeat domain.StaleServiceHeartbeat, now time.Time) domain.MonitorEvent {
	digest := sha256.Sum256([]byte(heartbeat.ServiceID + "\x00" + heartbeat.LastEventID + "\x00" + heartbeat.PolicyUpdatedAt.UTC().Format(time.RFC3339Nano)))
	eventID := "echo-watchdog:" + hex.EncodeToString(digest[:16])
	status, severity := "degraded", "warning"
	if heartbeat.Criticality == "high" || heartbeat.Criticality == "critical" {
		status, severity = "unhealthy", "critical"
	}
	summary := fmt.Sprintf("%s has not reported a heartbeat within %s", heartbeat.ServiceName, (time.Duration(heartbeat.TimeoutSeconds) * time.Second).String())
	details, _ := json.Marshal(map[string]any{
		"timeoutSeconds":  heartbeat.TimeoutSeconds,
		"lastEventId":     heartbeat.LastEventID,
		"lastObservedAt":  heartbeat.LastObservedAt,
		"policyUpdatedAt": heartbeat.PolicyUpdatedAt.UTC(),
		"policyReference": heartbeat.PolicyReference.UTC(),
	})
	return domain.MonitorEvent{
		EventID:     eventID,
		ServiceID:   heartbeat.ServiceID,
		Source:      "echo-watchdog",
		Status:      status,
		Severity:    severity,
		Summary:     summary,
		Fingerprint: "observer-silent",
		Details:     details,
		ObservedAt:  now.UTC(),
	}
}

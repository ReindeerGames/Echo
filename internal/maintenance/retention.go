package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

type RetentionStore interface {
	PruneConversationHistory(context.Context, time.Time) (int64, error)
	PruneIncidentAttachments(context.Context, time.Time) (int64, error)
	PruneMonitorEvents(context.Context, time.Time) (int64, error)
}

type RetentionResult struct {
	ConversationMessages int64
	IncidentAttachments  int64
	MonitorEvents        int64
}

type Retention struct {
	store              RetentionStore
	conversationWindow time.Duration
	attachmentWindow   time.Duration
	monitorWindow      time.Duration
	interval           time.Duration
	now                func() time.Time
}

func NewRetention(store RetentionStore, conversationWindow, attachmentWindow, monitorWindow, interval time.Duration) *Retention {
	if interval < time.Minute {
		interval = 24 * time.Hour
	}
	return &Retention{
		store: store, conversationWindow: conversationWindow, attachmentWindow: attachmentWindow,
		monitorWindow: monitorWindow, interval: interval, now: func() time.Time { return time.Now().UTC() },
	}
}

func (r *Retention) Run(ctx context.Context) error {
	if _, err := r.Sweep(ctx); err != nil {
		return err
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if _, err := r.Sweep(ctx); err != nil {
				return err
			}
		}
	}
}

func (r *Retention) Sweep(ctx context.Context) (RetentionResult, error) {
	now := r.now().UTC()
	result := RetentionResult{}
	conversationCount, conversationErr := r.store.PruneConversationHistory(ctx, now.Add(-r.conversationWindow))
	result.ConversationMessages = conversationCount
	attachmentCount, attachmentErr := r.store.PruneIncidentAttachments(ctx, now.Add(-r.attachmentWindow))
	result.IncidentAttachments = attachmentCount
	monitorCount, monitorErr := r.store.PruneMonitorEvents(ctx, now.Add(-r.monitorWindow))
	result.MonitorEvents = monitorCount
	if err := errors.Join(
		wrapRetentionError("conversation history", conversationErr),
		wrapRetentionError("incident attachments", attachmentErr),
		wrapRetentionError("monitoring events", monitorErr),
	); err != nil {
		return result, err
	}
	if result.ConversationMessages > 0 || result.IncidentAttachments > 0 || result.MonitorEvents > 0 {
		slog.Info("pruned expired retained data", "conversation_messages", result.ConversationMessages, "incident_attachments", result.IncidentAttachments, "monitor_events", result.MonitorEvents)
	}
	return result, nil
}

func wrapRetentionError(subject string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("prune %s: %w", subject, err)
}

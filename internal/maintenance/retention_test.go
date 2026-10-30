package maintenance

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeRetentionStore struct {
	conversationBefore time.Time
	attachmentBefore   time.Time
	monitorBefore      time.Time
	conversationCount  int64
	attachmentCount    int64
	monitorCount       int64
	conversationErr    error
	attachmentErr      error
	monitorErr         error
}

func (f *fakeRetentionStore) PruneConversationHistory(_ context.Context, before time.Time) (int64, error) {
	f.conversationBefore = before
	return f.conversationCount, f.conversationErr
}

func (f *fakeRetentionStore) PruneIncidentAttachments(_ context.Context, before time.Time) (int64, error) {
	f.attachmentBefore = before
	return f.attachmentCount, f.attachmentErr
}

func (f *fakeRetentionStore) PruneMonitorEvents(_ context.Context, before time.Time) (int64, error) {
	f.monitorBefore = before
	return f.monitorCount, f.monitorErr
}

func TestRetentionSweepUsesIndependentWindows(t *testing.T) {
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	backend := &fakeRetentionStore{conversationCount: 1, attachmentCount: 2, monitorCount: 3}
	retention := NewRetention(backend, 30*24*time.Hour, 7*24*time.Hour, 90*24*time.Hour, time.Hour)
	retention.now = func() time.Time { return now }
	result, err := retention.Sweep(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.ConversationMessages != 1 || result.IncidentAttachments != 2 || result.MonitorEvents != 3 {
		t.Fatalf("unexpected retention result: %#v", result)
	}
	if !backend.conversationBefore.Equal(now.Add(-30*24*time.Hour)) || !backend.attachmentBefore.Equal(now.Add(-7*24*time.Hour)) || !backend.monitorBefore.Equal(now.Add(-90*24*time.Hour)) {
		t.Fatalf("incorrect retention cutoffs: conversation=%s attachment=%s monitor=%s", backend.conversationBefore, backend.attachmentBefore, backend.monitorBefore)
	}
}

func TestRetentionSweepAttemptsEveryCollectionAndJoinsErrors(t *testing.T) {
	backend := &fakeRetentionStore{conversationErr: errors.New("conversation failed"), attachmentErr: errors.New("attachment failed"), monitorErr: errors.New("monitor failed")}
	retention := NewRetention(backend, time.Hour, time.Hour, time.Hour, time.Hour)
	_, err := retention.Sweep(context.Background())
	if err == nil || !errors.Is(err, backend.conversationErr) || !errors.Is(err, backend.attachmentErr) || !errors.Is(err, backend.monitorErr) {
		t.Fatalf("retention errors were not preserved: %v", err)
	}
	if backend.conversationBefore.IsZero() || backend.attachmentBefore.IsZero() || backend.monitorBefore.IsZero() {
		t.Fatal("retention stopped before attempting every collection")
	}
}

func TestRetentionRunFailsFastWhenInitialSweepFails(t *testing.T) {
	backend := &fakeRetentionStore{monitorErr: errors.New("monitor retention unavailable")}
	retention := NewRetention(backend, time.Hour, time.Hour, time.Hour, time.Hour)
	err := retention.Run(context.Background())
	if !errors.Is(err, backend.monitorErr) {
		t.Fatalf("initial retention failure was not returned: %v", err)
	}
	if backend.conversationBefore.IsZero() || backend.attachmentBefore.IsZero() || backend.monitorBefore.IsZero() {
		t.Fatal("initial retention run did not attempt every collection")
	}
}

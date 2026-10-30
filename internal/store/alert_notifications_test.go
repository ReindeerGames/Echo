package store

import (
	"strings"
	"testing"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/go-sql-driver/mysql"
)

func TestServiceAlertNotificationPartsRemainActionable(t *testing.T) {
	alert := domain.ServiceAlert{
		ID:          "alr_0000000000000000a1b2c3d4",
		ServiceName: "Checkout API",
		Fingerprint: "readiness",
		Status:      "open",
		Severity:    "critical",
		Summary:     "Readiness is unavailable",
	}
	header, item := serviceAlertNotificationParts("opened", alert)
	for _, expected := range []string{"Echo monitoring update · OPENED", "CRITICAL · Checkout API (readiness)", "Readiness is unavailable", "ack 0000a1b2c3d4", "service Checkout API", "investigate Checkout API"} {
		if !strings.Contains(header+item, expected) {
			t.Fatalf("notification is missing %q: header=%q item=%q", expected, header, item)
		}
	}
	if !monitorTransitionNotifiable("resolved") || !monitorTransitionNotifiable("reminder") || monitorTransitionNotifiable("updated") {
		t.Fatal("unexpected monitor transition notification policy")
	}
}

func TestMonitorTransactionRetryPolicyIsNarrow(t *testing.T) {
	for _, number := range []uint16{1205, 1213} {
		if !retryableTransactionError(&mysql.MySQLError{Number: number}) {
			t.Fatalf("MariaDB error %d should be retryable", number)
		}
	}
	if retryableTransactionError(&mysql.MySQLError{Number: 1062}) || retryableTransactionError(nil) {
		t.Fatal("non-lock database error was made retryable")
	}
}

func TestResolvedNotificationOmitsOpenAlertCommands(t *testing.T) {
	_, item := serviceAlertNotificationParts("resolved", domain.ServiceAlert{ServiceName: "Checkout API", Fingerprint: "readiness", Status: "resolved", Severity: "critical", Summary: "Readiness recovered"})
	if strings.Contains(item, "Reply") || !strings.Contains(item, "Readiness recovered") {
		t.Fatalf("unexpected resolved notification item %q", item)
	}
}

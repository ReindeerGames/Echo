package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/ReindeerGames/echo/internal/domain"
)

func TestValidateServiceRunbookBoundsContentAndReference(t *testing.T) {
	valid := domain.ServiceRunbook{ServiceID: "svc_0123456789abcdef01234567", Title: "Database recovery", Guidance: "Confirm replication state, then follow the approved failover process.", ReferenceURL: "https://docs.example.com/runbooks/database"}
	if err := ValidateServiceRunbook(valid); err != nil {
		t.Fatalf("valid runbook rejected: %v", err)
	}
	invalid := []domain.ServiceRunbook{
		{ServiceID: "short", Title: valid.Title, Guidance: valid.Guidance},
		{ServiceID: valid.ServiceID, Title: "x", Guidance: valid.Guidance},
		{ServiceID: valid.ServiceID, Title: valid.Title, Guidance: "x"},
		{ServiceID: valid.ServiceID, Title: valid.Title, Guidance: strings.Repeat("x", 4001)},
		{ServiceID: valid.ServiceID, Title: valid.Title, Guidance: valid.Guidance, ReferenceURL: "http://docs.example.com/runbook"},
		{ServiceID: valid.ServiceID, Title: valid.Title, Guidance: valid.Guidance, ReferenceURL: "https://user:secret@docs.example.com/runbook"},
		{ServiceID: valid.ServiceID, Title: valid.Title, Guidance: valid.Guidance, ReferenceURL: "https://docs.example.com/runbook?token=secret"},
		{ServiceID: valid.ServiceID, Title: valid.Title, Guidance: valid.Guidance, ReferenceURL: "https://docs.example.com/runbook#private"},
	}
	for _, item := range invalid {
		if err := ValidateServiceRunbook(item); !errors.Is(err, ErrInvalidRunbook) {
			t.Fatalf("invalid runbook accepted: %#v err=%v", item, err)
		}
	}
}

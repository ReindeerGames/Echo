package security

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/store"
)

type memorySecretStore struct {
	value    []byte
	metadata json.RawMessage
}

func (s *memorySecretStore) SaveEncryptedSetting(_ context.Context, _ string, value []byte, metadata json.RawMessage) (time.Time, error) {
	s.value = append([]byte(nil), value...)
	s.metadata = append(json.RawMessage(nil), metadata...)
	return time.Unix(1, 0), nil
}
func (s *memorySecretStore) GetEncryptedSetting(_ context.Context, _ string) ([]byte, json.RawMessage, time.Time, error) {
	if s.value == nil {
		return nil, nil, time.Time{}, store.ErrNotFound
	}
	return s.value, s.metadata, time.Unix(1, 0), nil
}
func (s *memorySecretStore) DeleteEncryptedSetting(_ context.Context, _ string) error {
	if s.value == nil {
		return store.ErrNotFound
	}
	s.value = nil
	return nil
}

func TestVaultEncryptsAndAuthenticatesSettings(t *testing.T) {
	backend := &memorySecretStore{}
	vault, err := NewVault(backend, "a-development-encryption-key-that-is-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Set(context.Background(), "github.token", "github_pat_secret", map[string]string{"account": "echo"}); err != nil {
		t.Fatal(err)
	}
	if string(backend.value) == "github_pat_secret" {
		t.Fatal("secret was stored as plaintext")
	}
	value, metadata, _, err := vault.Get(context.Background(), "github.token")
	if err != nil || value != "github_pat_secret" || !json.Valid(metadata) {
		t.Fatalf("value=%q metadata=%s err=%v", value, metadata, err)
	}
	backend.value[len(backend.value)-1] ^= 1
	if _, _, _, err := vault.Get(context.Background(), "github.token"); err == nil {
		t.Fatal("tampered ciphertext was accepted")
	}
}

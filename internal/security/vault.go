package security

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ReindeerGames/echo/internal/store"
)

var ErrSecretNotFound = errors.New("secret not found")

type encryptedSettingStore interface {
	SaveEncryptedSetting(context.Context, string, []byte, json.RawMessage) (time.Time, error)
	GetEncryptedSetting(context.Context, string) ([]byte, json.RawMessage, time.Time, error)
	DeleteEncryptedSetting(context.Context, string) error
}

type Vault struct {
	store encryptedSettingStore
	aead  cipher.AEAD
}

func NewVault(store encryptedSettingStore, masterKey string) (*Vault, error) {
	if len(masterKey) < 32 {
		return nil, errors.New("encryption key must contain at least 32 characters")
	}
	key := sha256.Sum256([]byte(masterKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{store: store, aead: aead}, nil
}

func (v *Vault) Set(ctx context.Context, name, value string, metadata any) (time.Time, error) {
	if value == "" {
		return time.Time{}, errors.New("secret value is empty")
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return time.Time{}, fmt.Errorf("generate encryption nonce: %w", err)
	}
	sealed := v.aead.Seal(nil, nonce, []byte(value), []byte(name))
	blob := append([]byte{1}, nonce...)
	blob = append(blob, sealed...)
	raw, err := json.Marshal(metadata)
	if err != nil {
		return time.Time{}, err
	}
	return v.store.SaveEncryptedSetting(ctx, name, blob, raw)
}

func (v *Vault) Get(ctx context.Context, name string) (string, json.RawMessage, time.Time, error) {
	blob, metadata, updated, err := v.store.GetEncryptedSetting(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil, time.Time{}, ErrSecretNotFound
	}
	if err != nil {
		return "", nil, time.Time{}, err
	}
	minimum := 1 + v.aead.NonceSize() + v.aead.Overhead()
	if len(blob) < minimum || blob[0] != 1 {
		return "", nil, time.Time{}, errors.New("encrypted setting has an unsupported format")
	}
	nonce := blob[1 : 1+v.aead.NonceSize()]
	plaintext, err := v.aead.Open(nil, nonce, blob[1+v.aead.NonceSize():], []byte(name))
	if err != nil {
		return "", nil, time.Time{}, errors.New("decrypt encrypted setting")
	}
	return string(plaintext), metadata, updated, nil
}

func (v *Vault) Delete(ctx context.Context, name string) error {
	if err := v.store.DeleteEncryptedSetting(ctx, name); errors.Is(err, store.ErrNotFound) {
		return ErrSecretNotFound
	} else if err != nil {
		return err
	}
	return nil
}

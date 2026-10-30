package ai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ReindeerGames/echo/internal/domain"
	"github.com/ReindeerGames/echo/internal/security"
	"github.com/ReindeerGames/echo/internal/store"
)

func TestDiagnoseParsesStructuredResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		var requestPayload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&requestPayload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if _, present := requestPayload["temperature"]; present {
			t.Error("temperature must be omitted for compatibility with models that only accept the provider default")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"status\":\"complete\",\"confidence\":\"medium\",\"summary\":\"Found validation regression\",\"evidence\":[],\"proposal\":{\"action\":\"patch\",\"repository\":\"owner/repo\",\"description\":\"Restore validation\",\"risks\":[],\"tests\":[]}}"}}]}`))
	}))
	defer server.Close()
	client := New(server.URL, "test", "test-model")
	result, err := client.Diagnose(context.Background(), "forms blank", "evidence")
	if err != nil {
		t.Fatal(err)
	}
	if result.Confidence != "medium" {
		t.Fatalf("unexpected result %#v", result)
	}
}

func TestDiagnoseIncludesOnlyBoundedSupportedImages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Messages) != 2 {
			t.Fatalf("unexpected messages %#v", payload.Messages)
		}
		content, ok := payload.Messages[1].Content.([]any)
		if !ok || len(content) != 3 {
			t.Fatalf("expected text and two bounded images, got %#v", payload.Messages[1].Content)
		}
		encoded, _ := json.Marshal(content)
		body := string(encoded)
		if !strings.Contains(body, `data:image/png;base64,cG5n`) || !strings.Contains(body, `data:image/jpeg;base64,anBlZw==`) || strings.Contains(body, "svg") || strings.Contains(body, "third") {
			t.Fatalf("unexpected multimodal content %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"status\":\"complete\",\"confidence\":\"medium\",\"summary\":\"Read screenshot\",\"evidence\":[],\"proposal\":{\"action\":\"none\",\"repository\":\"\",\"description\":\"\",\"risks\":[],\"tests\":[]}}"}}]}`))
	}))
	defer server.Close()
	attachments := []domain.IncidentAttachment{
		{Kind: "image", MIMEType: "image/png", Data: []byte("png")},
		{Kind: "image", MIMEType: "image/jpeg", Data: []byte("jpeg")},
		{Kind: "image", MIMEType: "image/webp", FileName: "third", Data: []byte("third")},
		{Kind: "image", MIMEType: "image/svg+xml", FileName: "svg", Data: []byte("svg")},
	}
	result, err := New(server.URL, "test", "test-model").Diagnose(context.Background(), "blank form", "repository evidence", attachments...)
	if err != nil || result.Summary != "Read screenshot" {
		t.Fatalf("unexpected multimodal diagnosis %#v err=%v", result, err)
	}
}

type aiSecretStore struct {
	value    []byte
	metadata json.RawMessage
	updated  time.Time
}

func (s *aiSecretStore) SaveEncryptedSetting(_ context.Context, _ string, value []byte, metadata json.RawMessage) (time.Time, error) {
	s.value, s.metadata, s.updated = append([]byte(nil), value...), append(json.RawMessage(nil), metadata...), time.Now().UTC()
	return s.updated, nil
}
func (s *aiSecretStore) GetEncryptedSetting(context.Context, string) ([]byte, json.RawMessage, time.Time, error) {
	if len(s.value) == 0 {
		return nil, nil, time.Time{}, store.ErrNotFound
	}
	return append([]byte(nil), s.value...), append(json.RawMessage(nil), s.metadata...), s.updated, nil
}
func (s *aiSecretStore) DeleteEncryptedSetting(context.Context, string) error {
	if len(s.value) == 0 {
		return store.ErrNotFound
	}
	s.value = nil
	return nil
}

func TestManagedSettingsAreValidatedEncryptedAndUsed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test-key-with-enough-characters" {
			t.Error("managed API key was not used")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/models/test-model":
			_, _ = w.Write([]byte(`{"id":"test-model"}`))
		case "/chat/completions":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"status\":\"complete\",\"confidence\":\"high\",\"summary\":\"Validated\",\"evidence\":[],\"proposal\":{\"action\":\"none\",\"repository\":\"\",\"description\":\"\",\"risks\":[],\"tests\":[]}}"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	secretStore := &aiSecretStore{}
	vault, err := security.NewVault(secretStore, "test-encryption-key-with-at-least-thirty-two-characters")
	if err != nil {
		t.Fatal(err)
	}
	client := NewManaged(vault, false, "", "", "")
	status, err := client.Configure(context.Background(), "sk-test-key-with-enough-characters", server.URL, "test-model")
	if err != nil || !status.Configured || status.Source != "application" {
		t.Fatalf("unexpected configuration status %#v err=%v", status, err)
	}
	if string(secretStore.value) == "sk-test-key-with-enough-characters" {
		t.Fatal("API key was stored in plaintext")
	}
	result, err := client.Diagnose(context.Background(), "issue", "evidence")
	if err != nil || result.Confidence != "high" {
		t.Fatalf("unexpected diagnosis %#v err=%v", result, err)
	}
}

func TestOpenAIDiagnosisUsesStrictSchema(t *testing.T) {
	format, ok := diagnosisResponseFormat("openai").(map[string]any)
	if !ok || format["type"] != "json_schema" {
		t.Fatalf("expected strict JSON schema response format, got %#v", format)
	}
	definition, ok := format["json_schema"].(map[string]any)
	if !ok || definition["strict"] != true {
		t.Fatalf("expected strict schema definition, got %#v", definition)
	}

	compatible, ok := diagnosisResponseFormat("openai-compatible").(map[string]string)
	if !ok || compatible["type"] != "json_object" {
		t.Fatalf("expected compatible fallback response format, got %#v", compatible)
	}
}

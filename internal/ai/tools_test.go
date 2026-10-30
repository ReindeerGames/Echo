package ai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRespondWithToolsPreservesOutputItemsAndReturnsAnswer(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		var payload struct {
			Store   bool              `json:"store"`
			Include []string          `json:"include"`
			Input   []json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Store {
			t.Fatal("owner investigation responses must not be stored remotely")
		}
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`{"id":"resp_1","status":"completed","output":[{"id":"rs_1","type":"reasoning","encrypted_content":"encrypted"},{"type":"function_call","call_id":"call_1","name":"get_status","arguments":"{}"}]}`))
			return
		}
		var joinedBuilder strings.Builder
		for _, item := range payload.Input {
			joinedBuilder.Write(item)
			joinedBuilder.WriteByte('\n')
		}
		joined := joinedBuilder.String()
		if !strings.Contains(joined, `"encrypted_content":"encrypted"`) || !strings.Contains(joined, `"type":"function_call_output"`) {
			t.Fatalf("stateless tool continuation dropped required items: %s", joined)
		}
		_, _ = w.Write([]byte(`{"id":"resp_2","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Everything is healthy."}]}]}`))
	}))
	defer server.Close()

	client := New(server.URL, "test-key", "test-model")
	client.fallback.Provider = "openai"
	tools := []ToolDefinition{FunctionTool("get_status", "Read current status.", objectSchema(nil, nil))}
	answer, executions, err := client.RespondWithTools(context.Background(), "Use read-only tools.", []ConversationMessage{{Role: "user", Content: "How are things?"}}, tools, func(_ context.Context, name string, arguments json.RawMessage) (string, error) {
		if name != "get_status" || string(arguments) != "{}" {
			t.Fatalf("unexpected call %s %s", name, arguments)
		}
		return `{"openAlerts":0}`, nil
	})
	if err != nil || answer != "Everything is healthy." || len(executions) != 1 {
		t.Fatalf("answer=%q executions=%#v err=%v", answer, executions, err)
	}
}

func TestRespondWithToolsRejectsCompatibleProvider(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	client := New(server.URL, "test-key", "test-model")
	_, _, err := client.RespondWithTools(context.Background(), "instructions", []ConversationMessage{{Role: "user", Content: "status"}}, []ToolDefinition{FunctionTool("get_status", "status", objectSchema(nil, nil))}, func(context.Context, string, json.RawMessage) (string, error) { return "{}", nil })
	if !errors.Is(err, ErrResponsesUnsupported) {
		t.Fatalf("expected unsupported provider error, got %v", err)
	}
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}

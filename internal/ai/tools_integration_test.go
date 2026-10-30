package ai

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestLocalResponsesToolLoop(t *testing.T) {
	baseURL := os.Getenv("ECHO_TEST_AI_URL")
	if baseURL == "" {
		t.Skip("ECHO_TEST_AI_URL not set")
	}
	client := New(strings.TrimRight(baseURL, "/"), "local-test-only", "echo-local-test")
	answer, executions, err := client.RespondWithTools(context.Background(), "Use the status tool.", []ConversationMessage{{Role: "user", Content: "How are things?"}}, []ToolDefinition{FunctionTool("get_status", "Get status.", objectSchema(nil, nil))}, func(context.Context, string, json.RawMessage) (string, error) {
		return `{"open_alerts":0}`, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(executions) != 1 || executions[0].Name != "get_status" || !strings.Contains(answer, "bounded read-only tool") {
		t.Fatalf("answer=%q executions=%#v", answer, executions)
	}
}

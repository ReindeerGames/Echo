package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	maxToolRounds       = 6
	maxToolCalls        = 8
	maxToolArgumentSize = 16 << 10
	maxToolOutputSize   = 64 << 10
)

var ErrResponsesUnsupported = errors.New("Responses API is unavailable for this provider")

type ConversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ToolDefinition struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
	Strict      bool           `json:"strict"`
}

type ToolExecution struct {
	CallID    string          `json:"callId"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Output    string          `json:"output"`
	Error     string          `json:"error,omitempty"`
	Duration  time.Duration   `json:"duration"`
}

type ToolExecutor func(context.Context, string, json.RawMessage) (string, error)

type toolResponse struct {
	ID     string            `json:"id"`
	Status string            `json:"status"`
	Output []json.RawMessage `json:"output"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
}

type responseItem struct {
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}

func FunctionTool(name, description string, parameters map[string]any) ToolDefinition {
	return ToolDefinition{Type: "function", Name: name, Description: description, Parameters: parameters, Strict: true}
}

func (c *Client) RespondWithTools(ctx context.Context, instructions string, history []ConversationMessage, tools []ToolDefinition, executor ToolExecutor) (string, []ToolExecution, error) {
	settings, _, _, err := c.settings(ctx)
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(instructions) == "" || len(history) == 0 {
		return "", nil, errors.New("instructions and conversation history are required")
	}
	if len(tools) == 0 || executor == nil {
		return "", nil, errors.New("at least one executable tool is required")
	}
	input := make([]any, 0, len(history)+maxToolRounds*3)
	for _, message := range history {
		if message.Role != "user" && message.Role != "assistant" {
			return "", nil, fmt.Errorf("invalid conversation role %q", message.Role)
		}
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		input = append(input, ConversationMessage{Role: message.Role, Content: content})
	}
	if len(input) == 0 {
		return "", nil, errors.New("conversation history contains no text")
	}

	executions := make([]ToolExecution, 0, maxToolCalls)
	for round := 0; round < maxToolRounds; round++ {
		response, err := c.createToolResponse(ctx, settings, instructions, input, tools)
		if err != nil {
			return "", executions, err
		}
		for _, raw := range response.Output {
			input = append(input, raw)
		}
		calls := make([]responseItem, 0)
		var textParts []string
		for _, raw := range response.Output {
			var item responseItem
			if err := json.Unmarshal(raw, &item); err != nil {
				return "", executions, fmt.Errorf("decode Responses output item: %w", err)
			}
			switch item.Type {
			case "function_call":
				calls = append(calls, item)
			case "message":
				for _, content := range item.Content {
					if content.Type == "output_text" && strings.TrimSpace(content.Text) != "" {
						textParts = append(textParts, strings.TrimSpace(content.Text))
					} else if content.Type == "refusal" && strings.TrimSpace(content.Refusal) != "" {
						textParts = append(textParts, strings.TrimSpace(content.Refusal))
					}
				}
			}
		}
		if len(calls) == 0 {
			answer := strings.TrimSpace(strings.Join(textParts, "\n"))
			if answer == "" {
				return "", executions, errors.New("Responses API returned neither text nor a function call")
			}
			return answer, executions, nil
		}
		if len(executions)+len(calls) > maxToolCalls {
			return "", executions, fmt.Errorf("tool call limit exceeded: maximum %d", maxToolCalls)
		}
		for _, call := range calls {
			arguments := json.RawMessage(call.Arguments)
			if call.CallID == "" || call.Name == "" || len(arguments) > maxToolArgumentSize || !json.Valid(arguments) {
				return "", executions, errors.New("Responses API returned an invalid function call")
			}
			started := time.Now()
			output, callErr := executor(ctx, call.Name, arguments)
			execution := ToolExecution{CallID: call.CallID, Name: call.Name, Arguments: append(json.RawMessage(nil), arguments...), Duration: time.Since(started)}
			if callErr != nil {
				execution.Error = callErr.Error()
				encoded, _ := json.Marshal(map[string]string{"error": "read-only tool execution failed"})
				output = string(encoded)
			}
			if len(output) > maxToolOutputSize {
				output = output[:maxToolOutputSize] + `{"truncated":true}`
			}
			execution.Output = output
			executions = append(executions, execution)
			input = append(input, map[string]any{"type": "function_call_output", "call_id": call.CallID, "output": output})
		}
	}
	return "", executions, fmt.Errorf("tool round limit exceeded: maximum %d", maxToolRounds)
}

func (c *Client) createToolResponse(ctx context.Context, settings runtimeSettings, instructions string, input []any, tools []ToolDefinition) (toolResponse, error) {
	payload := map[string]any{
		"model":               settings.Model,
		"instructions":        instructions,
		"input":               input,
		"tools":               tools,
		"tool_choice":         "auto",
		"parallel_tool_calls": false,
		"store":               false,
		"include":             []string{"reasoning.encrypted_content"},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return toolResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, settings.BaseURL+"/responses", bytes.NewReader(raw))
	if err != nil {
		return toolResponse{}, err
	}
	req.Header.Set("Authorization", "Bearer "+settings.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return toolResponse{}, fmt.Errorf("Responses API request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return toolResponse{}, ErrResponsesUnsupported
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return toolResponse{}, fmt.Errorf("Responses API status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var result toolResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result); err != nil {
		return toolResponse{}, fmt.Errorf("decode Responses API result: %w", err)
	}
	if result.Error != nil {
		return toolResponse{}, fmt.Errorf("Responses API error: %s", result.Error.Message)
	}
	if result.Status != "" && result.Status != "completed" {
		return toolResponse{}, fmt.Errorf("Responses API returned non-completed status %q", result.Status)
	}
	if len(result.Output) == 0 {
		return toolResponse{}, errors.New("Responses API returned no output items")
	}
	return result, nil
}

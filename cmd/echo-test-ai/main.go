// echo-test-ai is a deterministic OpenAI-compatible stub for the disposable
// local test environment. It must never be used as an actual incident analyst.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type completionRequest struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--healthcheck" {
		client := http.Client{Timeout: 2 * time.Second}
		response, err := client.Get("http://127.0.0.1:8090/healthz")
		if err != nil || response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		_ = response.Body.Close()
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /v1/chat/completions", completion)
	log.Print("Echo local test AI listening on :8090")
	log.Fatal(http.ListenAndServe(":8090", mux))
}

func completion(w http.ResponseWriter, r *http.Request) {
	var request completionRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20)).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	patchRequest := false
	for _, message := range request.Messages {
		if message.Role == "system" && strings.Contains(strings.ToLower(message.Content), "patch planner") {
			patchRequest = true
			break
		}
	}

	var content any
	if patchRequest {
		content = map[string]any{
			"summary": "Local test AI does not create patches. Configure a real AI provider and GitHub token after validating incident intake.",
			"changes": []any{},
			"tests":   []string{"Validate incident intake and acknowledgement in the local test group."},
			"risks":   []string{"This is deterministic test output, not a code diagnosis."},
		}
	} else {
		content = map[string]any{
			"status":     "insufficient_evidence",
			"confidence": "low",
			"summary":    "Echo received and processed the WhatsApp report successfully. This local test uses deterministic mock analysis; configure a GitHub token and real AI provider for repository-backed diagnosis.",
			"evidence":   []any{},
			"proposal": map[string]any{
				"action":      "validate_incident_intake",
				"repository":  "",
				"description": "Confirm the acknowledgement appeared in the group and the incident and audit event are visible in the control plane.",
				"risks":       []string{"No repository was inspected in local mock mode."},
				"tests":       []string{"Check the group acknowledgement.", "Open the incident in the Echo UI.", "Check the audit trail."},
			},
		}
	}

	encoded, err := json.Marshal(content)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "encode response"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":    "echo-local-test",
		"model": "echo-local-test",
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]string{
				"role":    "assistant",
				"content": string(encoded),
			},
			"finish_reason": "stop",
		}},
	})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

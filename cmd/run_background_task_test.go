package cmd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kindship-ai/kindship-cli/internal/api"
)

func TestRunBackgroundTaskUsesBackgroundTaskEndpoints(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if strings.Contains(r.URL.Path, "/api/planning/") {
			t.Fatalf("background task run used planning route: %s", r.URL.Path)
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/background-tasks/task-1/execute":
			_ = json.NewEncoder(w).Encode(api.EntityExecuteResponse{
				Entity: api.PlanningEntity{
					ID:            "task-1",
					Type:          "TASK",
					Title:         "Background task",
					Description:   "Run a command",
					ExecutionMode: api.ExecutionModeBash,
					Code:          stringPtr("echo bg-ok"),
				},
				DependenciesStatus: api.DependencyStatus{AllMet: true},
				Inputs:             map[string]interface{}{},
			})
		case "/api/background-tasks/execution/start":
			_ = json.NewEncoder(w).Encode(api.ExecutionStartResponse{
				ExecutionID:   "run-1",
				AttemptNumber: 1,
				Inputs:        map[string]interface{}{},
			})
		case "/api/background-tasks/execution/run-1/logs":
			_ = json.NewEncoder(w).Encode(api.SendLogLinesResponse{Inserted: 1})
		case "/api/background-tasks/execution/run-1/complete":
			var req api.ExecutionCompleteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode complete request: %v", err)
			}
			if req.Status != api.ExecutionAttemptStatusSuccess {
				t.Fatalf("status = %q, want SUCCESS", req.Status)
			}
			_ = json.NewEncoder(w).Encode(api.ExecutionCompleteResponse{Success: true})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	oldAgentID, oldServiceKey, oldAPIURL := agentID, serviceKey, apiURL
	oldBackgroundTaskRun := backgroundTaskRun
	oldSessionID, oldResume, oldSessionRetry := sessionID, resume, sessionRetryOnConflict
	t.Cleanup(func() {
		agentID, serviceKey, apiURL = oldAgentID, oldServiceKey, oldAPIURL
		backgroundTaskRun = oldBackgroundTaskRun
		sessionID, resume, sessionRetryOnConflict = oldSessionID, oldResume, oldSessionRetry
	})

	agentID = "agent-1"
	serviceKey = "service-key"
	apiURL = server.URL
	backgroundTaskRun = true
	sessionID = ""
	resume = false
	sessionRetryOnConflict = false

	if err := runExecute(runCmd, []string{"task-1"}); err != nil {
		t.Fatalf("run background task: %v", err)
	}

	joined := strings.Join(paths, "\n")
	for _, want := range []string{
		"GET /api/background-tasks/task-1/execute",
		"POST /api/background-tasks/execution/start",
		"POST /api/background-tasks/execution/run-1/logs",
		"POST /api/background-tasks/execution/run-1/complete",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing path %q in:\n%s", want, joined)
		}
	}
}

func stringPtr(value string) *string {
	return &value
}

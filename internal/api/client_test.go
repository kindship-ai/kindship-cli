package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBackgroundTaskExecutionEndpoints(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("X-Kindship-Service-Key") != "test-service-key" {
			t.Fatalf("missing service key header")
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/background-tasks/task-1/execute":
			_ = json.NewEncoder(w).Encode(EntityExecuteResponse{
				Entity: PlanningEntity{
					ID:            "task-1",
					Type:          "TASK",
					Title:         "Background task",
					Description:   "Do work",
					ExecutionMode: ExecutionModeAgent,
				},
				DependenciesStatus: DependencyStatus{AllMet: true},
				Inputs:             map[string]interface{}{},
			})
		case "/api/background-tasks/execution/start":
			var req BackgroundTaskExecutionStartRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode start request: %v", err)
			}
			if req.BackgroundTaskID != "task-1" {
				t.Fatalf("unexpected background task id: %q", req.BackgroundTaskID)
			}
			_ = json.NewEncoder(w).Encode(ExecutionStartResponse{
				ExecutionID:   "run-1",
				AttemptNumber: 1,
				Inputs:        map[string]interface{}{},
			})
		case "/api/background-tasks/execution/run-1/complete":
			var req ExecutionCompleteRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode complete request: %v", err)
			}
			if req.Status != ExecutionAttemptStatusSuccess {
				t.Fatalf("unexpected status: %q", req.Status)
			}
			_ = json.NewEncoder(w).Encode(ExecutionCompleteResponse{
				Success: true,
				Message: "ok",
			})
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(server.URL, false)
	if _, err := client.FetchBackgroundTaskForExecution("task-1", "test-service-key"); err != nil {
		t.Fatalf("fetch background task: %v", err)
	}
	if _, err := client.StartBackgroundTaskExecution(BackgroundTaskExecutionStartRequest{
		BackgroundTaskID: "task-1",
		AgentID:          "agent-1",
		CLI:              "codex",
		SessionID:        "session-1",
	}, "test-service-key"); err != nil {
		t.Fatalf("start background task: %v", err)
	}
	if _, err := client.CompleteBackgroundTaskExecution("run-1", ExecutionCompleteRequest{
		Status: ExecutionAttemptStatusSuccess,
	}, "test-service-key"); err != nil {
		t.Fatalf("complete background task: %v", err)
	}

	want := []string{
		"GET /api/background-tasks/task-1/execute",
		"POST /api/background-tasks/execution/start",
		"POST /api/background-tasks/execution/run-1/complete",
	}
	if len(paths) != len(want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
	for i := range want {
		if paths[i] != want[i] {
			t.Fatalf("paths[%d] = %q, want %q", i, paths[i], want[i])
		}
	}
}

func TestBackgroundTaskLogEndpoint(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(SendLogLinesResponse{Inserted: 1})
	}))
	defer server.Close()

	client := NewClient(server.URL, false)
	err := client.SendBackgroundTaskLogLines("run-1", []LogLine{{
		Seq:     1,
		Stream:  "system",
		Content: "hello",
	}}, "test-service-key")
	if err != nil {
		t.Fatalf("send background task logs: %v", err)
	}

	if gotPath != "/api/background-tasks/execution/run-1/logs" {
		t.Fatalf("got path %q", gotPath)
	}
}

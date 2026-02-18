package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/auth"
)

// Client is the Kindship API client for fetching secrets
type Client struct {
	baseURL    string
	httpClient *http.Client
	verbose    bool
}

// SecretsResponse is the response from the secrets endpoint
type SecretsResponse struct {
	Env   map[string]string `json:"env"`
	Error string            `json:"error,omitempty"`
}

// log prints a message if verbose mode is enabled
func (c *Client) log(format string, args ...interface{}) {
	if c.verbose {
		fmt.Fprintf(os.Stderr, "[kindship:api] "+format+"\n", args...)
	}
}

// NewClient creates a new API client
func NewClient(baseURL string, verbose bool) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		verbose: verbose,
	}
}

// FetchSecrets retrieves secrets for a specific agent and command
func (c *Client) FetchSecrets(agentID, command, serviceKey string) (map[string]string, error) {
	// Build URL with query params
	endpoint := fmt.Sprintf("%s/api/agent-containers/%s/secrets", c.baseURL, agentID)
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	q := u.Query()
	q.Set("command", command)
	u.RawQuery = q.Encode()

	c.log("Request URL: %s", u.String())

	// Create request
	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("X-Kindship-Service-Key", serviceKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	c.log("Request headers: Accept=%s, User-Agent=%s", req.Header.Get("Accept"), req.Header.Get("User-Agent"))

	// Execute request
	reqStart := time.Now()
	resp, err := c.httpClient.Do(req)
	reqDuration := time.Since(reqStart)

	if err != nil {
		c.log("Request failed after %v: %v", reqDuration, err)
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	c.log("Response status: %d %s (took %v)", resp.StatusCode, resp.Status, reqDuration)
	c.log("Response headers: Content-Type=%s, Content-Length=%s",
		resp.Header.Get("Content-Type"), resp.Header.Get("Content-Length"))

	// Read body
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	c.log("Response body length: %d bytes", len(body))

	// Handle non-2xx status codes
	if resp.StatusCode != http.StatusOK {
		c.log("Error response body: %s", string(body))

		var errResp SecretsResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Error)
		}

		// Provide more context for common errors
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return nil, fmt.Errorf("authentication failed (%d): invalid service key or IP not whitelisted", resp.StatusCode)
		case http.StatusForbidden:
			return nil, fmt.Errorf("access denied (%d): %s", resp.StatusCode, string(body))
		case http.StatusNotFound:
			return nil, fmt.Errorf("not found (%d): agent or secrets endpoint not found", resp.StatusCode)
		case http.StatusTooManyRequests:
			return nil, fmt.Errorf("rate limited (%d): too many requests, try again later", resp.StatusCode)
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
			return nil, fmt.Errorf("server error (%d): %s", resp.StatusCode, string(body))
		default:
			return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
		}
	}

	// Parse response
	var secretsResp SecretsResponse
	if err := json.Unmarshal(body, &secretsResp); err != nil {
		c.log("Failed to parse JSON: %v", err)
		c.log("Raw response: %s", string(body))
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Successfully parsed %d secrets", len(secretsResp.Env))

	return secretsResp.Env, nil
}

// FetchEntityForExecution retrieves a planning entity for execution
func (c *Client) FetchEntityForExecution(entityID, serviceKey string) (*EntityExecuteResponse, error) {
	endpoint := fmt.Sprintf("%s/api/planning/entity/%s/execute", c.baseURL, entityID)
	c.log("Fetching entity for execution: %s", endpoint)

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-Kindship-Service-Key", serviceKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var entityResp EntityExecuteResponse
	if err := json.Unmarshal(body, &entityResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Successfully fetched entity: %s", entityResp.Entity.Title)
	return &entityResp, nil
}

// StartExecution creates a new execution attempt
func (c *Client) StartExecution(req ExecutionStartRequest, serviceKey string) (*ExecutionStartResponse, error) {
	endpoint := fmt.Sprintf("%s/api/planning/execution/start", c.baseURL)
	c.log("Starting execution for entity: %s", req.EntityID)

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("X-Kindship-Service-Key", serviceKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var startResp ExecutionStartResponse
	if err := json.Unmarshal(body, &startResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Started execution: %s (attempt %d)", startResp.ExecutionID, startResp.AttemptNumber)
	return &startResp, nil
}

// StartExecutionWithBearer creates a new execution attempt using bearer token auth
// (Authorization: Bearer <token>).
func (c *Client) StartExecutionWithBearer(req ExecutionStartRequest, bearerToken string) (*ExecutionStartResponse, error) {
	endpoint := fmt.Sprintf("%s/api/planning/execution/start", c.baseURL)
	c.log("Starting execution (bearer) for entity: %s", req.EntityID)

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", formatBearerHeader(bearerToken))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var startResp ExecutionStartResponse
	if err := json.Unmarshal(body, &startResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Started execution (bearer): %s (attempt %d)", startResp.ExecutionID, startResp.AttemptNumber)
	return &startResp, nil
}

// CompleteExecution marks an execution as complete
func (c *Client) CompleteExecution(executionID string, req ExecutionCompleteRequest, serviceKey string) (*ExecutionCompleteResponse, error) {
	endpoint := fmt.Sprintf("%s/api/planning/execution/%s/complete", c.baseURL, executionID)
	c.log("Completing execution: %s (status: %s)", executionID, req.Status)

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("X-Kindship-Service-Key", serviceKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var completeResp ExecutionCompleteResponse
	if err := json.Unmarshal(body, &completeResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Execution completed successfully")
	return &completeResp, nil
}

// CompleteExecutionWithBearer marks an execution as complete using bearer token auth
// (Authorization: Bearer <token>).
func (c *Client) CompleteExecutionWithBearer(executionID string, req ExecutionCompleteRequest, bearerToken string) (*ExecutionCompleteResponse, error) {
	endpoint := fmt.Sprintf("%s/api/planning/execution/%s/complete", c.baseURL, executionID)
	c.log("Completing execution (bearer): %s (status: %s)", executionID, req.Status)

	jsonData, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Authorization", formatBearerHeader(bearerToken))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var completeResp ExecutionCompleteResponse
	if err := json.Unmarshal(body, &completeResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Execution completed successfully (bearer)")
	return &completeResp, nil
}

func formatBearerHeader(tokenOrHeader string) string {
	trimmed := strings.TrimSpace(tokenOrHeader)
	if trimmed == "" {
		return ""
	}
	// Accept either a raw token or a preformatted "Bearer ..." header value.
	if strings.HasPrefix(strings.ToLower(trimmed), "bearer ") {
		return trimmed
	}
	return "Bearer " + trimmed
}

// FetchNextTask gets the next runnable task for an agent.
// Uses X-Kindship-Service-Key header for /api/cli/* endpoints.
func (c *Client) FetchNextTask(agentID, serviceKey string) (*PlanNextResponse, error) {
	u, err := url.Parse(fmt.Sprintf("%s/api/cli/plan/next", c.baseURL))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	q := u.Query()
	q.Set("agent_id", agentID)
	u.RawQuery = q.Encode()

	c.log("Fetching next task for agent: %s", agentID)

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-Kindship-Service-Key", serviceKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp PlanNextResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Error)
		}
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var nextResp PlanNextResponse
	if err := json.Unmarshal(body, &nextResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if nextResp.Task != nil {
		c.log("Next task: %s (%s)", nextResp.Task.Title, nextResp.Task.ID)
	} else {
		c.log("No runnable tasks available")
	}

	return &nextResp, nil
}

// FetchNextTaskScoped fetches the next runnable task scoped to any parent entity.
// Uses mode=orchestrate&entity_uuid=<parentEntityID>.
func (c *Client) FetchNextTaskScoped(agentID, parentEntityID, serviceKey string) (*PlanNextResponse, error) {
	u, err := url.Parse(fmt.Sprintf("%s/api/cli/plan/next", c.baseURL))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	q := u.Query()
	q.Set("agent_id", agentID)
	q.Set("mode", "orchestrate")
	q.Set("entity_uuid", parentEntityID)
	u.RawQuery = q.Encode()

	c.log("Fetching next task scoped to entity: %s", parentEntityID)

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-Kindship-Service-Key", serviceKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp PlanNextResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Error)
		}
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var nextResp PlanNextResponse
	if err := json.Unmarshal(body, &nextResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if nextResp.Task != nil {
		c.log("Next task scoped to entity: %s (%s)", nextResp.Task.Title, nextResp.Task.ID)
	} else {
		c.log("No more runnable tasks scoped to entity")
	}

	return &nextResp, nil
}

// FetchNextTaskScopedWithBearer fetches the next runnable task scoped to a parent entity
// using bearer auth (local mode).
func (c *Client) FetchNextTaskScopedWithBearer(agentID, parentEntityID, bearerToken string) (*PlanNextResponse, error) {
	u, err := url.Parse(fmt.Sprintf("%s/api/cli/plan/next", c.baseURL))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	q := u.Query()
	q.Set("agent_id", agentID)
	q.Set("mode", "orchestrate")
	q.Set("entity_uuid", parentEntityID)
	u.RawQuery = q.Encode()

	c.log("Fetching next scoped task (bearer) for entity: %s", parentEntityID)

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", formatBearerHeader(bearerToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp PlanNextResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Error)
		}
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var nextResp PlanNextResponse
	if err := json.Unmarshal(body, &nextResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if nextResp.Task != nil {
		c.log("Next scoped task (bearer): %s (%s)", nextResp.Task.Title, nextResp.Task.ID)
	} else if nextResp.PendingCount > 0 {
		c.log("No runnable scoped task yet; %d pending", nextResp.PendingCount)
	} else {
		c.log("No scoped tasks pending")
	}

	return &nextResp, nil
}

// FetchNextTaskForProcess fetches the next runnable task scoped to a specific Process.
// Deprecated: Use FetchNextTaskScoped instead. This is a backward-compatible wrapper.
func (c *Client) FetchNextTaskForProcess(agentID, processEntityID, serviceKey string) (*PlanNextResponse, error) {
	return c.FetchNextTaskScoped(agentID, processEntityID, serviceKey)
}

// ActivateEntity activates a planning entity, optionally including all descendants.
// Uses X-Kindship-Service-Key header for /api/cli/* endpoints.
func (c *Client) ActivateEntity(entityID, serviceKey string, recursive bool) (*ActivateEntityResponse, error) {
	u, err := url.Parse(fmt.Sprintf("%s/api/cli/entity/%s/activate", c.baseURL, entityID))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	q := u.Query()
	if recursive {
		q.Set("recursive", "true")
	}
	u.RawQuery = q.Encode()

	c.log("Activating entity: %s (recursive=%v)", entityID, recursive)

	req, err := http.NewRequest(http.MethodPost, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-Kindship-Service-Key", serviceKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp ActivateEntityResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Error)
		}
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var activateResp ActivateEntityResponse
	if err := json.Unmarshal(body, &activateResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Activated %d entities", activateResp.ActivatedCount)
	return &activateResp, nil
}

// RecoverRuns reconciles RUNNING runs after container restart.
// ORCHESTRATE runs are returned as resumed runs, leaf runs may be returned as
// resumable descriptors, and ASK_USER runs are skipped.
func (c *Client) RecoverRuns(agentID, serviceKey string) (*RecoverRunsResponse, error) {
	endpoint := fmt.Sprintf("%s/api/cli/agent/recover-runs", c.baseURL)
	c.log("Recovering runs for agent: %s", agentID)

	reqBody := struct {
		AgentID string `json:"agent_id"`
	}{AgentID: agentID}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("X-Kindship-Service-Key", serviceKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp RecoverRunsResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, errResp.Error)
		}
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var recoverResp RecoverRunsResponse
	if err := json.Unmarshal(body, &recoverResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Run reconciliation: %d resumed, %d resumable, %d failed, %d skipped (ASK_USER)",
		len(recoverResp.ResumedRuns),
		len(recoverResp.ResumableRuns),
		recoverResp.FailedCount,
		recoverResp.SkippedAskUser)
	return &recoverResp, nil
}

// StartProcessRunWithBearer starts a new ORCHESTRATE run on a process (or resumes existing).
func (c *Client) StartProcessRunWithBearer(processID, bearerToken string) (*StartProcessRunResponse, error) {
	endpoint := fmt.Sprintf("%s/api/cli/process/%s/start-run", c.baseURL, processID)
	c.log("Starting process run for: %s", processID)

	req, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", formatBearerHeader(bearerToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var startResp StartProcessRunResponse
	if err := json.Unmarshal(body, &startResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Process run: %s (run %d, resumed=%v, tasks=%d)",
		startResp.ProcessRunID, startResp.RunNumber, startResp.Resumed, len(startResp.Tasks))
	return &startResp, nil
}

// FetchActiveRunWithBearer retrieves the currently active (RUNNING) task run for an agent.
// Replaces local active_run.json file reads with a DB-backed API call.
func (c *Client) FetchActiveRunWithBearer(agentID, processID, bearerToken string) (*ActiveRunResponse, error) {
	u, err := url.Parse(fmt.Sprintf("%s/api/cli/agent/active-run", c.baseURL))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	q := u.Query()
	q.Set("agent_id", agentID)
	if processID != "" {
		q.Set("process_id", processID)
	}
	u.RawQuery = q.Encode()

	c.log("Fetching active run for agent: %s", agentID)

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", formatBearerHeader(bearerToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var activeResp ActiveRunResponse
	if err := json.Unmarshal(body, &activeResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if activeResp.ActiveRun != nil {
		c.log("Active run: %s (entity %s)", activeResp.ActiveRun.RunID, activeResp.ActiveRun.EntityID)
	} else {
		c.log("No active run for agent")
	}

	return &activeResp, nil
}

// UpdateRunOutput merges structured data into a running execution's outputs.
// PATCH /api/planning/execution/{runID}/output
func (c *Client) UpdateRunOutput(runID string, structured map[string]interface{}, bearerToken string) error {
	endpoint := fmt.Sprintf("%s/api/planning/execution/%s/output", c.baseURL, runID)
	c.log("Updating run output: %s", runID)

	reqBody := struct {
		Structured map[string]interface{} `json:"structured"`
	}{Structured: structured}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPatch, endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", formatBearerHeader(bearerToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	c.log("Run output updated successfully")
	return nil
}

// InstallSkill writes a skill file to an agent's container.
// POST /api/cli/agent/{agentID}/install-skill
func (c *Client) InstallSkill(agentID, name, content, bearerToken string) error {
	endpoint := fmt.Sprintf("%s/api/cli/agent/%s/install-skill", c.baseURL, agentID)
	c.log("Installing skill '%s' for agent: %s", name, agentID)

	reqBody := struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}{Name: name, Content: content}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", formatBearerHeader(bearerToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	c.log("Skill installed successfully")
	return nil
}

// CreateDevLoopWithBearer creates a dev loop process for an agent using server-side blueprint.
func (c *Client) CreateDevLoopWithBearer(agentID, repoName, bearerToken string) (*CreateDevLoopResponse, error) {
	endpoint := fmt.Sprintf("%s/api/cli/process/create-dev-loop", c.baseURL)
	c.log("Creating dev loop for agent: %s (repo: %s)", agentID, repoName)

	reqBody := struct {
		AgentID  string `json:"agent_id"`
		RepoName string `json:"repo_name,omitempty"`
	}{AgentID: agentID, RepoName: repoName}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", formatBearerHeader(bearerToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var devLoopResp CreateDevLoopResponse
	if err := json.Unmarshal(body, &devLoopResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Dev loop: process_id=%s, created=%v, tasks=%d", devLoopResp.ProcessID, devLoopResp.Created, len(devLoopResp.Tasks))
	return &devLoopResp, nil
}

// ReadAttachmentWithBearer reads a text attachment's content by name.
// GET /api/cli/entity/{entityID}/attachments?name={name}
func (c *Client) ReadAttachmentWithBearer(entityID, name, bearerToken string) (string, error) {
	endpoint := fmt.Sprintf("%s/api/cli/entity/%s/attachments?name=%s", c.baseURL, entityID, url.QueryEscape(name))
	c.log("Reading attachment '%s' for entity: %s", name, entityID)

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", formatBearerHeader(bearerToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Attachment read: %d bytes", len(result.Content))
	return result.Content, nil
}

// UpsertAttachmentWithBearer creates or updates a text attachment.
// PUT /api/cli/entity/{entityID}/attachments
func (c *Client) UpsertAttachmentWithBearer(entityID, name, content, bearerToken string) error {
	endpoint := fmt.Sprintf("%s/api/cli/entity/%s/attachments", c.baseURL, entityID)
	c.log("Upserting attachment '%s' for entity: %s (%d bytes)", name, entityID, len(content))

	reqBody := struct {
		Name    string `json:"name"`
		Content string `json:"content"`
	}{Name: name, Content: content}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPut, endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", formatBearerHeader(bearerToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	c.log("Attachment upserted successfully")
	return nil
}

// ListAttachmentsWithBearer lists all attachments for an entity.
// GET /api/cli/entity/{entityID}/attachments
func (c *Client) ListAttachmentsWithBearer(entityID, bearerToken string) ([]EntityAttachment, error) {
	endpoint := fmt.Sprintf("%s/api/cli/entity/%s/attachments", c.baseURL, entityID)
	c.log("Listing attachments for entity: %s", entityID)

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", formatBearerHeader(bearerToken))
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Attachments []EntityAttachment `json:"attachments"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Listed %d attachments", len(result.Attachments))
	return result.Attachments, nil
}

// GetEntity retrieves a single planning entity by ID with optional includes.
func (c *Client) GetEntity(ctx *auth.Context, entityID string, include []string) (*EntityGetResponse, error) {
	u, err := url.Parse(fmt.Sprintf("%s/api/cli/entity/%s", c.baseURL, entityID))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	if len(include) > 0 {
		q := u.Query()
		q.Set("include", strings.Join(include, ","))
		u.RawQuery = q.Encode()
	}

	c.log("Getting entity: %s (include=%v)", entityID, include)

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var getResp EntityGetResponse
	if err := json.Unmarshal(body, &getResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Got entity: %s (%s)", getResp.Entity.Title, getResp.Entity.Type)
	return &getResp, nil
}

// ListEntities retrieves a filtered list of planning entities.
func (c *Client) ListEntities(ctx *auth.Context, opts ListEntitiesOpts) (*EntityListResponse, error) {
	u, err := url.Parse(fmt.Sprintf("%s/api/cli/entity", c.baseURL))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	q := u.Query()
	if opts.AgentID != "" {
		q.Set("agent_id", opts.AgentID)
	}
	if opts.Type != "" {
		q.Set("type", opts.Type)
	}
	if opts.Status != "" {
		q.Set("status", opts.Status)
	}
	if opts.ParentID != "" {
		q.Set("parent_id", opts.ParentID)
	}
	if opts.Limit > 0 {
		q.Set("limit", strconv.Itoa(opts.Limit))
	}
	if opts.IncludeDeleted {
		q.Set("include_deleted", "true")
	}
	u.RawQuery = q.Encode()

	c.log("Listing entities: %s", u.String())

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var listResp EntityListResponse
	if err := json.Unmarshal(body, &listResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Listed %d entities", listResp.Count)
	return &listResp, nil
}

// CreateEntity creates a new planning entity.
func (c *Client) CreateEntity(ctx *auth.Context, createReq EntityCreateRequest) (*EntityCreateResponse, error) {
	endpoint := fmt.Sprintf("%s/api/cli/entity", c.baseURL)
	c.log("Creating entity: %s (%s)", createReq.Title, createReq.Type)

	jsonData, err := json.Marshal(createReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var createResp EntityCreateResponse
	if err := json.Unmarshal(body, &createResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Created entity: %s (%s)", createResp.Entity.ID, createResp.Entity.Title)
	return &createResp, nil
}

// UpdateEntity updates an existing planning entity.
func (c *Client) UpdateEntity(ctx *auth.Context, entityID string, updateReq EntityUpdateRequest) (*EntityUpdateResponse, error) {
	endpoint := fmt.Sprintf("%s/api/cli/entity/%s", c.baseURL, entityID)
	c.log("Updating entity: %s", entityID)

	jsonData, err := json.Marshal(updateReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPatch, endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var updateResp EntityUpdateResponse
	if err := json.Unmarshal(body, &updateResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Updated entity: %s", updateResp.Entity.Title)
	return &updateResp, nil
}

// DeleteEntity soft-deletes a planning entity.
func (c *Client) DeleteEntity(ctx *auth.Context, entityID string, force bool) (*EntityDeleteResponse, error) {
	u, err := url.Parse(fmt.Sprintf("%s/api/cli/entity/%s", c.baseURL, entityID))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	if force {
		q := u.Query()
		q.Set("force", "true")
		u.RawQuery = q.Encode()
	}

	c.log("Deleting entity: %s (force=%v)", entityID, force)

	req, err := http.NewRequest(http.MethodDelete, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var deleteResp EntityDeleteResponse
	if err := json.Unmarshal(body, &deleteResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Deleted entity: %s", entityID)
	return &deleteResp, nil
}

// RestoreEntity restores a soft-deleted planning entity.
func (c *Client) RestoreEntity(ctx *auth.Context, entityID string) (*EntityRestoreResponse, error) {
	endpoint := fmt.Sprintf("%s/api/cli/entity/%s/restore", c.baseURL, entityID)
	c.log("Restoring entity: %s", entityID)

	req, err := http.NewRequest(http.MethodPost, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var restoreResp EntityRestoreResponse
	if err := json.Unmarshal(body, &restoreResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Restored entity: %s", restoreResp.Entity.Title)
	return &restoreResp, nil
}

// ManageDeps manages dependencies on a planning entity (add, remove, or check).
func (c *Client) ManageDeps(ctx *auth.Context, entityID, op, depID string) (*EntityDepsResponse, error) {
	u, err := url.Parse(fmt.Sprintf("%s/api/cli/entity/%s/deps", c.baseURL, entityID))
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}

	q := u.Query()
	q.Set("op", op)
	u.RawQuery = q.Encode()

	c.log("Managing deps on entity %s: op=%s dep=%s", entityID, op, depID)

	var reqBody io.Reader
	if depID != "" {
		jsonData, err := json.Marshal(EntityDepsRequest{DependencyID: depID})
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonData)
	}

	req, err := http.NewRequest(http.MethodPost, u.String(), reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")
	if depID != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var depsResp EntityDepsResponse
	if err := json.Unmarshal(body, &depsResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Deps %s: success=%v", op, depsResp.Success)
	return &depsResp, nil
}

// MoveEntity moves an entity to a new parent and/or sequence position.
func (c *Client) MoveEntity(ctx *auth.Context, entityID string, moveReq EntityMoveRequest) (*EntityMoveResponse, error) {
	endpoint := fmt.Sprintf("%s/api/cli/entity/%s/move", c.baseURL, entityID)
	c.log("Moving entity %s to parent %s", entityID, moveReq.ParentID)

	jsonData, err := json.Marshal(moveReq)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API error (%d): %s", resp.StatusCode, string(body))
	}

	var moveResp EntityMoveResponse
	if err := json.Unmarshal(body, &moveResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.log("Moved entity: %s", moveResp.Entity.Title)
	return &moveResp, nil
}

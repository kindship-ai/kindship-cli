package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
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

// RecoverRuns classifies and recovers RUNNING runs after container restart.
// ORCHESTRATE runs are returned for resumption, leaf runs are marked FAILED,
// ASK_USER runs are skipped.
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

	c.log("Recovered runs: %d resumed, %d failed, %d skipped (ASK_USER)",
		len(recoverResp.ResumedRuns), recoverResp.FailedCount, recoverResp.SkippedAskUser)
	return &recoverResp, nil
}

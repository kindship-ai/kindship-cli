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

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", serviceKey))
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

	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", serviceKey))
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

	httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", serviceKey))
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

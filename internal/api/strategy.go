package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/auth"
)

// StrategyContext is the read-only projection of an agent's row used
// to fill placeholders in kindship-strategy/prompts/generate-user.md.
// The server returns this from GET /api/cli/agent/:agentId/strategy-context.
type StrategyContext struct {
	UserVision    *string `json:"userVision"`
	AgentName     string  `json:"agentName"`
	AgentSlug     string  `json:"agentSlug"`
	PublicPosture struct {
		Mode            string  `json:"mode"`            // agent_voice | collaborator | organization
		AttributionName *string `json:"attributionName"` // name for collaborator/organization, else null
	} `json:"publicPosture"`
}

// FetchStrategyContext calls the strategy-context endpoint using the
// provided auth context (service key in container mode, bearer in
// local mode).
func (c *Client) FetchStrategyContext(
	ctx *auth.Context, agentID string,
) (*StrategyContext, error) {
	endpoint := fmt.Sprintf("%s/api/cli/agent/%s/strategy-context", c.baseURL, agentID)

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	ctx.SetAuthHeaders(req)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "kindship-cli/1.0")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("strategy-context %d: %s", resp.StatusCode, string(body))
	}

	var out StrategyContext
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}
	return &out, nil
}

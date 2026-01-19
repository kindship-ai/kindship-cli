package auth

import (
	"fmt"
	"os"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/config"
)

// AuthMethod represents the authentication method being used
type AuthMethod string

const (
	// AuthMethodOAuth is used for local development (user's CLI token)
	AuthMethodOAuth AuthMethod = "oauth"
	// AuthMethodServiceKey is used in agent containers
	AuthMethodServiceKey AuthMethod = "service_key"
)

// Context holds the authentication context for API requests
type Context struct {
	Method  AuthMethod
	Token   string
	AgentID string

	// OAuth-specific fields
	UserID      string
	UserEmail   string
	TokenID     string
	TokenPrefix string
	TokenExpiry time.Time

	// API configuration
	APIBaseURL string
}

// GetAuthContext determines the authentication context from environment and config.
// Priority:
// 1. Service key (container mode) - KINDSHIP_SERVICE_KEY env var
// 2. OAuth token (local mode) - ~/.kindship/config.json
func GetAuthContext() (*Context, error) {
	// Priority 1: Service key from environment (container mode)
	serviceKey := os.Getenv("KINDSHIP_SERVICE_KEY")
	if serviceKey != "" {
		agentID := os.Getenv("AGENT_ID")
		apiURL := os.Getenv("KINDSHIP_API_URL")
		if apiURL == "" {
			apiURL = "https://kindship.ai"
		}

		return &Context{
			Method:     AuthMethodServiceKey,
			Token:      serviceKey,
			AgentID:    agentID,
			APIBaseURL: apiURL,
		}, nil
	}

	// Priority 2: OAuth token from config (local mode)
	cfg, err := config.LoadGlobalConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.Token == "" {
		return nil, fmt.Errorf("not authenticated: run 'kindship login' first")
	}

	if cfg.IsExpired() {
		return nil, fmt.Errorf("token expired: run 'kindship login' to refresh")
	}

	// Try to get agent ID from repo config
	var agentID string
	repoConfig, err := config.LoadRepoConfig()
	if err == nil {
		agentID = repoConfig.AgentID
	}

	// Fall back to global default agent if no repo config
	if agentID == "" {
		agentID = cfg.DefaultAgentID
	}

	return &Context{
		Method:      AuthMethodOAuth,
		Token:       cfg.Token,
		AgentID:     agentID,
		UserID:      cfg.UserID,
		UserEmail:   cfg.UserEmail,
		TokenID:     cfg.TokenID,
		TokenPrefix: cfg.TokenPrefix,
		TokenExpiry: cfg.TokenExpiry,
		APIBaseURL:  cfg.GetAPIBaseURL(),
	}, nil
}

// GetAuthContextOrNil is like GetAuthContext but returns nil instead of error
// when not authenticated. Useful for commands that have optional auth.
func GetAuthContextOrNil() *Context {
	ctx, err := GetAuthContext()
	if err != nil {
		return nil
	}
	return ctx
}

// IsContainerMode returns true if running in a container with service key auth
func (c *Context) IsContainerMode() bool {
	return c.Method == AuthMethodServiceKey
}

// IsLocalMode returns true if running locally with OAuth token auth
func (c *Context) IsLocalMode() bool {
	return c.Method == AuthMethodOAuth
}

// GetAuthHeader returns the appropriate Authorization header value
func (c *Context) GetAuthHeader() string {
	return fmt.Sprintf("Bearer %s", c.Token)
}

// RequireAgentID returns the agent ID or an error if not set
func (c *Context) RequireAgentID() (string, error) {
	if c.AgentID == "" {
		if c.IsLocalMode() {
			return "", fmt.Errorf("no agent configured: run 'kindship setup' in a git repository")
		}
		return "", fmt.Errorf("AGENT_ID environment variable not set")
	}
	return c.AgentID, nil
}

// MaskedToken returns a masked version of the token for logging
func (c *Context) MaskedToken() string {
	if len(c.Token) < 8 {
		return "***"
	}
	return c.Token[:4] + "..." + c.Token[len(c.Token)-4:]
}

package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	// ConfigDir is the directory name for Kindship config
	ConfigDir = ".kindship"
	// ConfigFile is the global config filename
	ConfigFile = "config.json"
	// RepoConfigFile is the per-repo config filename
	RepoConfigFile = "config.json"
	// ConfigFileMode is the required file permissions (owner read/write only)
	ConfigFileMode = 0600
	// ConfigDirMode is the required directory permissions
	ConfigDirMode = 0700
)

// GlobalConfig represents the user's global CLI configuration
// stored at ~/.kindship/config.json
type GlobalConfig struct {
	// Authentication
	Token       string    `json:"token,omitempty"`
	TokenID     string    `json:"token_id,omitempty"`
	TokenExpiry time.Time `json:"token_expiry,omitempty"`
	TokenPrefix string    `json:"token_prefix,omitempty"`

	// User info
	UserID    string `json:"user_id,omitempty"`
	UserEmail string `json:"user_email,omitempty"`

	// API configuration
	APIBaseURL string `json:"api_base_url,omitempty"`

	// Default agent (optional)
	DefaultAgentID string `json:"default_agent_id,omitempty"`
}

// RepoConfig represents the per-repository configuration
// stored at .kindship/config.json in the repo root
type RepoConfig struct {
	AgentID   string    `json:"agent_id"`
	AgentSlug string    `json:"agent_slug,omitempty"`
	AccountID string    `json:"account_id,omitempty"`
	BoundAt   time.Time `json:"bound_at,omitempty"`
}

// GetGlobalConfigDir returns the path to the global config directory
func GetGlobalConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, ConfigDir), nil
}

// GetGlobalConfigPath returns the path to the global config file
func GetGlobalConfigPath() (string, error) {
	dir, err := GetGlobalConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ConfigFile), nil
}

// LoadGlobalConfig loads the global configuration file
func LoadGlobalConfig() (*GlobalConfig, error) {
	configPath, err := GetGlobalConfigPath()
	if err != nil {
		return nil, err
	}

	// Check if file exists
	info, err := os.Stat(configPath)
	if os.IsNotExist(err) {
		// Return empty config if file doesn't exist
		return &GlobalConfig{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to stat config file: %w", err)
	}

	// Verify file permissions are secure (owner read/write only)
	if info.Mode().Perm() != ConfigFileMode {
		return nil, fmt.Errorf("config file has insecure permissions %o, expected %o", info.Mode().Perm(), ConfigFileMode)
	}

	// Read and parse config
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var config GlobalConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return &config, nil
}

// SaveGlobalConfig saves the global configuration file with secure permissions
func SaveGlobalConfig(config *GlobalConfig) error {
	configDir, err := GetGlobalConfigDir()
	if err != nil {
		return err
	}

	// Create config directory if it doesn't exist
	if err := os.MkdirAll(configDir, ConfigDirMode); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Set directory permissions (in case it already existed with wrong perms)
	if err := os.Chmod(configDir, ConfigDirMode); err != nil {
		return fmt.Errorf("failed to set config directory permissions: %w", err)
	}

	configPath := filepath.Join(configDir, ConfigFile)

	// Marshal config
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write with secure permissions
	if err := os.WriteFile(configPath, data, ConfigFileMode); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// ClearGlobalConfig removes authentication data from the global config
func ClearGlobalConfig() error {
	config, err := LoadGlobalConfig()
	if err != nil {
		// If we can't load it, try to create a fresh empty one
		config = &GlobalConfig{}
	}

	// Clear auth-related fields
	config.Token = ""
	config.TokenID = ""
	config.TokenExpiry = time.Time{}
	config.TokenPrefix = ""
	config.UserID = ""
	config.UserEmail = ""

	return SaveGlobalConfig(config)
}

// IsAuthenticated checks if the user is currently authenticated
func (c *GlobalConfig) IsAuthenticated() bool {
	return c.Token != "" && time.Now().Before(c.TokenExpiry)
}

// IsExpired checks if the token has expired
func (c *GlobalConfig) IsExpired() bool {
	return c.Token != "" && time.Now().After(c.TokenExpiry)
}

// GetAPIBaseURL returns the API base URL, defaulting to production
func (c *GlobalConfig) GetAPIBaseURL() string {
	if c.APIBaseURL != "" {
		return c.APIBaseURL
	}
	return "https://kindship.ai"
}

// GetRepoConfigDir returns the path to the repo config directory
// starting from the current working directory and searching up
func GetRepoConfigDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}

	// Search up from current directory for .kindship/config.json
	dir := cwd
	for {
		configDir := filepath.Join(dir, ConfigDir)
		configPath := filepath.Join(configDir, RepoConfigFile)

		if _, err := os.Stat(configPath); err == nil {
			return configDir, nil
		}

		// Move up one directory
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached filesystem root without finding config
			return "", fmt.Errorf("not in a kindship-configured repository")
		}
		dir = parent
	}
}

// LoadRepoConfig loads the repository configuration
func LoadRepoConfig() (*RepoConfig, error) {
	configDir, err := GetRepoConfigDir()
	if err != nil {
		return nil, err
	}

	configPath := filepath.Join(configDir, RepoConfigFile)
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read repo config: %w", err)
	}

	var config RepoConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse repo config: %w", err)
	}

	return &config, nil
}

// SaveRepoConfig saves the repository configuration
func SaveRepoConfig(config *RepoConfig, repoRoot string) error {
	configDir := filepath.Join(repoRoot, ConfigDir)

	// Create config directory
	if err := os.MkdirAll(configDir, ConfigDirMode); err != nil {
		return fmt.Errorf("failed to create repo config directory: %w", err)
	}

	configPath := filepath.Join(configDir, RepoConfigFile)

	// Marshal config
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal repo config: %w", err)
	}

	// Write config
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write repo config: %w", err)
	}

	return nil
}

// FindRepoRoot finds the root of the current git repository
func FindRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current directory: %w", err)
	}

	dir := cwd
	for {
		gitDir := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitDir); err == nil && info.IsDir() {
			return dir, nil
		}

		// Move up one directory
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not in a git repository")
		}
		dir = parent
	}
}

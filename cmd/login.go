package cmd

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/config"

	"github.com/spf13/cobra"
)

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with Kindship",
	Long: `Authenticate the Kindship CLI with your account.

This command opens your browser for authentication and stores
the credentials securely in ~/.kindship/config.json.

Example:
  kindship login`,
	RunE: runLogin,
}

var (
	loginAPIURL string
)

func init() {
	loginCmd.Flags().StringVar(&loginAPIURL, "api-url", "", "API base URL (default: https://kindship.ai)")
	rootCmd.AddCommand(loginCmd)
}

// AuthStartResponse is the response from /api/cli/auth/start
type AuthStartResponse struct {
	AuthURL       string `json:"auth_url"`
	State         string `json:"state"`
	CodeChallenge string `json:"code_challenge"`
	ExpiresIn     int    `json:"expires_in"`
	Error         string `json:"error,omitempty"`
}

// AuthCallbackRequest is the request to /api/cli/auth/callback
type AuthCallbackRequest struct {
	AuthCode     string `json:"auth_code"`
	CodeVerifier string `json:"code_verifier"`
	State        string `json:"state"`
}

// AuthCallbackResponse is the response from /api/cli/auth/callback
type AuthCallbackResponse struct {
	Token       string `json:"token"`
	TokenID     string `json:"token_id"`
	TokenPrefix string `json:"token_prefix"`
	UserID      string `json:"user_id"`
	UserEmail   string `json:"user_email"`
	ExpiresAt   string `json:"expires_at"`
	Error       string `json:"error,omitempty"`
}

func runLogin(cmd *cobra.Command, args []string) error {
	// Determine API base URL
	apiURL := loginAPIURL
	if apiURL == "" {
		apiURL = os.Getenv("KINDSHIP_API_URL")
	}
	if apiURL == "" {
		apiURL = "https://kindship.ai"
	}

	fmt.Println("Authenticating with Kindship...")

	// Step 1: Generate PKCE parameters
	codeVerifier, err := generateCodeVerifier()
	if err != nil {
		return fmt.Errorf("failed to generate code verifier: %w", err)
	}

	// Compute code_challenge = SHA256(code_verifier) - PKCE S256 method
	codeChallenge := computeCodeChallenge(codeVerifier)

	// Step 2: Find an available port and start local callback server
	listener, port, err := findAvailablePort()
	if err != nil {
		return fmt.Errorf("failed to start local server: %w", err)
	}
	defer listener.Close()

	// Channel to receive the callback result
	callbackCh := make(chan *callbackResult, 1)

	// Start the local callback server
	server := startCallbackServer(listener, callbackCh)
	defer server.Shutdown(context.Background())

	// Step 3: Call /api/cli/auth/start to get auth URL
	// Send code_challenge to server for PKCE verification later
	hostname, _ := os.Hostname()
	startURL := fmt.Sprintf("%s/api/cli/auth/start?callback_port=%d&hostname=%s&cli_version=%s&code_challenge=%s",
		apiURL, port, url.QueryEscape(hostname), url.QueryEscape(Version), url.QueryEscape(codeChallenge))

	startResp, err := callAuthStart(startURL)
	if err != nil {
		return err
	}

	// Step 4: Open browser
	fmt.Printf("\nOpening browser for authentication...\n")
	fmt.Printf("If browser doesn't open, visit:\n%s\n\n", startResp.AuthURL)

	if err := openBrowser(startResp.AuthURL); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: Failed to open browser: %v\n", err)
	}

	fmt.Println("Waiting for authentication...")

	// Step 5: Wait for callback (with timeout)
	select {
	case result := <-callbackCh:
		if result.err != nil {
			return fmt.Errorf("authentication failed: %w", result.err)
		}

		// Verify state matches
		if result.state != startResp.State {
			return fmt.Errorf("state mismatch: possible CSRF attack")
		}

		// Step 6: Exchange auth code for token
		tokenResp, err := exchangeAuthCode(apiURL, result.code, codeVerifier, startResp.State)
		if err != nil {
			return err
		}

		// Step 7: Save token to config
		expiresAt, _ := time.Parse(time.RFC3339, tokenResp.ExpiresAt)

		cfg := &config.GlobalConfig{
			Token:       tokenResp.Token,
			TokenID:     tokenResp.TokenID,
			TokenPrefix: tokenResp.TokenPrefix,
			TokenExpiry: expiresAt,
			UserID:      tokenResp.UserID,
			UserEmail:   tokenResp.UserEmail,
			APIBaseURL:  apiURL,
		}

		if err := config.SaveGlobalConfig(cfg); err != nil {
			return fmt.Errorf("failed to save config: %w", err)
		}

		fmt.Printf("\n✓ Successfully authenticated as %s\n", tokenResp.UserEmail)
		fmt.Printf("  Token expires: %s\n", expiresAt.Format(time.RFC1123))

		return nil

	case <-time.After(10 * time.Minute):
		return fmt.Errorf("authentication timed out")
	}
}

// generateCodeVerifier generates a random code verifier for PKCE
func generateCodeVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// computeCodeChallenge computes the SHA256 code challenge from verifier
func computeCodeChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

// findAvailablePort finds an available port and returns a listener
func findAvailablePort() (net.Listener, int, error) {
	// Try to find an available port starting from 54321
	for port := 54321; port < 54421; port++ {
		listener, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
		if err == nil {
			return listener, port, nil
		}
	}
	return nil, 0, fmt.Errorf("no available ports found")
}

type callbackResult struct {
	code  string
	state string
	err   error
}

// startCallbackServer starts a local HTTP server to receive the OAuth callback
func startCallbackServer(listener net.Listener, ch chan<- *callbackResult) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		errorMsg := r.URL.Query().Get("error")

		if errorMsg != "" {
			ch <- &callbackResult{err: fmt.Errorf("authentication error: %s", errorMsg)}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, callbackPageHTML("Authentication Failed", errorMsg, true))
			return
		}

		if code == "" {
			ch <- &callbackResult{err: fmt.Errorf("no authorization code received")}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, callbackPageHTML("Authentication Failed", "No authorization code received.", true))
			return
		}

		ch <- &callbackResult{code: code, state: state}

		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, callbackPageHTML("Authentication Successful!", "You can close this window and return to your terminal.", false))
	})

	server := &http.Server{Handler: mux}
	go server.Serve(listener)

	return server
}

// callAuthStart calls the /api/cli/auth/start endpoint
func callAuthStart(url string) (*AuthStartResponse, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to initiate auth: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp AuthStartResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("auth start failed: %s", errResp.Error)
		}
		return nil, fmt.Errorf("auth start failed (%d): %s", resp.StatusCode, string(body))
	}

	var startResp AuthStartResponse
	if err := json.Unmarshal(body, &startResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &startResp, nil
}

// exchangeAuthCode exchanges the auth code for a CLI token
func exchangeAuthCode(apiURL, authCode, codeVerifier, state string) (*AuthCallbackResponse, error) {
	reqBody := AuthCallbackRequest{
		AuthCode:     authCode,
		CodeVerifier: codeVerifier,
		State:        state,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/cli/auth/callback", apiURL)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(jsonData))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange token: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp AuthCallbackResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("token exchange failed: %s", errResp.Error)
		}
		return nil, fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, string(body))
	}

	var tokenResp AuthCallbackResponse
	if err := json.Unmarshal(body, &tokenResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &tokenResp, nil
}

// openBrowser opens the specified URL in the default browser
func openBrowser(url string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	return cmd.Start()
}

// callbackPageHTML generates a styled HTML page for the OAuth callback
func callbackPageHTML(title, message string, isError bool) string {
	iconColor := "#10b981" // green
	iconPath := `<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7"></path>`
	if isError {
		iconColor = "#ef4444" // red
		iconPath = `<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12"></path>`
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>%s - Kindship CLI</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif;
            background: linear-gradient(135deg, #0f172a 0%%, #1e293b 100%%);
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
            padding: 20px;
        }
        .card {
            background: #1e293b;
            border: 1px solid #334155;
            border-radius: 16px;
            padding: 48px;
            max-width: 420px;
            width: 100%%;
            text-align: center;
            box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.5);
        }
        .icon-container {
            width: 64px;
            height: 64px;
            border-radius: 50%%;
            background: %s20;
            display: flex;
            align-items: center;
            justify-content: center;
            margin: 0 auto 24px;
        }
        .icon {
            width: 32px;
            height: 32px;
            color: %s;
        }
        h1 {
            color: #f1f5f9;
            font-size: 24px;
            font-weight: 600;
            margin-bottom: 12px;
        }
        p {
            color: #94a3b8;
            font-size: 16px;
            line-height: 1.5;
        }
        .logo {
            margin-bottom: 32px;
            color: #f1f5f9;
            font-size: 20px;
            font-weight: 700;
            letter-spacing: -0.5px;
        }
    </style>
</head>
<body>
    <div class="card">
        <div class="logo">Kindship</div>
        <div class="icon-container">
            <svg class="icon" fill="none" stroke="%s" viewBox="0 0 24 24">
                %s
            </svg>
        </div>
        <h1>%s</h1>
        <p>%s</p>
    </div>
</body>
</html>`, title, iconColor, iconColor, iconColor, iconPath, title, message)
}

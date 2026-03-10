package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"

	"github.com/spf13/cobra"
)

var siteCmd = &cobra.Command{
	Use:   "site",
	Short: "Site hosting commands",
	Long: `Commands for managing hosted sites.

Subcommands:
  create   Create a new site
  list     List your sites
  status   Get site info and build status
  push     Upload project files
  logs     View build logs
  delete   Delete a site`,
}

var siteCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new site",
	Long: `Create a new hosted site with the given name.

Examples:
  kindship site create my-app
  kindship site create my-app --format json`,
	Args: cobra.ExactArgs(1),
	RunE: runSiteCreate,
}

var siteListCmd = &cobra.Command{
	Use:   "list",
	Short: "List your sites",
	Long: `List all sites for the current agent.

Examples:
  kindship site list
  kindship site list --format json`,
	RunE: runSiteList,
}

var siteStatusCmd = &cobra.Command{
	Use:   "status <name>",
	Short: "Get site info and build status",
	Long: `Get detailed information about a site including latest build status.

Examples:
  kindship site status my-app
  kindship site status my-app --format json`,
	Args: cobra.ExactArgs(1),
	RunE: runSiteStatus,
}

var sitePushCmd = &cobra.Command{
	Use:   "push",
	Short: "Upload project files to a site",
	Long: `Tar and upload project files to a site.

Automatically excludes .git/, node_modules/, .env*, *.pem, *.key,
and .woodpecker.yml. Enforces max 50MB compressed and 1000 files.

Examples:
  kindship site push --dir . --message "Initial deploy"
  kindship site push --dir ./dist`,
	Args: cobra.ExactArgs(1),
	RunE: runSitePush,
}

var siteLogsCmd = &cobra.Command{
	Use:   "logs <name>",
	Short: "View build logs",
	Long: `View build logs for a site. Shows the latest build by default.

Examples:
  kindship site logs my-app
  kindship site logs my-app --build 3
  kindship site logs my-app --format json`,
	Args: cobra.ExactArgs(1),
	RunE: runSiteLogs,
}

var siteDeleteCmd = &cobra.Command{
	Use:   "delete <name>",
	Short: "Delete a site",
	Long: `Delete a site and tear down all associated infrastructure.

Examples:
  kindship site delete my-app`,
	Args: cobra.ExactArgs(1),
	RunE: runSiteDelete,
}

var (
	siteFormat  string
	pushDir     string
	pushMessage string
	logsBuild   int
)

func init() {
	siteCreateCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")
	siteListCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")
	siteStatusCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")
	siteLogsCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")
	siteDeleteCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")

	sitePushCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")
	sitePushCmd.Flags().StringVar(&pushDir, "dir", ".", "Directory to upload")
	sitePushCmd.Flags().StringVar(&pushMessage, "message", "Deploy from agent", "Commit message")

	siteLogsCmd.Flags().IntVar(&logsBuild, "build", 0, "Build number (default: latest)")

	siteCmd.AddCommand(siteCreateCmd)
	siteCmd.AddCommand(siteListCmd)
	siteCmd.AddCommand(siteStatusCmd)
	siteCmd.AddCommand(sitePushCmd)
	siteCmd.AddCommand(siteLogsCmd)
	siteCmd.AddCommand(siteDeleteCmd)
	rootCmd.AddCommand(siteCmd)
}

func runSiteCreate(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	reqBody, err := json.Marshal(map[string]string{
		"site_name": args[0],
		"agent_id":  agentID,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/cli/site/create", ctx.APIBaseURL)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to create site: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var createResp api.SiteCreateResponse
	if err := json.Unmarshal(body, &createResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		if createResp.Error != "" {
			return fmt.Errorf("failed to create site: %s", createResp.Error)
		}
		return fmt.Errorf("failed to create site (%d): %s", resp.StatusCode, string(body))
	}

	if siteFormat == "json" {
		return printJSON(createResp)
	}

	site := createResp.Site
	fmt.Printf("✓ Created site %q\n", site.SiteName)
	fmt.Printf("  Domain:  %s\n", site.Domain)
	fmt.Printf("  Status:  %s\n", site.Status)
	if site.GiteaRepoURL != nil {
		fmt.Printf("  Repo:    %s\n", *site.GiteaRepoURL)
	}

	return nil
}

func runSiteList(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/api/cli/site/list?agent_id=%s", ctx.APIBaseURL, agentID)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to list sites: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp api.SiteListResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("failed to list sites: %s", errResp.Error)
		}
		return fmt.Errorf("failed to list sites (%d): %s", resp.StatusCode, string(body))
	}

	var listResp api.SiteListResponse
	if err := json.Unmarshal(body, &listResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if siteFormat == "json" {
		return printJSON(listResp)
	}

	if len(listResp.Sites) == 0 {
		fmt.Println("No sites found.")
		return nil
	}

	// Table header
	fmt.Printf("%-16s %-32s %-10s %s\n", "NAME", "DOMAIN", "STATUS", "LAST DEPLOY")
	for _, site := range listResp.Sites {
		lastDeploy := "-"
		if site.LastDeployAt != nil {
			lastDeploy = formatRelativeTime(*site.LastDeployAt)
		}
		fmt.Printf("%-16s %-32s %-10s %s\n", site.SiteName, site.Domain, site.Status, lastDeploy)
	}

	return nil
}

func runSiteStatus(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/api/cli/site/status?site_name=%s&agent_id=%s", ctx.APIBaseURL, args[0], agentID)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get site status: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp api.SiteStatusResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("failed: %s", errResp.Error)
		}
		return fmt.Errorf("failed (%d): %s", resp.StatusCode, string(body))
	}

	var statusResp api.SiteStatusResponse
	if err := json.Unmarshal(body, &statusResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if siteFormat == "json" {
		return printJSON(statusResp)
	}

	site := statusResp.Site
	fmt.Printf("Site: %s\n", site.SiteName)
	fmt.Printf("  Domain:  %s\n", site.Domain)
	fmt.Printf("  Status:  %s\n", site.Status)
	if statusResp.Build != nil {
		b := statusResp.Build
		age := formatRelativeTimestamp(b.FinishedAt)
		fmt.Printf("  Build:   #%d %s (%s)\n", b.Number, b.Status, age)
	} else {
		fmt.Printf("  Build:   none\n")
	}
	if site.LastError != nil {
		fmt.Printf("  Error:   %s\n", *site.LastError)
	}

	return nil
}

func runSitePush(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	siteName := args[0]

	// Resolve directory
	dir, err := filepath.Abs(pushDir)
	if err != nil {
		return fmt.Errorf("failed to resolve directory: %w", err)
	}

	// Create tar.gz archive in memory
	archiveBuf, fileCount, err := createArchive(dir)
	if err != nil {
		return err
	}

	if fileCount == 0 {
		return fmt.Errorf("no files to push in %s", dir)
	}

	// Build multipart request
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)

	_ = writer.WriteField("site_name", siteName)
	_ = writer.WriteField("agent_id", agentID)
	_ = writer.WriteField("message", pushMessage)

	part, err := writer.CreateFormFile("archive", "archive.tar.gz")
	if err != nil {
		return fmt.Errorf("failed to create form file: %w", err)
	}
	if _, err := io.Copy(part, archiveBuf); err != nil {
		return fmt.Errorf("failed to write archive to form: %w", err)
	}
	writer.Close()

	endpoint := fmt.Sprintf("%s/api/cli/site/push", ctx.APIBaseURL)
	req, err := http.NewRequest(http.MethodPost, endpoint, &requestBody)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to push files: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var pushResp api.SitePushResponse
	if err := json.Unmarshal(body, &pushResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if pushResp.Error != "" {
			return fmt.Errorf("push failed: %s", pushResp.Error)
		}
		return fmt.Errorf("push failed (%d): %s", resp.StatusCode, string(body))
	}

	if siteFormat == "json" {
		return printJSON(pushResp)
	}

	sha := pushResp.CommitSha
	if len(sha) > 7 {
		sha = sha[:7]
	}
	fmt.Printf("✓ Pushed %d files (commit %s)\n", pushResp.FilesPushed, sha)
	fmt.Println("  Build triggered")
	if len(pushResp.SkippedReserved) > 0 {
		fmt.Printf("  Skipped reserved: %s\n", strings.Join(pushResp.SkippedReserved, ", "))
	}
	if len(pushResp.SkippedDenied) > 0 {
		fmt.Printf("  Skipped denied: %s\n", strings.Join(pushResp.SkippedDenied, ", "))
	}

	return nil
}

func runSiteLogs(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/api/cli/site/logs?site_name=%s&agent_id=%s", ctx.APIBaseURL, args[0], agentID)
	if logsBuild > 0 {
		endpoint += fmt.Sprintf("&build_number=%d", logsBuild)
	}

	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get logs: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp api.SiteLogsResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("failed: %s", errResp.Error)
		}
		return fmt.Errorf("failed (%d): %s", resp.StatusCode, string(body))
	}

	var logsResp api.SiteLogsResponse
	if err := json.Unmarshal(body, &logsResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if siteFormat == "json" {
		return printJSON(logsResp)
	}

	fmt.Printf("Build #%d (%s)\n", logsResp.BuildNumber, logsResp.Status)
	for _, step := range logsResp.Steps {
		icon := statusIcon(step.Status)
		fmt.Printf("  [%s] %s\n", icon, step.Name)
		if step.Output != "" {
			// Indent output lines
			for _, line := range strings.Split(strings.TrimRight(step.Output, "\n"), "\n") {
				fmt.Printf("      %s\n", line)
			}
		}
	}

	return nil
}

func runSiteDelete(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	reqBody, err := json.Marshal(map[string]string{
		"site_name": args[0],
		"agent_id":  agentID,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/cli/site/delete", ctx.APIBaseURL)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete site: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	var deleteResp api.SiteDeleteResponse
	if err := json.Unmarshal(body, &deleteResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if deleteResp.Error != "" {
			return fmt.Errorf("failed to delete site: %s", deleteResp.Error)
		}
		return fmt.Errorf("failed to delete site (%d): %s", resp.StatusCode, string(body))
	}

	if siteFormat == "json" {
		return printJSON(deleteResp)
	}

	fmt.Printf("✓ Deleted site %q\n", args[0])

	return nil
}

// skipPatterns defines files/dirs to exclude from push archives
var skipPatterns = []string{
	".git",
	"node_modules",
	".woodpecker.yml",
}

// skipExtensions defines file extensions to exclude
var skipExtensions = []string{
	".pem",
	".key",
}

const (
	maxArchiveSize = 50 * 1024 * 1024 // 50MB compressed
	maxFileCount   = 1000
)

func createArchive(dir string) (*bytes.Buffer, int, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	fileCount := 0

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Get relative path
		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		// Skip root
		if relPath == "." {
			return nil
		}

		// Check skip patterns
		for _, pattern := range skipPatterns {
			if matchesPattern(relPath, pattern) {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}

		// Skip .env* files
		base := filepath.Base(relPath)
		if strings.HasPrefix(base, ".env") {
			return nil
		}

		// Skip by extension
		for _, ext := range skipExtensions {
			if strings.HasSuffix(base, ext) {
				return nil
			}
		}

		// Skip symlinks and directories (dirs are created implicitly)
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if info.IsDir() {
			return nil
		}

		// Regular files only
		if !info.Mode().IsRegular() {
			return nil
		}

		fileCount++
		if fileCount > maxFileCount {
			return fmt.Errorf("too many files (max %d)", maxFileCount)
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return fmt.Errorf("failed to create tar header for %s: %w", relPath, err)
		}
		// Use forward slashes and relative path
		header.Name = filepath.ToSlash(relPath)

		if err := tw.WriteHeader(header); err != nil {
			return fmt.Errorf("failed to write tar header: %w", err)
		}

		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("failed to open %s: %w", relPath, err)
		}
		defer f.Close()

		if _, err := io.Copy(tw, f); err != nil {
			return fmt.Errorf("failed to write %s to archive: %w", relPath, err)
		}

		return nil
	})

	if err != nil {
		return nil, 0, err
	}

	if err := tw.Close(); err != nil {
		return nil, 0, fmt.Errorf("failed to finalize tar: %w", err)
	}
	if err := gw.Close(); err != nil {
		return nil, 0, fmt.Errorf("failed to finalize gzip: %w", err)
	}

	if buf.Len() > maxArchiveSize {
		return nil, 0, fmt.Errorf("archive too large (%dMB, max %dMB)", buf.Len()/(1024*1024), maxArchiveSize/(1024*1024))
	}

	return &buf, fileCount, nil
}

func matchesPattern(path, pattern string) bool {
	// Check if any path component matches the pattern
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts {
		if part == pattern {
			return true
		}
	}
	return false
}

func formatRelativeTime(timeStr string) string {
	t, err := time.Parse(time.RFC3339, timeStr)
	if err != nil {
		return timeStr
	}
	return formatDuration(time.Since(t))
}

func formatRelativeTimestamp(unixTimestamp int64) string {
	if unixTimestamp == 0 {
		return "pending"
	}
	t := time.Unix(unixTimestamp, 0)
	return formatDuration(time.Since(t))
}

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		m := int(d.Minutes())
		if m == 1 {
			return "1m ago"
		}
		return fmt.Sprintf("%dm ago", m)
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		if h == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", h)
	}
	days := int(d.Hours() / 24)
	if days == 1 {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", days)
}

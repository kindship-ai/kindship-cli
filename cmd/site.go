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
	"os/exec"
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
  delete   Delete a site
  domain   Manage custom domains`,
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
	Use:   "push <name>",
	Short: "Publish a local site workspace",
	Long: `Publish a local site workspace to a Kindship hosted site.

Automatically excludes .git/, node_modules/, .env*, *.pem, *.key,
and .woodpecker.yml. Enforces max 50MB compressed and 1000 files.

Use --only to deploy a targeted subset of files from a dirty worktree.
With --only, 'push' clones the last committed tree (HEAD) into a temp
dir, overlays just the matching file(s) from the worktree on top, and
sends the archive with partial=true so the server updates only the
shipped paths and leaves every other remote file untouched.

Caveats for --only:
  * Each --only pattern must match at least one file (literal path or
    single-segment glob like '*.html'); zero matches fails fast.
  * Files matching push exclusions (.git, node_modules, .woodpecker.yml,
    .env*, *.pem, *.key) cannot be shipped via --only — the overlay
    step rejects the pattern rather than silently dropping it later.

Examples:
  kindship site push my-app
  kindship site push my-app --message "Initial deploy"
  kindship site push my-app --dir ./my-app
  kindship site push my-app --only index.html --only about.html
  kindship site push my-app --only 'blog/2026-*.html'`,
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

var siteDomainCmd = &cobra.Command{
	Use:   "domain",
	Short: "Manage custom domains",
	Long: `Commands for managing custom domains on sites.

Subcommands:
  set      Set a custom domain for a site
  status   Check domain DNS/TLS status
  remove   Remove a custom domain`,
}

var siteDomainSetCmd = &cobra.Command{
	Use:   "set <site> <domain>",
	Short: "Set a custom domain for a site",
	Long: `Register a custom domain for a site via Cloudflare for SaaS.

Only subdomains are supported (e.g., www.example.com, app.example.com).
Apex/root domains (e.g., example.com) are not supported.

Examples:
  kindship site domain set my-app www.example.com
  kindship site domain set my-app app.example.com`,
	Args: cobra.ExactArgs(2),
	RunE: runSiteDomainSet,
}

var siteDomainStatusCmd = &cobra.Command{
	Use:   "status <site>",
	Short: "Check domain DNS/TLS status",
	Long: `Check the status of a custom domain's DNS and TLS certificate.

Examples:
  kindship site domain status my-app`,
	Args: cobra.ExactArgs(1),
	RunE: runSiteDomainStatus,
}

var siteDomainRemoveCmd = &cobra.Command{
	Use:   "remove <site>",
	Short: "Remove a custom domain",
	Long: `Remove the custom domain from a site. The site will still be accessible
at its kindship.site subdomain. Triggers a redeploy to update routing.

Examples:
  kindship site domain remove my-app`,
	Args: cobra.ExactArgs(1),
	RunE: runSiteDomainRemove,
}

var siteDomainCheckCmd = &cobra.Command{
	Use:   "check <domain>",
	Short: "Check domain availability and price",
	Long: `Check if a domain is available for registration and get pricing info.

Examples:
  kindship site domain check example.com`,
	Args: cobra.ExactArgs(1),
	RunE: runSiteDomainCheck,
}

var siteDomainRegisterCmd = &cobra.Command{
	Use:   "register <site> <domain>",
	Short: "Register a new domain",
	Long: `Register a new domain via Kindship (powered by DNSimple).
The domain will be registered to Kindship with WHOIS privacy enabled.
Payment is processed via Stripe.

Examples:
  kindship site domain register my-app example.com`,
	Args: cobra.ExactArgs(2),
	RunE: runSiteDomainRegister,
}

var siteDomainRegisterStatusCmd = &cobra.Command{
	Use:   "register-status <site>",
	Short: "Check domain registration status",
	Long: `Check the registration status of a domain purchase.

Examples:
  kindship site domain register-status my-app`,
	Args: cobra.ExactArgs(1),
	RunE: runSiteDomainRegisterStatus,
}

var (
	siteFormat  string
	pushDir     string
	pushMessage string
	pushOnly    []string
	pushDryRun  bool
	logsBuild   int
)

const containerSiteWorkspaceRoot = "/workspace/sites"

func init() {
	siteCreateCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")
	siteListCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")
	siteStatusCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")
	siteLogsCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")
	siteDeleteCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")

	sitePushCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")
	sitePushCmd.Flags().StringVar(&pushDir, "dir", "", "Directory to upload (defaults to the canonical site workspace)")
	sitePushCmd.Flags().StringVar(&pushMessage, "message", "Deploy from agent", "Commit message")
	sitePushCmd.Flags().StringArrayVar(&pushOnly, "only", nil, "Clone HEAD into a temp dir, overlay only the matching file(s) from the worktree, and push the overlay. Repeatable. Requires at least one commit on the site repo.")
	sitePushCmd.Flags().BoolVar(&pushDryRun, "dry-run", false, "Inspect the archive and report what would change without triggering a build. Returns changed files and affected routes.")

	siteLogsCmd.Flags().IntVar(&logsBuild, "build", 0, "Build number (default: latest)")

	siteDomainCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")
	siteDomainSetCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")
	siteDomainStatusCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")
	siteDomainRemoveCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")

	siteDomainCheckCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")
	siteDomainRegisterCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")
	siteDomainRegisterStatusCmd.Flags().StringVar(&siteFormat, "format", "text", "Output format (json, text)")

	siteDomainCmd.AddCommand(siteDomainSetCmd)
	siteDomainCmd.AddCommand(siteDomainStatusCmd)
	siteDomainCmd.AddCommand(siteDomainRemoveCmd)
	siteDomainCmd.AddCommand(siteDomainCheckCmd)
	siteDomainCmd.AddCommand(siteDomainRegisterCmd)
	siteDomainCmd.AddCommand(siteDomainRegisterStatusCmd)

	siteCmd.AddCommand(siteCreateCmd)
	siteCmd.AddCommand(siteListCmd)
	siteCmd.AddCommand(siteStatusCmd)
	siteCmd.AddCommand(sitePushCmd)
	siteCmd.AddCommand(siteLogsCmd)
	siteCmd.AddCommand(siteDeleteCmd)
	siteCmd.AddCommand(siteDomainCmd)
	rootCmd.AddCommand(siteCmd)
}

// handleNonJSONResponse checks if a response body is non-JSON (e.g., Cloudflare
// HTML error pages on 502/503) and returns an error with the HTTP status and a
// body snippet. Returns nil if the body looks like JSON and should be parsed.
func handleNonJSONResponse(statusCode int, body []byte) error {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		return nil // looks like JSON, caller should parse it
	}
	snippet := string(body)
	if len(snippet) > 200 {
		snippet = snippet[:200] + "..."
	}
	return fmt.Errorf("server returned HTTP %d with non-JSON body: %s", statusCode, snippet)
}

func defaultSiteWorkspaceDir(ctx *auth.Context, siteName string) (string, error) {
	if strings.TrimSpace(siteName) == "" || strings.ContainsAny(siteName, `/\`) || siteName == "." || siteName == ".." {
		return "", fmt.Errorf("invalid site name for workspace path: %q", siteName)
	}

	if override := strings.TrimSpace(os.Getenv("KINDSHIP_SITE_WORKSPACE_ROOT")); override != "" {
		root, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("failed to resolve KINDSHIP_SITE_WORKSPACE_ROOT: %w", err)
		}
		return filepath.Join(root, siteName), nil
	}

	if ctx.IsContainerMode() {
		return filepath.Join(containerSiteWorkspaceRoot, siteName), nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine home directory: %w", err)
	}

	return filepath.Join(homeDir, "kindship", "sites", siteName), nil
}

func resolveSiteWorkspaceDir(ctx *auth.Context, siteName, explicitDir string) (string, error) {
	if strings.TrimSpace(explicitDir) == "" {
		return defaultSiteWorkspaceDir(ctx, siteName)
	}

	return filepath.Abs(explicitDir)
}

func localSiteGitIdentity(ctx *auth.Context, siteName string) (string, string) {
	name := "Kindship Site Agent"

	switch {
	case ctx.IsContainerMode() && ctx.AgentID != "":
		return name, fmt.Sprintf("agent-%s@kindship.ai", ctx.AgentID)
	case ctx.UserEmail != "":
		return name, ctx.UserEmail
	default:
		return name, fmt.Sprintf("site-%s@kindship.ai", siteName)
	}
}

func runGit(dir string, args ...string) error {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func ensureLocalSiteWorkspace(ctx *auth.Context, siteName, dir string) (bool, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, fmt.Errorf("failed to create site workspace: %w", err)
	}

	hadUserFiles, err := workspaceHasUserFiles(dir)
	if err != nil {
		return false, fmt.Errorf("failed to inspect site workspace: %w", err)
	}

	bootstrapped := false
	gitDir := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		if !os.IsNotExist(err) {
			return false, fmt.Errorf("failed to inspect site workspace: %w", err)
		}

		if err := runGit("", "init", dir); err != nil {
			return false, fmt.Errorf("failed to initialize local site repo: %w", err)
		}
		bootstrapped = true
	}

	name, email := localSiteGitIdentity(ctx, siteName)
	if err := runGit(dir, "config", "user.name", name); err != nil {
		return false, fmt.Errorf("failed to configure git user.name: %w", err)
	}
	if err := runGit(dir, "config", "user.email", email); err != nil {
		return false, fmt.Errorf("failed to configure git user.email: %w", err)
	}

	wroteGitignore, err := writeFileIfAbsent(filepath.Join(dir, ".gitignore"), []byte(defaultSiteGitignore), 0o644)
	if err != nil {
		return false, fmt.Errorf("failed to write .gitignore: %w", err)
	}
	if wroteGitignore {
		bootstrapped = true
	}
	wroteReadme, err := writeFileIfAbsent(filepath.Join(dir, "README.md"), []byte(defaultSiteReadme(siteName, dir)), 0o644)
	if err != nil {
		return false, fmt.Errorf("failed to write README.md: %w", err)
	}
	if wroteReadme {
		bootstrapped = true
	}
	if !hadUserFiles {
		wroteStarter, err := writeFileIfAbsent(filepath.Join(dir, "index.html"), []byte(defaultSiteIndexHTML(siteName)), 0o644)
		if err != nil {
			return false, fmt.Errorf("failed to write starter index.html: %w", err)
		}
		if wroteStarter {
			bootstrapped = true
		}
	}

	return bootstrapped, nil
}

func requireLocalSiteWorkspaceRepo(dir string) error {
	gitDir := filepath.Join(dir, ".git")
	if info, err := os.Stat(gitDir); err != nil || !info.IsDir() {
		return fmt.Errorf("site workspace %s is not a git repo; run 'kindship site create <name>' or initialize git there first", dir)
	}

	return nil
}

// buildOverlayFromHEAD creates a temp directory containing a snapshot
// of the last committed tree (HEAD) overlaid with files from the
// worktree that match the given glob patterns. Caller must remove the
// returned directory when done.
//
// This is the implementation for `site push --only`. The intent is to
// deploy a targeted subset of files from a dirty worktree without
// shipping unrelated uncommitted state — the archive shape stays
// "full site" so the server-side full-replace push does not wipe
// remote files that the overlay does not cover.
//
// Semantics:
//   - HEAD must exist. `site create` initializes a repo but doesn't
//     commit, so a fresh site with no commits cannot use --only;
//     we fail fast with a clear error pointing at the fix.
//   - Each pattern is matched against the worktree (relative paths).
//     Matching files are copied over the HEAD snapshot. Untracked
//     files are included. Files deleted in the worktree but still
//     present in HEAD survive — --only is additive, it cannot remove
//     files from the remote.
//   - If no pattern matches any file, we fail fast rather than push
//     an unmodified HEAD snapshot (which would be surprising).
func buildOverlayFromHEAD(repoDir string, patterns []string) (string, error) {
	// 1. Verify HEAD exists.
	rev := exec.Command("git", "-C", repoDir, "rev-parse", "--verify", "HEAD")
	if out, err := rev.CombinedOutput(); err != nil {
		return "", fmt.Errorf(
			"--only requires at least one commit on the site repo at %s, "+
				"but HEAD does not resolve (%s). Commit once and retry, "+
				"or omit --only to push the current worktree directly.",
			repoDir, strings.TrimSpace(string(out)),
		)
	}

	// 2. Create temp overlay directory.
	overlayDir, err := os.MkdirTemp("", "kindship-site-only-*")
	if err != nil {
		return "", fmt.Errorf("failed to create overlay temp dir: %w", err)
	}
	// On any error below, clean up before returning.
	cleanup := func() { _ = os.RemoveAll(overlayDir) }

	// 3. Extract HEAD tree into the overlay via `git archive | tar -x`.
	archive := exec.Command("git", "-C", repoDir, "archive", "HEAD")
	untar := exec.Command("tar", "-x", "-C", overlayDir)
	pipe, err := archive.StdoutPipe()
	if err != nil {
		cleanup()
		return "", fmt.Errorf("failed to pipe git archive: %w", err)
	}
	untar.Stdin = pipe
	untar.Stderr = os.Stderr
	archive.Stderr = os.Stderr
	if err := untar.Start(); err != nil {
		cleanup()
		return "", fmt.Errorf("failed to start tar extract: %w", err)
	}
	if err := archive.Run(); err != nil {
		_ = untar.Wait()
		cleanup()
		return "", fmt.Errorf("git archive HEAD failed: %w", err)
	}
	if err := untar.Wait(); err != nil {
		cleanup()
		return "", fmt.Errorf("tar extract failed: %w", err)
	}

	// 4. Overlay matching files from the worktree. We walk the worktree
	// once so patterns can match nested paths (single-segment globs via
	// filepath.Match). Rejection rules:
	//   - Files matching the archive exclude list (skipPatterns /
	//     skipExtensions / .env*) are rejected at overlay time so the
	//     operator sees a clear error instead of a "Pushed N files"
	//     success that silently drops the requested file.
	//   - Each pattern must match at least one non-excluded file. A
	//     glob that matches zero files is almost always a typo, so we
	//     fail the entire push.
	perPatternMatches := make(map[string]int, len(patterns))
	var excludedMatches []string
	walkErr := filepath.Walk(repoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(repoDir, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if rel == ".git" || strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		// Skip symlinks and non-regular files — createArchive drops
		// these too, so --only should not smuggle them in.
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil
		}
		// Evaluate every pattern against this file and count all
		// matches. A file matched by multiple patterns credits all of
		// them — otherwise overlapping patterns like --only '*.html'
		// --only index.html would false-fail zero-match on the
		// narrower one.
		anyMatch := false
		for _, pat := range patterns {
			ok, matchErr := filepath.Match(pat, rel)
			if matchErr != nil {
				return fmt.Errorf("invalid --only pattern %q: %w", pat, matchErr)
			}
			if !ok {
				continue
			}
			perPatternMatches[pat]++
			anyMatch = true
		}
		if !anyMatch {
			return nil
		}
		if isExcludedFromArchive(rel) {
			excludedMatches = append(excludedMatches, rel)
			return nil
		}
		dst := filepath.Join(overlayDir, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("failed to create overlay dir for %s: %w", rel, err)
		}
		if err := copyFileOverwrite(path, dst); err != nil {
			return fmt.Errorf("failed to overlay %s: %w", rel, err)
		}
		return nil
	})
	if walkErr != nil {
		cleanup()
		return "", walkErr
	}

	// Reject excluded files before reporting unmatched patterns — a
	// single bad match is enough to stop the push.
	if len(excludedMatches) > 0 {
		cleanup()
		return "", fmt.Errorf(
			"--only cannot ship files excluded from the push archive "+
				"(.git, node_modules, .woodpecker.yml, .env*, *.pem, *.key): %s",
			strings.Join(excludedMatches, ", "),
		)
	}

	// 5. Every pattern must match at least one file. A pattern that
	// matched nothing is almost always a typo, and ignoring it would
	// silently ship a subset of what the operator requested.
	var unmatched []string
	for _, pat := range patterns {
		if perPatternMatches[pat] == 0 {
			unmatched = append(unmatched, pat)
		}
	}
	if len(unmatched) > 0 {
		cleanup()
		return "", fmt.Errorf(
			"--only pattern(s) matched zero files in %s: %s",
			repoDir, strings.Join(unmatched, ", "),
		)
	}

	return overlayDir, nil
}

// isExcludedFromArchive mirrors the exclusion rules in createArchive.
// Kept in sync with skipPatterns, skipExtensions, and the .env*
// prefix skip so --only rejects an excluded file at overlay time
// rather than letting the archiver drop it silently.
func isExcludedFromArchive(rel string) bool {
	for _, pat := range skipPatterns {
		if matchesPattern(rel, pat) {
			return true
		}
	}
	base := filepath.Base(rel)
	if strings.HasPrefix(base, ".env") {
		return true
	}
	for _, ext := range skipExtensions {
		if strings.HasSuffix(base, ext) {
			return true
		}
	}
	return false
}

// copyFileOverwrite copies src to dst, replacing dst if it exists.
func copyFileOverwrite(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func workspaceHasUserFiles(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}

	for _, entry := range entries {
		switch entry.Name() {
		case ".git", ".gitignore", "README.md":
			continue
		default:
			return true, nil
		}
	}

	return false, nil
}

func writeFileIfAbsent(path string, contents []byte, mode os.FileMode) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !os.IsNotExist(err) {
		return false, err
	}

	if err := os.WriteFile(path, contents, mode); err != nil {
		return false, err
	}

	return true, nil
}

func defaultSiteReadme(siteName, dir string) string {
	return fmt.Sprintf("# %s\n\nLocal workspace for the Kindship hosted site `%s`.\n\n- Workspace: `%s`\n- Deploy: `kindship site push %s`\n\nDeployment uses the Kindship CLI upload path. Treat this local repository as the source of truth.\n", siteName, siteName, filepath.ToSlash(dir), siteName)
}

func defaultSiteIndexHTML(siteName string) string {
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>%s</title>
  <style>
    body {
      margin: 0;
      min-height: 100vh;
      display: grid;
      place-items: center;
      font-family: "Helvetica Neue", Helvetica, Arial, sans-serif;
      background: linear-gradient(135deg, #f5f7fb 0%%, #e7eef8 100%%);
      color: #1f2937;
    }
    main {
      width: min(40rem, calc(100%% - 3rem));
      padding: 3rem;
      border-radius: 1.5rem;
      background: rgba(255, 255, 255, 0.9);
      box-shadow: 0 24px 60px rgba(15, 23, 42, 0.12);
    }
    h1 {
      margin: 0 0 1rem;
      font-size: clamp(2rem, 6vw, 3.5rem);
      line-height: 1;
    }
    p {
      margin: 0.75rem 0;
      font-size: 1.05rem;
      line-height: 1.6;
    }
    code {
      padding: 0.15rem 0.4rem;
      border-radius: 999px;
      background: #e2e8f0;
      font-size: 0.95em;
    }
  </style>
</head>
<body>
  <main>
    <h1>%s</h1>
    <p>Your Kindship site workspace is ready.</p>
    <p>Edit this file or replace it with your app, then deploy with <code>kindship site push %s</code>.</p>
  </main>
</body>
</html>
`, siteName, siteName, siteName)
}

const defaultSiteGitignore = `.DS_Store
.env
.env.*
.next/
dist/
build/
node_modules/
`

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

	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return fmt.Errorf("failed to create site: %w", err)
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
	if createResp.Site == nil {
		return fmt.Errorf("site created but API response did not include site details")
	}

	site := createResp.Site
	workspaceDir, err := defaultSiteWorkspaceDir(ctx, site.SiteName)
	if err != nil {
		return err
	}
	bootstrapped, err := ensureLocalSiteWorkspace(ctx, site.SiteName, workspaceDir)
	if err != nil {
		return fmt.Errorf("site %q created, but failed to bootstrap local workspace: %w", site.SiteName, err)
	}

	createResp.WorkspaceDir = workspaceDir
	createResp.WorkspaceBootstrapped = bootstrapped

	if siteFormat == "json" {
		return printJSON(createResp)
	}

	fmt.Printf("✓ Created site %q\n", site.SiteName)
	fmt.Printf("  Domain:  %s\n", site.Domain)
	fmt.Printf("  Status:  %s\n", site.Status)
	fmt.Printf("  Local:   %s\n", workspaceDir)
	if site.GiteaRepoURL != nil {
		fmt.Printf("  Repo:    %s\n", *site.GiteaRepoURL)
	}
	if bootstrapped {
		fmt.Printf("  Workspace bootstrapped\n")
	} else {
		fmt.Printf("  Workspace already present\n")
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

	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return fmt.Errorf("failed to list sites: %w", err)
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
	fmt.Printf("%-16s %-32s %-10s %-24s %s\n", "NAME", "DOMAIN", "STATUS", "CUSTOM DOMAIN", "LAST DEPLOY")
	for _, site := range listResp.Sites {
		lastDeploy := "-"
		if site.LastDeployAt != nil {
			lastDeploy = formatRelativeTime(*site.LastDeployAt)
		}
		customDomain := ""
		if site.CustomDomain != nil {
			customDomain = *site.CustomDomain
		}
		fmt.Printf("%-16s %-32s %-10s %-24s %s\n", site.SiteName, site.Domain, site.Status, customDomain, lastDeploy)
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

	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return fmt.Errorf("failed to get site status: %w", err)
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
	if site.CustomDomain != nil {
		fmt.Printf("  Custom:  %s\n", *site.CustomDomain)
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

	// Resolve site workspace
	dir, err := resolveSiteWorkspaceDir(ctx, siteName, pushDir)
	if err != nil {
		return fmt.Errorf("failed to resolve directory: %w", err)
	}
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("site workspace not found at %s; run 'kindship site create %s' first or pass --dir", dir, siteName)
		}
		return fmt.Errorf("failed to access %s: %w", dir, err)
	}
	if err := requireLocalSiteWorkspaceRepo(dir); err != nil {
		return err
	}

	// Source directory to archive. With --only, we build an overlay tree
	// (HEAD snapshot + selected worktree files) in a temp dir and point
	// the archiver at that instead of the dirty worktree.
	sourceDir := dir
	if len(pushOnly) > 0 {
		overlayDir, err := buildOverlayFromHEAD(dir, pushOnly)
		if err != nil {
			return err
		}
		defer os.RemoveAll(overlayDir)
		sourceDir = overlayDir
	}

	// Create tar.gz archive in memory
	archiveBuf, fileCount, err := createArchive(sourceDir)
	if err != nil {
		return err
	}

	if fileCount == 0 {
		return fmt.Errorf("no files to push in %s", dir)
	}

	// Buffer the archive once. Both --dry-run (legacy multipart) and
	// the new two-step push flow (init → PUT → finalize) need to read
	// the same bytes; archiveBuf is a *bytes.Buffer that can only be
	// drained once, so we materialize the bytes here.
	archiveBytes := archiveBuf.Bytes()
	partial := len(pushOnly) > 0

	// --dry-run still uses the legacy multipart endpoint. Dry-runs
	// inspect the archive contents server-side without touching Gitea
	// or storage, so the path doesn't need the signed-URL flow.
	if pushDryRun {
		dryResp, err := postSitePushDryRun(ctx, dryRunArgs{
			siteName:     siteName,
			agentID:      agentID,
			message:      pushMessage,
			partial:      partial,
			archiveBytes: archiveBytes,
		})
		if err != nil {
			return err
		}
		if siteFormat == "json" {
			return printJSON(dryResp)
		}
		return renderDryRun(*dryResp, dir)
	}

	// Two-step push: ask the server for a signed upload URL, PUT the
	// tarball directly to Supabase Storage (bypasses Vercel's 4.5MB
	// function body cap), then finalize so the server can dispatch
	// the Hatchet workflow against the staged archive.
	initResp, err := postSitePushInit(ctx, api.SitePushInitRequest{
		SiteName:    siteName,
		AgentID:     agentID,
		ArchiveSize: int64(len(archiveBytes)),
	})
	if err != nil {
		return err
	}

	if err := putSiteArchiveToSignedURL(initResp.UploadURL, archiveBytes); err != nil {
		return fmt.Errorf("failed to upload archive: %w", err)
	}

	pushResp, err := postSitePushFinalize(ctx, api.SitePushFinalizeRequest{
		SiteName:    siteName,
		AgentID:     agentID,
		StagingPath: initResp.StagingPath,
		Message:     pushMessage,
		Partial:     partial,
	})
	if err != nil {
		return err
	}

	pushResp.SourceDir = dir

	if siteFormat == "json" {
		return printJSON(pushResp)
	}

	sha := pushResp.CommitSha
	if len(sha) > 7 {
		sha = sha[:7]
	}
	fmt.Printf("✓ Pushed %d files (commit %s)\n", pushResp.FilesPushed, sha)
	fmt.Printf("  Source:  %s\n", dir)
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

	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return fmt.Errorf("failed to get logs: %w", err)
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

	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return fmt.Errorf("failed to delete site: %w", err)
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

func runSiteDomainSet(cmd *cobra.Command, args []string) error {
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
		"domain":    args[1],
		"agent_id":  agentID,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/cli/site/domain", ctx.APIBaseURL)
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
		return fmt.Errorf("failed to set custom domain: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return fmt.Errorf("failed to set custom domain: %w", err)
	}

	var domainResp api.SiteDomainResponse
	if err := json.Unmarshal(body, &domainResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if domainResp.Error != "" {
			return fmt.Errorf("failed to set custom domain: %s", domainResp.Error)
		}
		return fmt.Errorf("failed to set custom domain (%d): %s", resp.StatusCode, string(body))
	}

	if siteFormat == "json" {
		return printJSON(domainResp)
	}

	fmt.Printf("✓ Custom domain %s registered (status: %s)\n", domainResp.CustomDomain, domainResp.Status)

	if domainResp.DnsAutoConfigured {
		fmt.Printf("\n  ✓ CNAME record auto-created at %s\n", domainResp.DnsProvider)
	} else {
		if domainResp.DnsError != "" {
			fmt.Printf("\n  ⚠ Auto-DNS failed: %s\n", domainResp.DnsError)
		}
		fmt.Println()
		fmt.Println("  Add this DNS record:")
		fmt.Printf("    CNAME  %s  →  %s\n", domainResp.CustomDomain, domainResp.CnameTarget)
		fmt.Println()
		fmt.Println("  Note: Apex domains (example.com) are not supported — use a subdomain (www).")
	}
	fmt.Println()
	fmt.Printf("  Then check:  kindship site domain status %s\n", args[0])

	return nil
}

func runSiteDomainStatus(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/api/cli/site/domain?site_name=%s&agent_id=%s", ctx.APIBaseURL, args[0], agentID)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get domain status: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return fmt.Errorf("failed to get domain status: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp api.SiteDomainResponse
		if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("failed: %s", errResp.Error)
		}
		return fmt.Errorf("failed (%d): %s", resp.StatusCode, string(body))
	}

	var domainResp api.SiteDomainResponse
	if err := json.Unmarshal(body, &domainResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if siteFormat == "json" {
		return printJSON(domainResp)
	}

	fmt.Printf("Domain: %s\n", domainResp.CustomDomain)
	fmt.Printf("  Status:  %s\n", domainResp.Status)
	fmt.Printf("  SSL:     %s\n", domainResp.SSLStatus)
	fmt.Printf("  CNAME:   %s\n", domainResp.CnameTarget)

	return nil
}

func runSiteDomainRemove(cmd *cobra.Command, args []string) error {
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

	endpoint := fmt.Sprintf("%s/api/cli/site/domain", ctx.APIBaseURL)
	req, err := http.NewRequest(http.MethodDelete, endpoint, bytes.NewBuffer(reqBody))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to remove custom domain: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return fmt.Errorf("failed to remove custom domain: %w", err)
	}

	var removeResp api.SiteDomainRemoveResponse
	if err := json.Unmarshal(body, &removeResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if removeResp.Error != "" {
			return fmt.Errorf("failed to remove custom domain: %s", removeResp.Error)
		}
		return fmt.Errorf("failed to remove custom domain (%d): %s", resp.StatusCode, string(body))
	}

	if siteFormat == "json" {
		return printJSON(removeResp)
	}

	fmt.Printf("✓ Custom domain removed from %s\n", args[0])

	return nil
}

func runSiteDomainCheck(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/api/cli/site/domain/check?domain=%s", ctx.APIBaseURL, args[0])
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to check domain: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return fmt.Errorf("failed to check domain: %w", err)
	}

	var checkResp api.DomainCheckResponse
	if err := json.Unmarshal(body, &checkResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if checkResp.Error != "" {
			return fmt.Errorf("failed: %s", checkResp.Error)
		}
		return fmt.Errorf("failed (%d): %s", resp.StatusCode, string(body))
	}

	if siteFormat == "json" {
		return printJSON(checkResp)
	}

	fmt.Printf("Domain: %s\n", checkResp.Domain)
	if checkResp.Available {
		fmt.Println("  Status:  AVAILABLE")
	} else {
		fmt.Println("  Status:  NOT AVAILABLE")
	}
	if checkResp.Premium {
		fmt.Println("  Type:    Premium")
	}
	if checkResp.Price != nil {
		fmt.Printf("  Register: $%.2f %s\n", float64(checkResp.Price.RegistrationCents)/100, checkResp.Price.Currency)
		fmt.Printf("  Renewal:  $%.2f %s/year\n", float64(checkResp.Price.RenewalCents)/100, checkResp.Price.Currency)
	}

	return nil
}

func runSiteDomainRegister(cmd *cobra.Command, args []string) error {
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
		"domain":    args[1],
		"agent_id":  agentID,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/cli/site/domain/register", ctx.APIBaseURL)
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
		return fmt.Errorf("failed to register domain: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return fmt.Errorf("failed to register domain: %w", err)
	}

	var regResp api.DomainRegisterResponse
	if err := json.Unmarshal(body, &regResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if regResp.Error != "" {
			return fmt.Errorf("failed: %s", regResp.Error)
		}
		return fmt.Errorf("failed (%d): %s", resp.StatusCode, string(body))
	}

	if siteFormat == "json" {
		return printJSON(regResp)
	}

	fmt.Printf("✓ Domain registration started: %s\n", regResp.Domain)
	fmt.Printf("  Hostname:  %s\n", regResp.RequestedHostname)
	fmt.Printf("  Price:     $%.2f\n", float64(regResp.PriceCents)/100)
	fmt.Printf("  Status:    %s\n", regResp.Status)
	if regResp.CheckoutURL != "" {
		fmt.Printf("\n  Complete payment: %s\n", regResp.CheckoutURL)
	}

	return nil
}

func runSiteDomainRegisterStatus(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}

	endpoint := fmt.Sprintf("%s/api/cli/site/domain/register?site_name=%s&agent_id=%s", ctx.APIBaseURL, args[0], agentID)
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	ctx.SetAuthHeaders(req)
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to get registration status: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if err := handleNonJSONResponse(resp.StatusCode, body); err != nil {
		return fmt.Errorf("failed to get registration status: %w", err)
	}

	var regResp api.DomainRegisterResponse
	if err := json.Unmarshal(body, &regResp); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if regResp.Error != "" {
			return fmt.Errorf("failed: %s", regResp.Error)
		}
		return fmt.Errorf("failed (%d): %s", resp.StatusCode, string(body))
	}

	if siteFormat == "json" {
		return printJSON(regResp)
	}

	fmt.Printf("Domain: %s\n", regResp.Domain)
	fmt.Printf("  Hostname:  %s\n", regResp.RequestedHostname)
	fmt.Printf("  Status:    %s\n", regResp.Status)
	if regResp.LastError != "" {
		fmt.Printf("  Error:     %s\n", regResp.LastError)
	}

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
			return fmt.Errorf(
				"too many files (max %d). Use --only <glob> to ship a subset per call; the remote preserves files not listed. See `kindship site push --help`",
				maxFileCount,
			)
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
		return nil, 0, fmt.Errorf(
			"archive too large (%dMB, max %dMB). Use --only <glob> to ship a subset per call; the remote preserves files not listed. See `kindship site push --help`",
			buf.Len()/(1024*1024),
			maxArchiveSize/(1024*1024),
		)
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

// renderDryRun prints a human-friendly summary of what a push would
// change. Routes are shown first (agents care about which URLs move);
// file-level changes come second; skipped files are surfaced only when
// present.
func renderDryRun(resp api.SitePushDryRunResponse, sourceDir string) error {
	mode := "full push"
	if resp.Partial {
		mode = "partial (--only) push"
	}
	fmt.Printf("Dry run — %s from %s\n", mode, sourceDir)
	fmt.Printf("  %d files in archive, %d would change\n", resp.FilesInArchive, len(resp.Changes))

	if len(resp.AffectedRoutes) > 0 {
		fmt.Println("\nAffected routes:")
		for _, r := range resp.AffectedRoutes {
			fmt.Printf("  %s\n", r)
		}
	}

	if len(resp.Changes) > 0 {
		added, modified, deleted := 0, 0, 0
		for _, c := range resp.Changes {
			switch c.Status {
			case "added":
				added++
			case "modified":
				modified++
			case "deleted":
				deleted++
			}
		}
		fmt.Printf("\nChanges: %d added, %d modified, %d deleted\n", added, modified, deleted)
		// Cap to 50 lines to keep output bounded; agents who want full
		// detail can rerun with --format json.
		limit := 50
		if len(resp.Changes) < limit {
			limit = len(resp.Changes)
		}
		for i := 0; i < limit; i++ {
			c := resp.Changes[i]
			sym := "?"
			switch c.Status {
			case "added":
				sym = "A"
			case "modified":
				sym = "M"
			case "deleted":
				sym = "D"
			}
			fmt.Printf("  %s %s\n", sym, c.Path)
		}
		if len(resp.Changes) > limit {
			fmt.Printf("  ... and %d more (use --format json for full list)\n", len(resp.Changes)-limit)
		}
	}

	if len(resp.SkippedReserved) > 0 {
		fmt.Printf("\nSkipped reserved: %s\n", strings.Join(resp.SkippedReserved, ", "))
	}
	if len(resp.SkippedDenied) > 0 {
		fmt.Printf("Skipped denied: %s\n", strings.Join(resp.SkippedDenied, ", "))
	}

	fmt.Println("\nNo build was triggered.")
	return nil
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

// ─── Two-step push helpers ──────────────────────────────────────

// postSitePushInit asks the server for a signed upload URL the CLI
// can PUT the tarball against. Mirrors video publish-init.
func postSitePushInit(
	ctx *auth.Context, body api.SitePushInitRequest,
) (*api.SitePushInitResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal init request: %w", err)
	}
	httpReq, err := http.NewRequest(
		http.MethodPost,
		ctx.APIBaseURL+"/api/cli/site/push/init",
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("build init request: %w", err)
	}
	ctx.SetAuthHeaders(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("POST site push init: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read init response: %w", err)
	}
	if err := handleNonJSONResponse(resp.StatusCode, respBody); err != nil {
		return nil, err
	}
	var out api.SitePushInitResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("parse init response: %w: %s", err, firstBytes(respBody, 300))
	}
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" {
			return nil, fmt.Errorf("push init %d: %s", resp.StatusCode, out.Error)
		}
		return nil, fmt.Errorf("push init %d: %s", resp.StatusCode, firstBytes(respBody, 300))
	}
	if out.UploadURL == "" || out.StagingPath == "" {
		return nil, fmt.Errorf("init response missing required fields: %s", firstBytes(respBody, 300))
	}
	return &out, nil
}

// putSiteArchiveToSignedURL PUTs the tarball body to the presigned
// URL returned by /init. Supabase signed upload URLs accept the body
// directly with Content-Type signalling; no extra headers needed.
//
// Generous timeout: bucket caps at 50MB, uploads over typical
// container egress (~25 Mbit/s) take ~16s at the cap. 5 minutes is
// pathological headroom so a throttled network won't time out before
// the upload completes.
func putSiteArchiveToSignedURL(uploadURL string, body []byte) error {
	req, err := http.NewRequest(http.MethodPut, uploadURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build PUT: %w", err)
	}
	req.Header.Set("Content-Type", "application/gzip")
	req.ContentLength = int64(len(body))

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("PUT: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("PUT %d: %s", resp.StatusCode, firstBytes(raw, 300))
	}
	return nil
}

// postSitePushFinalize tells the server the tarball is staged and
// ready to be extracted + committed.
func postSitePushFinalize(
	ctx *auth.Context, body api.SitePushFinalizeRequest,
) (*api.SitePushResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal finalize request: %w", err)
	}
	httpReq, err := http.NewRequest(
		http.MethodPost,
		ctx.APIBaseURL+"/api/cli/site/push/finalize",
		bytes.NewReader(payload),
	)
	if err != nil {
		return nil, fmt.Errorf("build finalize request: %w", err)
	}
	ctx.SetAuthHeaders(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("X-Kindship-CLI-Version", Version)

	// Finalize waits on the Hatchet workflow; the server budgets ~4.5
	// minutes internally before timing out. Our client timeout is a
	// touch longer so we see the server's 424 instead of a connection
	// reset.
	client := &http.Client{Timeout: 6 * time.Minute}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("POST site push finalize: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read finalize response: %w", err)
	}
	if err := handleNonJSONResponse(resp.StatusCode, respBody); err != nil {
		return nil, err
	}
	var out api.SitePushResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("parse finalize response: %w: %s", err, firstBytes(respBody, 300))
	}
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" {
			return nil, fmt.Errorf("push failed: %s", out.Error)
		}
		return nil, fmt.Errorf("push failed (%d): %s", resp.StatusCode, firstBytes(respBody, 300))
	}
	return &out, nil
}

// dryRunArgs bundles the inputs to the legacy dry-run multipart endpoint.
type dryRunArgs struct {
	siteName     string
	agentID      string
	message      string
	partial      bool
	archiveBytes []byte
}

// postSitePushDryRun keeps the legacy multipart call for --dry-run.
// Dry-run inspects the archive without touching Gitea/storage, so it
// doesn't need the new signed-URL flow.
func postSitePushDryRun(
	ctx *auth.Context, args dryRunArgs,
) (*api.SitePushDryRunResponse, error) {
	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)
	_ = writer.WriteField("site_name", args.siteName)
	_ = writer.WriteField("agent_id", args.agentID)
	_ = writer.WriteField("message", args.message)
	if args.partial {
		_ = writer.WriteField("partial", "true")
	}
	part, err := writer.CreateFormFile("archive", "archive.tar.gz")
	if err != nil {
		return nil, fmt.Errorf("failed to create form file: %w", err)
	}
	if _, err := part.Write(args.archiveBytes); err != nil {
		return nil, fmt.Errorf("failed to write archive to form: %w", err)
	}
	writer.Close()

	endpoint := fmt.Sprintf("%s/api/cli/site/push/dry-run", ctx.APIBaseURL)
	req, err := http.NewRequest(http.MethodPost, endpoint, &requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Kindship-CLI-Version", Version)

	client := &http.Client{Timeout: 300 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to push files: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}
	if err := handleNonJSONResponse(resp.StatusCode, respBody); err != nil {
		return nil, fmt.Errorf("dry-run failed: %w", err)
	}

	var dryResp api.SitePushDryRunResponse
	if err := json.Unmarshal(respBody, &dryResp); err != nil {
		return nil, fmt.Errorf("failed to parse dry-run response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		if dryResp.Error != "" {
			return nil, fmt.Errorf("dry-run failed: %s", dryResp.Error)
		}
		return nil, fmt.Errorf("dry-run failed (%d): %s", resp.StatusCode, firstBytes(respBody, 300))
	}
	return &dryResp, nil
}

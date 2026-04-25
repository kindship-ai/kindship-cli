package api

// SiteInfo represents a site returned from the API
type SiteInfo struct {
	ID               string  `json:"id"`
	SiteName         string  `json:"site_name"`
	Domain           string  `json:"domain"`
	Status           string  `json:"status"`
	GiteaRepoURL     *string `json:"gitea_repo_url"`
	WoodpeckerRepoID *int    `json:"woodpecker_repo_id"`
	LastDeployAt     *string `json:"last_deploy_at"`
	LastDeploySha    *string `json:"last_deploy_sha"`
	LastError        *string `json:"last_error"`
	CustomDomain     *string `json:"custom_domain"`
	CreatedAt        string  `json:"created_at"`
}

// SiteBuild represents build info from the status endpoint
type SiteBuild struct {
	Number     int    `json:"number"`
	Status     string `json:"status"`
	Commit     string `json:"commit"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at"`
}

// SiteBuildStep represents a single step in build logs
type SiteBuildStep struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Output string `json:"output"`
}

// SiteCreateResponse is the response from POST /api/cli/site/create
type SiteCreateResponse struct {
	Site                  *SiteInfo `json:"site,omitempty"`
	WorkspaceDir          string    `json:"workspace_dir,omitempty"`
	WorkspaceBootstrapped bool      `json:"workspace_bootstrapped"`
	Error                 string    `json:"error,omitempty"`
}

// SiteListResponse is the response from GET /api/cli/site/list
type SiteListResponse struct {
	Sites []SiteInfo `json:"sites"`
	Error string     `json:"error,omitempty"`
}

// SiteStatusResponse is the response from GET /api/cli/site/status
type SiteStatusResponse struct {
	Site  *SiteInfo  `json:"site,omitempty"`
	Build *SiteBuild `json:"build"`
	Error string     `json:"error,omitempty"`
}

// SitePushResponse is the response from POST /api/cli/site/push (legacy
// multipart endpoint) and from /api/cli/site/push/finalize (the second
// step of the new two-step flow). Both share the same success shape so
// callers can render either path identically.
type SitePushResponse struct {
	CommitSha       string   `json:"commit_sha"`
	FilesPushed     int      `json:"files_pushed"`
	Message         string   `json:"message"`
	SourceDir       string   `json:"source_dir,omitempty"`
	SkippedReserved []string `json:"skipped_reserved"`
	SkippedDenied   []string `json:"skipped_denied"`
	Error           string   `json:"error,omitempty"`
}

// SitePushInitRequest is the request body for POST /api/cli/site/push/init.
// archive_size lets the server reject pushes above the staging bucket cap
// before the CLI wastes an upload.
type SitePushInitRequest struct {
	SiteName    string `json:"site_name"`
	AgentID     string `json:"agent_id,omitempty"`
	ArchiveSize int64  `json:"archive_size"`
}

// SitePushInitResponse is the response from POST /api/cli/site/push/init.
// upload_url is a short-lived Supabase Storage signed PUT URL; the CLI
// uploads the tar.gz directly to it (no Vercel function body cap).
type SitePushInitResponse struct {
	StagingPath string `json:"staging_path"`
	UploadURL   string `json:"upload_url"`
	UploadToken string `json:"upload_token"`
	Error       string `json:"error,omitempty"`
}

// SitePushFinalizeRequest is the request body for POST /api/cli/site/push/finalize.
// staging_path must match the path returned from /init for the same site.
type SitePushFinalizeRequest struct {
	SiteName    string `json:"site_name"`
	AgentID     string `json:"agent_id,omitempty"`
	StagingPath string `json:"staging_path"`
	Message     string `json:"message,omitempty"`
	Partial     bool   `json:"partial,omitempty"`
}

// SitePushDryRunChange describes a single file that would change in a push.
// Status values mirror git diff semantics: "added", "modified", "deleted".
type SitePushDryRunChange struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Size   int    `json:"size,omitempty"`
}

// SitePushDryRunResponse is the response from POST /api/cli/site/push/dry-run.
// Reports what a push would change without triggering a build.
type SitePushDryRunResponse struct {
	FilesInArchive  int                    `json:"files_in_archive"`
	Changes         []SitePushDryRunChange `json:"changes"`
	AffectedRoutes  []string               `json:"affected_routes"`
	SkippedReserved []string               `json:"skipped_reserved"`
	SkippedDenied   []string               `json:"skipped_denied"`
	Partial         bool                   `json:"partial"`
	Error           string                 `json:"error,omitempty"`
}

// SiteLogsResponse is the response from GET /api/cli/site/logs
type SiteLogsResponse struct {
	BuildNumber int             `json:"build_number"`
	Status      string          `json:"status"`
	Steps       []SiteBuildStep `json:"steps"`
	Error       string          `json:"error,omitempty"`
}

// SiteDeleteResponse is the response from POST /api/cli/site/delete
type SiteDeleteResponse struct {
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// SiteDomainResponse is the response from custom domain set/status endpoints
type SiteDomainResponse struct {
	CustomDomain      string `json:"custom_domain"`
	Status            string `json:"status"`
	SSLStatus         string `json:"ssl_status"`
	CnameTarget       string `json:"cname_target"`
	DnsAutoConfigured bool   `json:"dns_auto_configured"`
	DnsProvider       string `json:"dns_provider,omitempty"`
	DnsError          string `json:"dns_error,omitempty"`
	Error             string `json:"error,omitempty"`
}

// SiteDomainRemoveResponse is the response from DELETE /api/cli/site/domain
type SiteDomainRemoveResponse struct {
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

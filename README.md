# Kindship CLI

Kindship CLI supports:
- **Local development**: authenticate as a user, bind a repo to an agent, and run/complete tasks (optionally driven by Claude Code hooks).
- **Agent containers**: securely inject secrets into subprocesses via a service key (Kindship infra).

## Local Development (Claude Code Hook Loop)

### Quickstart

```bash
kindship login
cd /path/to/your/repo
kindship setup
```

Then start Claude Code in that repo. `kindship setup` installs:
- `.claude/settings.local.json` hooks (SessionStart + Stop)
- `.claude/skills/kindship.yaml` slash commands (`/kindship next`, `/kindship complete`, `/kindship fail`, `/kindship status`)

### How The Loop Works

On session start, Claude Code runs:
- `kindship hook start`
  - checks for an existing active run via the API (`/api/cli/agent/active-run`)
  - if none, fetches the next task (`/api/cli/plan/next`) and **claims it** by starting an execution run (`/api/planning/execution/start`)
  - prints markdown task context to stdout (Claude sees it as session context)

On session stop, Claude Code runs:
- `kindship hook stop --auto-continue`
  - fetches the current active run from the API (`/api/cli/agent/active-run`)
  - **auto-completes** the run (`/api/planning/execution/{id}/complete`) using the session summary
  - fetches and claims the next task
  - if a next task exists, returns `{"decision":"block","reason":"..."}` to keep the loop going

The loop stops normally when there are no more tasks, or when the transcript contains a user request to stop.

### Manual Local Commands

If you want to drive locally without hooks:

```bash
kindship run local-next
kindship run local-complete
kindship run local-fail --reason "blocked on X"
kindship status --local
```

`local-complete` and `local-fail` accept optional `--outputs` JSON matching `api.ExecutionOutputs`.

### Plan Management

```bash
# Submit a plan (creates PROJECT + TASKs)
kindship plan submit plan.json

# Export an entity tree as flat JSON
kindship plan export <entity-id>
kindship plan export <entity-id> --output plan.json
kindship plan export <entity-id> --include-deleted

# Import entities (upsert: existing UUIDs updated, new UUIDs created)
kindship plan import plan.json
kindship plan export <id> | kindship plan import   # round-trip pipe

# Get next executable task
kindship plan next
```

### Entity CRUD

```bash
kindship entity list
kindship entity list --type TASK --status ACTIVE
kindship entity create --type TASK --title "My Task" --parent <parent-id>
kindship entity update <id> --status ACTIVE
kindship entity delete <id>
```

### Hosted Sites

```bash
kindship site create my-app
kindship site push my-app
kindship site status my-app
```

`kindship site create` now bootstraps a local site workspace repo for the agent. By default the workspace lives at:
- container mode: `/workspace/sites/<site-name>`
- local mode: `~/kindship/sites/<site-name>`

`kindship site push <site-name>` deploys from that canonical workspace unless `--dir` is explicitly provided.

### Videos

```bash
kindship video publish intro --title "Meet Ripple"
```

`kindship video publish <slug>` tars an agent's Remotion bundle
(`composition.mjs` + `compositions.json` + any `public/` assets, plus an
optional `poster.png` at the workspace root) and uploads it to the
Kindship publish pipeline. Each call creates a new immutable revision of
the same logical video, so re-publishing under the same slug is how you
iterate — the Videos tab in the web UI always plays the latest.

Drop a `poster.png` at the workspace root (produced by
`npx remotion still <composition-id> poster.png --frame=<N> --scale=0.5`)
and the Videos tab will use it as the thumbnail. Only the exact lowercase
filename `poster.png` is recognized; missing poster is fine — the tab
falls back to a generic icon.

By default the video workspace lives at `/workspace/videos/<slug>/` in
container mode. The full authoring workflow — scaffold, compose, esbuild
to `composition.mjs`, emit `compositions.json`, render the poster,
publish — is documented in the `kindship-video` skill.

#### Flags

| Flag                | Required      | Description                                                                                                |
| ------------------- | ------------- | ---------------------------------------------------------------------------------------------------------- |
| `--title`           | yes           | Human-readable title shown in the Videos tab.                                                              |
| `--description`     | no            | One-sentence description.                                                                                   |
| `--composition-id`  | conditionally | Required only when `compositions.json` lists more than one composition. Otherwise the only entry is auto-selected. |
| `--input-props`     | no            | JSON object of default props for `<Player>`. Falls back to the composition's `defaultProps`, then `{}`.     |
| `--dir`             | no            | Override the workspace dir (default `/workspace/videos/<slug>/`).                                           |
| `--format`          | no            | Output format: `text` (default) or `json`.                                                                  |

`kindship video list` and `kindship video delete` are stubbed in the
current release; manage existing videos through the Videos tab in the web
UI for now.

## Agent Containers (Service Key Mode)

The original `kindship auth` flow is for Kindship agent containers. It fetches secrets from the Kindship API and injects them as environment variables into a subprocess.

## Usage (Container Mode)

```bash
kindship auth <command> [args...]
kindship auth --verbose <command> [args...]  # Debug mode
kindship update                               # Self-update to latest version
```

### Examples

```bash
# Run Claude Code with automatic credential injection
kindship auth claude "what files are in this directory?"

# Run Codex with credentials
kindship auth codex "fix the bug in main.go"

# Run Gemini with credentials
kindship auth gemini "explain this code"

# Debug mode - shows detailed logs
kindship auth --verbose claude "what is 2+2"

# Pass flags to the underlying CLI
kindship auth claude --dangerously-skip-permissions "list files"
```

### Verbose Mode

Use `--verbose` or `-v` to enable detailed logging for debugging:

```bash
kindship auth -v claude "test"
```

Output includes:
- Environment variable validation
- API request URL and headers
- Response status and timing
- Secrets fetched (values masked)
- Executable path resolution
- Total setup time

## How It Works

1. Reads `AGENT_ID` and `KINDSHIP_SERVICE_KEY` from environment
2. Calls `GET /api/agent-containers/{agentId}/secrets?command={command}`
3. API validates IP whitelist and service key
4. Returns environment variables for the specified command
5. Sets env vars (e.g., `CLAUDE_CODE_OAUTH_TOKEN`) in subprocess
6. Replaces current process with the target command via `exec`

## Environment Variables

Required (set by container at creation):

| Variable | Description |
|----------|-------------|
| `AGENT_ID` | UUID of the agent |
| `KINDSHIP_SERVICE_KEY` | Auth key for API requests |
| `KINDSHIP_API_URL` | API base URL (default: `https://kindship.ai`) |

## Building

```bash
# Build for current platform
go build -o kindship .

# Build for Linux ARM64 (container target)
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o kindship .
```

## Project Structure

```
kindship-cli/
├── main.go                 # Entry point
├── cmd/
│   ├── root.go            # Root command setup
│   ├── auth.go            # 'kindship auth' command
│   ├── hook.go            # Claude Code hook handlers (local dev loop)
│   ├── setup.go           # Repo binding + Claude Code integration setup
│   ├── status.go          # Status + local run status
│   ├── plan.go            # Plan submit/export/import/next (local)
│   ├── entity.go          # Entity CRUD commands (local)
│   ├── login.go           # OAuth login (local)
│   ├── agent.go           # 'kindship agent' command
│   └── update.go          # 'kindship update' command
├── internal/
│   ├── api/
│   │   ├── client.go      # API client for fetching secrets and plan data
│   │   └── models.go      # Request/response types for all API endpoints
│   └── logging/
│       └── axiom.go       # Axiom structured logging
├── go.mod
└── go.sum
```

## API Endpoint

The CLI calls:

```
GET /api/agent-containers/{agentId}/secrets?command={command}
Headers:
  X-Kindship-Service-Key: {serviceKey}
```

Response:
```json
{
  "env": {
    "CLAUDE_CODE_OAUTH_TOKEN": "..."
  }
}
```

## Security

- **IP Whitelist**: API only responds to known agent server IPs
- **Service Key**: Unique per container, validated on every request
- **No disk writes**: Credentials are injected into subprocess memory only
- **Process replacement**: Uses `exec` syscall, credentials never in shell history

## Self-Update

The CLI can update itself without rebuilding the Docker image:

```bash
kindship update
```

This automatically detects your platform and downloads the correct binary.

### How Updates Work

1. Release created by pushing version tag (e.g., `v0.1.3`)
2. GoReleaser builds binaries for all 6 platforms (linux/darwin/windows × amd64/arm64)
3. Binaries uploaded to GitHub releases
4. `kindship update` auto-detects platform and downloads correct binary
5. API endpoint extracts binary from archive and serves it

### Supported Platforms

- Linux: amd64, arm64
- macOS (Darwin): amd64, arm64
- Windows: amd64, arm64

### Binary Proxy Endpoint

Multi-platform downloads via API proxy:

```
GET https://kindship.ai/cli/kindship?os={linux|darwin|windows}&arch={amd64|arm64}
```

This endpoint:
- Fetches the latest release from GitHub (using server-side `GITHUB_TOKEN`)
- Extracts platform-specific binary from archive
- Streams the binary to the client
- Requires no authentication from the client
- Falls back to legacy linux/arm64 binary if no platform specified

For more details on creating releases, see [RELEASE.md](./RELEASE.md).

## Updating AI CLIs

The AI CLIs (Claude, Gemini, Codex) are installed via npm in a user-writable location and can be updated without rebuilding the Docker image:

```bash
# Update Claude Code CLI
npm update -g @anthropic-ai/claude-code

# Update Gemini CLI
npm update -g @google/gemini-cli

# Update Codex CLI
npm update -g @openai/codex

# Or update all at once
npm update -g @anthropic-ai/claude-code @google/gemini-cli @openai/codex
```

## Integration

The kindship CLI is:
1. Built in the first stage of the Dockerfile (`golang:1.22-alpine`)
2. Copied to `/home/autonomous/.local/bin/kindship` in the final image
3. Available to the `autonomous` user in all containers
4. Can self-update via `kindship update`

The AI CLIs (Claude, Gemini, Codex) are:
1. Installed via npm to `/home/autonomous/.npm-global/bin/`
2. User-writable for updates via `npm update -g`

See `infra/agent-container/Dockerfile` for build details.

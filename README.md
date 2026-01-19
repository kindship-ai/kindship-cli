# Kindship CLI

Go binary for secure credential injection in agent containers. Fetches secrets from the Kindship API and injects them as environment variables into CLI subprocesses.

## Usage

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
apps/kindship-cli/
├── main.go                 # Entry point
├── cmd/
│   ├── root.go            # Root command setup
│   ├── auth.go            # 'kindship auth' command
│   └── update.go          # 'kindship update' command
├── internal/
│   ├── api/
│   │   └── client.go      # API client for fetching secrets
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

This downloads the latest binary from `https://kindship.ai/cli/kindship` and replaces the current executable.

### How Updates Work

1. GitHub Actions builds the CLI on every push to `apps/kindship-cli/**`
2. Binary is uploaded to GitHub releases at tag `kindship-latest`
3. The `/cli/kindship` API route proxies downloads from the private repo
4. `kindship update` downloads and replaces itself

### Binary Proxy Endpoint

Since the repo is private, the binary is served via an API proxy:

```
GET https://kindship.ai/cli/kindship
```

This endpoint:
- Fetches the latest release from GitHub (using server-side `GITHUB_TOKEN`)
- Streams the binary to the client
- Requires no authentication from the client

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

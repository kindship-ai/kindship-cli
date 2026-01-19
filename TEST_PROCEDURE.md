# Kindship CLI Test Procedure

This document outlines the manual test procedure for validating the CLI authentication and planning features.

## Prerequisites

1. Local Supabase running: `pnpm supabase:web:start`
2. Web app running: `pnpm dev`
3. CLI built: `cd apps/kindship-cli && go build -o kindship .`
4. At least one agent created in the web app
5. `CLI_TOKEN_SECRET` set in `.env.local`

## Test 1: CLI Version

```bash
./kindship version
```

**Expected:** Shows version number (e.g., `kindship version 0.1.0`)

---

## Test 2: Pre-Auth Status Check

```bash
./kindship whoami
```

**Expected:**
```
Not authenticated.
Run 'kindship login' to authenticate.
```

---

## Test 3: Login Flow (OAuth/PKCE)

```bash
./kindship login --api-url http://localhost:4000
```

**Expected Sequence:**
1. CLI prints "Authenticating with Kindship..."
2. CLI prints "Opening browser for authentication..."
3. Browser opens to `http://localhost:4000/cli/auth?state=...&callback_port=...&code_challenge=...`
4. If not logged in, shows login form
5. After authentication, browser shows "Authentication Successful!"
6. CLI prints "✓ Successfully authenticated as [email]"
7. CLI prints token expiry date

**Verify:**
- Check `~/.kindship/config.json` exists with correct permissions:
  ```bash
  ls -la ~/.kindship/config.json
  # Should show -rw------- (600 permissions)
  ```
- Check config content:
  ```bash
  cat ~/.kindship/config.json | jq .
  # Should contain: token, token_id, token_prefix, user_id, user_email, token_expiry
  ```

---

## Test 4: Post-Auth Verification

```bash
./kindship whoami
```

**Expected:**
```
Logged in as: [your-email]
Token prefix: [first-8-chars]...
Token expires: [date]

API: http://localhost:4000
```

---

## Test 5: Status Command (Outside Repo)

```bash
cd /tmp
./kindship status
```

**Expected:**
```
Kindship CLI Status
===================

Authentication:
  ✓ Logged in as [email]
  Token expires: [date]

Repository:
  ✗ Not in a git repository
```

---

## Test 6: Setup Command

```bash
cd /path/to/your/git/repo
kindship setup --api-url http://localhost:4000
```

**Expected Sequence:**
1. Shows "Repository root: /path/to/repo"
2. Shows "Authenticated as: [email]"
3. Lists available agents with numbers
4. Prompts to select an agent
5. After selection: "✓ Repository linked to agent '[name]'"
6. Shows "✓ Claude Code hooks installed"

**Verify:**
```bash
# Check repo config
cat .kindship/config.json | jq .

# Check hooks installed
ls -la .claude/hooks/
# Should show start.yaml and stop.yaml

# Check skills installed
ls -la .claude/skills/
# Should show kindship.yaml
```

---

## Test 7: Status Command (Inside Repo)

```bash
./kindship status
```

**Expected:**
```
Kindship CLI Status
===================

Authentication:
  ✓ Logged in as [email]
  Token expires: [date]

Repository:
  ✓ Git repository: /path/to/repo
  ✓ Agent bound: [agent-id]
    Slug: [agent-slug]
    Bound at: [date]

Claude Code Integration:
  ✓ Hooks installed

API: http://localhost:4000
```

---

## Test 8: Plan Submit

Create a test plan file:

```bash
cat > /tmp/test-plan.json << 'EOF'
{
  "title": "Test Project",
  "description": "A test project for CLI validation",
  "tasks": [
    {"title": "Task 1", "description": "First test task"},
    {"title": "Task 2", "description": "Second test task"},
    {"title": "Task 3", "description": "Third test task"}
  ]
}
EOF
```

Submit the plan:

```bash
./kindship plan submit /tmp/test-plan.json
```

**Expected:**
```
✓ Created project 'Test Project' with 3 tasks
  Project ID: [uuid]
  [1] Task 1 ([uuid])
  [2] Task 2 ([uuid])
  [3] Task 3 ([uuid])
```

---

## Test 9: Plan Next

```bash
./kindship plan next
```

**Expected (if no ACTIVE tasks):**
```json
{
  "task": null,
  "message": "No executable tasks found"
}
```

**Note:** Tasks are created in DRAFT status. To test plan next with actual tasks, you need to activate them in the database or through the web UI.

---

## Test 10: JSON Output

```bash
./kindship status --json | jq .
./kindship whoami --json | jq .
```

**Expected:** Valid JSON output with all fields populated.

---

## Test 11: Logout (Single Token)

```bash
./kindship logout
```

**Expected:**
```
✓ Successfully logged out
```

**Verify:**
```bash
./kindship whoami
# Should show: Not authenticated.

cat ~/.kindship/config.json | jq .token
# Should show: null or empty
```

---

## Test 12: Re-Login and Logout All

```bash
# Login again
./kindship login --api-url http://localhost:4000

# Verify logged in
./kindship whoami

# Logout all tokens
./kindship logout --all
```

**Expected:**
```
✓ Successfully logged out (all tokens revoked)
```

---

## Test 13: Hook Commands (Manual)

These are called by Claude Code hooks, but can be tested manually:

```bash
# Start hook (returns agent/task context)
./kindship hook start

# Stop hook (with mock summary file)
echo '{"summary": "Test session"}' > /tmp/summary.json
./kindship hook stop --summary-file /tmp/summary.json
```

**Expected:** JSON output with agent information.

---

## Test 14: Error Handling

### Invalid Token
```bash
# Manually corrupt the token
echo '{"token": "invalid"}' > ~/.kindship/config.json
./kindship whoami
```

**Expected:** Error message about invalid/expired token.

### Expired Token
```bash
# Set token_expiry to past date in config
./kindship whoami
```

**Expected:** "token expired: run 'kindship login' to refresh"

### No Agent Configured
```bash
rm .kindship/config.json
./kindship plan next
```

**Expected:** Error about no agent configured.

---

## Database Verification

After running tests, verify in the database:

```sql
-- Check CLI tokens
SELECT id, user_id, token_prefix, created_at, last_used_at, revoked_at
FROM cli_tokens
ORDER BY created_at DESC
LIMIT 5;

-- Check auth flows
SELECT id, state, user_id, completed_at, expires_at
FROM cli_auth_flows
ORDER BY created_at DESC
LIMIT 5;

-- Check created planning entities
SELECT id, type, title, status, created_at
FROM planning_entities
WHERE title LIKE 'Test%'
ORDER BY created_at DESC;
```

---

## Cleanup

```bash
# Remove test config
rm -rf ~/.kindship
rm -rf .kindship
rm -rf .claude/hooks .claude/skills

# Remove test plan file
rm /tmp/test-plan.json
```

---

## Test Matrix

| Test | Command | Expected Result | Pass/Fail |
|------|---------|-----------------|-----------|
| 1 | `kindship version` | Shows version | |
| 2 | `kindship whoami` (unauth) | Not authenticated | |
| 3 | `kindship login` | Browser auth flow | |
| 4 | `kindship whoami` (auth) | Shows email/token | |
| 5 | `kindship status` (no repo) | Shows auth, no repo | |
| 6 | `kindship setup` | Creates config, hooks | |
| 7 | `kindship status` (repo) | Shows full status | |
| 8 | `kindship plan submit` | Creates project/tasks | |
| 9 | `kindship plan next` | Returns next task | |
| 10 | JSON output flags | Valid JSON | |
| 11 | `kindship logout` | Revokes token | |
| 12 | `kindship logout --all` | Revokes all tokens | |
| 13 | Hook commands | Returns context | |
| 14 | Error handling | Proper messages | |

---

## Notes

- The CLI uses `http://localhost:4000` for local dev; production uses `https://kindship.ai`
- Tokens expire after 14 days
- Auth flows expire after 10 minutes
- Config files must have 0600 permissions (owner read/write only)

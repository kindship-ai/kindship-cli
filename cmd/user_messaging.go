package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/spf13/cobra"
)

// User Messaging — agent → user channel with agency tagging. See
// /Users/Testsson/.claude/plans/snazzy-leaping-stonebraker.md.

var userMessagingCmd = &cobra.Command{
	Use:     "user-messaging",
	Aliases: []string{"um"},
	Short:   "Send messages to the user (ask, choice, approve, report)",
	Long: `User Messaging is how you — the agent — communicate with the user:
asking questions, proposing choices, requesting approval, and reporting
results.

Every message is tagged with one of your 12 agencies (or 'other'). The
agencies are YOUR domains of autonomy; the user configures an oversight
level on each (silent / report / approval). If the user has set an agency
to 'silent' they are telling you to operate fully autonomously in that
area — the CLI will refuse to deliver a silent-tagged message and return
{"suppressed": true, ...}. Treat that refusal as an instruction, not an
error.

Agents must NEVER block waiting for answers. Send, keep working, check
back later via 'status' or 'list'. Messages do not expire by default so
the user can answer days later.

Subcommands:
  ask        Ask a free-text question
  choice     Pick one of N options
  approve    Yes/no approval on a proposed action
  report     One-way status update (user sees, doesn't need to respond)
  status     Fetch a single message by id
  list       List messages for the current agent
  remind     Nudge a pending message (push a fresh notification)
  retract    Cancel a pending message (still in DB, hidden from inbox)
  dispose    Mark an answered/acknowledged/expired message as reviewed
  agencies   List your agencies and the user's oversight over each`,
}

// ---- shared flags ----

var (
	umAgency              string
	umTitle               string
	umJSON                bool
	umChoiceOption        []string // --option "value:label"
	umExpiresIn           string   // parsed as duration (e.g. "30m", "2h", "7d")
	umUrgency             string   // ambient | daily | urgent | critical
	umTldr                string   // ≤140-char subject
	umOnAnswer            string   // ≤2KB self-reminder for context-free pickup
	umListStatus          string
	umListAgency          string
	umListLimit           int
	umListIncludeFixtures bool
	umListIncludeDisposed bool
	umDisposeCode         string
	umDisposeNote         string
)

// ---- ask ----

var userMessagingAskCmd = &cobra.Command{
	Use:   "ask <question...>",
	Short: "Ask the user a free-text question",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runUserMessagingAsk,
}

func runUserMessagingAsk(_ *cobra.Command, args []string) error {
	return sendUserMessage(api.UserMessagingSendRequest{
		Type:         "question",
		Agency:       umAgency,
		Body:         strings.Join(args, " "),
		Title:        umTitle,
		Urgency:      umUrgency,
		Tldr:         umTldr,
		OnAnswerNote: umOnAnswer,
	})
}

// ---- choice ----

var userMessagingChoiceCmd = &cobra.Command{
	Use:   "choice <question...>",
	Short: "Ask the user to pick one of N options",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runUserMessagingChoice,
}

func runUserMessagingChoice(_ *cobra.Command, args []string) error {
	if len(umChoiceOption) < 2 {
		return fmt.Errorf("choice requires at least two --option <value:label> flags")
	}
	choices := make([]api.UserMessageChoice, 0, len(umChoiceOption))
	for _, raw := range umChoiceOption {
		parts := strings.SplitN(raw, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("--option must be value:label (got %q)", raw)
		}
		choices = append(choices, api.UserMessageChoice{Value: parts[0], Label: parts[1]})
	}
	return sendUserMessage(api.UserMessagingSendRequest{
		Type:         "choice",
		Agency:       umAgency,
		Body:         strings.Join(args, " "),
		Title:        umTitle,
		Choices:      choices,
		Urgency:      umUrgency,
		Tldr:         umTldr,
		OnAnswerNote: umOnAnswer,
	})
}

// ---- approve ----

var userMessagingApproveCmd = &cobra.Command{
	Use:   "approve <proposed-action...>",
	Short: "Ask the user for yes/no approval on a proposed action",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runUserMessagingApprove,
}

func runUserMessagingApprove(_ *cobra.Command, args []string) error {
	return sendUserMessage(api.UserMessagingSendRequest{
		Type:         "approval",
		Agency:       umAgency,
		Body:         strings.Join(args, " "),
		Title:        umTitle,
		Urgency:      umUrgency,
		Tldr:         umTldr,
		OnAnswerNote: umOnAnswer,
	})
}

// ---- report ----

var userMessagingReportCmd = &cobra.Command{
	Use:   "report <summary...>",
	Short: "Send a one-way status update (no response expected)",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runUserMessagingReport,
}

func runUserMessagingReport(_ *cobra.Command, args []string) error {
	return sendUserMessage(api.UserMessagingSendRequest{
		Type:         "report",
		Agency:       umAgency,
		Body:         strings.Join(args, " "),
		Title:        umTitle,
		Urgency:      umUrgency,
		Tldr:         umTldr,
		OnAnswerNote: umOnAnswer,
	})
}

// ---- send helper shared by ask/choice/approve/report ----

func sendUserMessage(req api.UserMessagingSendRequest) error {
	if req.Agency == "" {
		return fmt.Errorf("--agency is required (one of 12 canonical agencies or 'other')")
	}
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}
	agentID, err := ctx.RequireAgentID()
	if err != nil {
		return err
	}
	req.AgentID = agentID

	if umExpiresIn != "" {
		secs, err := parseDurationSeconds(umExpiresIn)
		if err != nil {
			return fmt.Errorf("--expires-in: %w", err)
		}
		req.ExpiresInSeconds = secs
	}

	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	endpoint := fmt.Sprintf("%s/api/cli/user-messaging/send", ctx.APIBaseURL)
	httpReq, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBuffer(body))
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}
	ctx.SetAuthHeaders(httpReq)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Kindship-CLI-Version", Version)

	respBody, statusCode, err := doUserMessagingHTTP(httpReq)
	if err != nil {
		return err
	}

	var out api.UserMessagingSendResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return fmt.Errorf("failed to parse response (status %d): %s", statusCode, string(respBody))
	}
	if statusCode >= 400 {
		if out.Error != "" {
			return fmt.Errorf("send failed: %s", out.Error)
		}
		return fmt.Errorf("send failed (%d): %s", statusCode, string(respBody))
	}

	if umJSON {
		return printJSON(out)
	}
	if out.Suppressed {
		// Intentional success path: the user has opted out of this agency.
		// The agent should treat this as "operate autonomously here" and
		// carry on. Do not exit non-zero.
		fmt.Printf("✓ Suppressed by agency silent-mode (%s): %s\n", out.Agency, out.Reason)
		return nil
	}
	id := ""
	if out.ID != nil {
		id = *out.ID
	}
	// Surface delivery policy so agents learn the cadence contract. In Phase B
	// this is always 'push_now'; Phase C introduces deferred/bundled/wake
	// variants that callers MUST trust (don't re-send if delivery is in the
	// future).
	policy := out.DeliveryPolicy
	if policy == "" {
		policy = "push_now"
	}
	suffix := ""
	if out.EstimatedDeliveryAt != nil && *out.EstimatedDeliveryAt != "" {
		suffix = fmt.Sprintf(" → %s at %s", policy, *out.EstimatedDeliveryAt)
	} else {
		suffix = fmt.Sprintf(" → %s", policy)
	}
	fmt.Printf("✓ Sent (%s): %s%s\n", req.Type, id, suffix)
	return nil
}

// ---- status ----

var userMessagingStatusCmd = &cobra.Command{
	Use:   "status <id>",
	Short: "Fetch a single message by id (single-shot; no long-poll)",
	Args:  cobra.ExactArgs(1),
	RunE:  runUserMessagingStatus,
}

func runUserMessagingStatus(_ *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}
	u, _ := url.Parse(fmt.Sprintf("%s/api/cli/user-messaging/status", ctx.APIBaseURL))
	q := u.Query()
	q.Set("id", args[0])
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("failed to build request: %w", err)
	}
	ctx.SetAuthHeaders(req)
	req.Header.Set("X-Kindship-CLI-Version", Version)

	body, statusCode, err := doUserMessagingHTTP(req)
	if err != nil {
		return err
	}
	if statusCode == http.StatusNotFound {
		return fmt.Errorf("not found")
	}
	if statusCode >= 400 {
		return fmt.Errorf("status failed (%d): %s", statusCode, string(body))
	}

	var row api.UserMessage
	if err := json.Unmarshal(body, &row); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if umJSON {
		return printJSON(row)
	}
	fmt.Printf("%s  [%s] %s\n", row.ID, row.Agency, row.Type)
	fmt.Printf("  status:   %s\n", row.Status)
	if row.Title != nil && *row.Title != "" {
		fmt.Printf("  title:    %s\n", *row.Title)
	}
	fmt.Printf("  body:     %s\n", row.Body)
	switch row.Status {
	case "answered":
		if row.AnswerText != nil {
			fmt.Printf("  answer:   %s\n", *row.AnswerText)
		}
		if row.AnswerChoice != nil {
			fmt.Printf("  choice:   %s\n", *row.AnswerChoice)
		}
		if row.AnswerApproval != nil {
			fmt.Printf("  approved: %v\n", *row.AnswerApproval)
		}
		if row.AnsweredVia != nil {
			fmt.Printf("  via:      %s\n", *row.AnsweredVia)
		}
	case "pending":
		fmt.Printf("  reminders: %d\n", row.ReminderCount)
	}
	return nil
}

// ---- list ----

var userMessagingListCmd = &cobra.Command{
	Use:   "list",
	Short: "List messages for the current agent",
	RunE:  runUserMessagingList,
}

func runUserMessagingList(_ *cobra.Command, _ []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}
	u, _ := url.Parse(fmt.Sprintf("%s/api/cli/user-messaging/list", ctx.APIBaseURL))
	q := u.Query()
	if umListStatus != "" {
		q.Set("status", umListStatus)
	}
	if umListAgency != "" {
		q.Set("agency", umListAgency)
	}
	if umListLimit > 0 {
		q.Set("limit", strconv.Itoa(umListLimit))
	}
	if umListIncludeFixtures {
		q.Set("include_fixtures", "1")
	}
	if umListIncludeDisposed {
		q.Set("include_disposed", "1")
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequest(http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	ctx.SetAuthHeaders(req)
	req.Header.Set("X-Kindship-CLI-Version", Version)

	body, statusCode, err := doUserMessagingHTTP(req)
	if err != nil {
		return err
	}
	if statusCode >= 400 {
		return fmt.Errorf("list failed (%d): %s", statusCode, string(body))
	}
	var out api.UserMessagingListResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if umJSON {
		return printJSON(out)
	}
	if len(out.Items) == 0 {
		fmt.Println("(no messages)")
		return nil
	}
	for _, m := range out.Items {
		// Subject priority for the list view mirrors the server's
		// notification priority: tldr → title → body.
		subject := m.Body
		if m.Tldr != nil && *m.Tldr != "" {
			subject = *m.Tldr
		} else if m.Title != nil && *m.Title != "" {
			subject = *m.Title
		}
		if len(subject) > 60 {
			subject = subject[:57] + "..."
		}
		urgency := m.Urgency
		if urgency == "" {
			urgency = "daily" // server default; older rows may omit
		}
		// Plan requires one-line format with urgency + tldr + on_answer_note
		// inline. on-answer is appended to the same row when present so a
		// heartbeat scan sees the resume cue at a glance without a second line.
		line := fmt.Sprintf("%s  %-11s %-9s %-12s %-6s  %s", m.ID, m.Status, urgency, m.Agency, m.Type, subject)
		if m.OnAnswerNote != nil && *m.OnAnswerNote != "" {
			note := *m.OnAnswerNote
			if len(note) > 80 {
				note = note[:77] + "..."
			}
			line += fmt.Sprintf("  ⟵ on-answer: %s", note)
		}
		fmt.Println(line)
	}
	return nil
}

// ---- remind / retract ----

var userMessagingRemindCmd = &cobra.Command{
	Use:   "remind <id>",
	Short: "Push a fresh notification for a pending message",
	Args:  cobra.ExactArgs(1),
	RunE:  runUserMessagingRemind,
}

var userMessagingRetractCmd = &cobra.Command{
	Use:   "retract <id>",
	Short: "Retract a pending message (hidden from inbox, audit row remains)",
	Args:  cobra.ExactArgs(1),
	RunE:  runUserMessagingRetract,
}

func runUserMessagingRemind(_ *cobra.Command, args []string) error {
	return postUserMessagingIDAction("remind", args[0])
}

func runUserMessagingRetract(_ *cobra.Command, args []string) error {
	return postUserMessagingIDAction("retract", args[0])
}

func postUserMessagingIDAction(action, id string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{"id": id})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(
		http.MethodPost,
		fmt.Sprintf("%s/api/cli/user-messaging/%s", ctx.APIBaseURL, action),
		bytes.NewBuffer(body),
	)
	if err != nil {
		return err
	}
	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kindship-CLI-Version", Version)

	respBody, statusCode, err := doUserMessagingHTTP(req)
	if err != nil {
		return err
	}
	if statusCode >= 400 {
		return fmt.Errorf("%s failed (%d): %s", action, statusCode, string(respBody))
	}
	if umJSON {
		fmt.Println(string(respBody))
		return nil
	}
	fmt.Printf("✓ %s: %s\n", action, id)
	return nil
}

// ---- dispose ----

var userMessagingDisposeCmd = &cobra.Command{
	Use:   "dispose <id>",
	Short: "Mark an answered/acknowledged/expired message as reviewed (buries it from default scans)",
	Long: `Dispose marks a resolved user message as "reviewed and buried" so it
stops surfacing in default 'list' output on future heartbeats. Typical
uses: clearing a smoke-test fixture, burying an answered thread whose
reply has been acted on, tagging a row as handled-elsewhere.

Dispose is only valid for rows with status answered, acknowledged, or
expired. Pending rows use 'retract' instead; retracted rows are already
terminal. Re-disposing an already-disposed row is a no-op that returns
409 so double-dispose bugs are visible.

Use --include-disposed on 'list' to see buried rows.`,
	Args: cobra.ExactArgs(1),
	RunE: runUserMessagingDispose,
}

func runUserMessagingDispose(_ *cobra.Command, args []string) error {
	if umDisposeCode == "" {
		return fmt.Errorf("--code is required (e.g. smoke-test-fixture, obsolete, handled-elsewhere)")
	}
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}
	reqBody := api.UserMessagingDisposeRequest{
		ID:   args[0],
		Code: umDisposeCode,
		Note: umDisposeNote,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}
	req, err := http.NewRequest(
		http.MethodPost,
		fmt.Sprintf("%s/api/cli/user-messaging/dispose", ctx.APIBaseURL),
		bytes.NewBuffer(body),
	)
	if err != nil {
		return err
	}
	ctx.SetAuthHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Kindship-CLI-Version", Version)

	respBody, statusCode, err := doUserMessagingHTTP(req)
	if err != nil {
		return err
	}

	var out api.UserMessagingDisposeResponse
	// Parse JSON even on error paths — the server returns structured
	// error bodies for 404/409/500 that carry useful detail (status,
	// disposedAt) beyond the generic message.
	_ = json.Unmarshal(respBody, &out)

	if umJSON {
		// Keep stdout clean JSON. On error, signal failure via exit code
		// instead of returning to Cobra, which would add a non-JSON
		// "Error: ..." line on stderr.
		fmt.Println(string(respBody))
		if statusCode >= 400 {
			os.Exit(1)
		}
		return nil
	}

	if statusCode >= 400 {
		msg := out.Error
		if msg == "" {
			msg = strings.TrimSpace(string(respBody))
		}
		// Surface the structured detail fields the server returns on
		// 409s so operators see *which* row blocked them.
		if out.DisposedAt != nil && *out.DisposedAt != "" {
			msg = fmt.Sprintf("%s (already disposed at %s)", msg, *out.DisposedAt)
		}
		if out.Status != "" {
			msg = fmt.Sprintf("%s (current status: %s)", msg, out.Status)
		}
		return fmt.Errorf("dispose failed (%d): %s", statusCode, msg)
	}
	fmt.Printf("✓ disposed: %s (code=%s)\n", args[0], umDisposeCode)
	return nil
}

// ---- agencies ----

var userMessagingAgenciesCmd = &cobra.Command{
	Use:   "agencies",
	Short: "List your agencies and the user's oversight over each",
	Long: `List your 12 agencies (plus 'other') and the oversight level the user
has configured on each.

The agencies are YOUR domains of autonomy — they describe how you work,
not permissions the user granted. The user only configures oversight:
 - silent   : you have full authority here; the user does not want to be
              involved. The CLI refuses to deliver messages tagged with
              a silent agency.
 - report   : you can act but keep the user informed.
 - approval : ask before acting (conservative default for unset agencies).`,
	RunE: runUserMessagingAgencies,
}

func runUserMessagingAgencies(_ *cobra.Command, _ []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}
	req, err := http.NewRequest(
		http.MethodGet,
		fmt.Sprintf("%s/api/cli/user-messaging/agencies", ctx.APIBaseURL),
		nil,
	)
	if err != nil {
		return err
	}
	ctx.SetAuthHeaders(req)
	req.Header.Set("X-Kindship-CLI-Version", Version)

	body, statusCode, err := doUserMessagingHTTP(req)
	if err != nil {
		return err
	}
	if statusCode >= 400 {
		return fmt.Errorf("agencies failed (%d): %s", statusCode, string(body))
	}
	var out api.UserMessagingAgenciesResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("failed to parse response: %w", err)
	}

	if umJSON {
		return printJSON(out)
	}
	fmt.Printf("%-15s %-10s %s\n", "AGENCY", "OVERSIGHT", "NOTE")
	for _, a := range out.Items {
		note := ""
		if a.Fixed {
			note = "fixed"
		}
		marker := ""
		if a.Oversight == "silent" {
			marker = "  ← autonomous, no user involvement"
		}
		fmt.Printf("%-15s %-10s %s%s\n", a.Slug, a.Oversight, note, marker)
	}
	return nil
}

// ---- helpers ----

func doUserMessagingHTTP(req *http.Request) ([]byte, int, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("failed to read response: %w", err)
	}
	return body, resp.StatusCode, nil
}

// parseDurationSeconds accepts Go's time.ParseDuration format plus a 'd'
// (days) suffix. Returns the duration in integer seconds.
func parseDurationSeconds(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasSuffix(raw, "d") {
		n, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid days: %q", raw)
		}
		return n * 86400, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration must be positive")
	}
	return int(d.Seconds()), nil
}

func init() {
	// Shared flags
	for _, c := range []*cobra.Command{
		userMessagingAskCmd,
		userMessagingChoiceCmd,
		userMessagingApproveCmd,
		userMessagingReportCmd,
	} {
		c.Flags().StringVar(&umAgency, "agency", "", "One of your 12 agencies (or 'other') — the agent's domain of autonomy this message belongs to")
		c.Flags().StringVar(&umTitle, "title", "", "Optional short title")
		c.Flags().StringVar(&umExpiresIn, "expires-in", "", "Optional expiry (e.g. 30m, 2h, 7d) — default is no expiry")
		c.Flags().StringVar(&umUrgency, "urgency", "", "Urgency label: ambient | daily (default) | urgent | critical. Phase B: metadata + push subject only. Phase C: routing (bundled daily dispatch, quiet-hours hold, wake-override).")
		c.Flags().StringVar(&umTldr, "tldr", "", "Optional ≤140-char subject. Used as the push/email/telegram subject and the web card heading. Falls back to title, then body snippet.")
		c.Flags().StringVar(&umOnAnswer, "on-answer", "", "Optional self-reminder (≤2KB) surfaced alongside the answered row on your next heartbeat so you can resume context-free. Cue format suggestion: 'resume <ROADMAP item> / reread <file> / follow up with <…>'.")
		c.Flags().BoolVar(&umJSON, "json", false, "JSON output")
	}
	userMessagingChoiceCmd.Flags().StringArrayVar(&umChoiceOption, "option", nil, "--option value:label (repeatable; minimum 2)")

	userMessagingStatusCmd.Flags().BoolVar(&umJSON, "json", false, "JSON output")
	userMessagingListCmd.Flags().StringVar(&umListStatus, "status", "", "Filter by status (pending|answered|acknowledged|retracted|expired)")
	userMessagingListCmd.Flags().StringVar(&umListAgency, "agency", "", "Filter by agency")
	userMessagingListCmd.Flags().IntVar(&umListLimit, "limit", 50, "Max items")
	userMessagingListCmd.Flags().BoolVar(&umJSON, "json", false, "JSON output")
	userMessagingListCmd.Flags().BoolVar(&umListIncludeFixtures, "include-fixtures", false, "Include smoke-test/synthetic fixture rows (default: excluded)")
	userMessagingListCmd.Flags().BoolVar(&umListIncludeDisposed, "include-disposed", false, "Include rows previously disposed via 'dispose' (default: excluded)")
	userMessagingRemindCmd.Flags().BoolVar(&umJSON, "json", false, "JSON output")
	userMessagingRetractCmd.Flags().BoolVar(&umJSON, "json", false, "JSON output")
	userMessagingDisposeCmd.Flags().StringVar(&umDisposeCode, "code", "", "Machine label for the disposition (e.g. smoke-test-fixture, obsolete, handled-elsewhere) — required")
	userMessagingDisposeCmd.Flags().StringVar(&umDisposeNote, "note", "", "Optional human-readable reason")
	userMessagingDisposeCmd.Flags().BoolVar(&umJSON, "json", false, "JSON output")
	userMessagingAgenciesCmd.Flags().BoolVar(&umJSON, "json", false, "JSON output")

	userMessagingCmd.AddCommand(
		userMessagingAskCmd,
		userMessagingChoiceCmd,
		userMessagingApproveCmd,
		userMessagingReportCmd,
		userMessagingStatusCmd,
		userMessagingListCmd,
		userMessagingRemindCmd,
		userMessagingRetractCmd,
		userMessagingDisposeCmd,
		userMessagingAgenciesCmd,
	)
	rootCmd.AddCommand(userMessagingCmd)
}

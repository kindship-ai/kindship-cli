package cmd

import (
	"fmt"
	"os"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/kindship-ai/kindship-cli/internal/config"

	"github.com/spf13/cobra"
)

var attachCmd = &cobra.Command{
	Use:   "attach",
	Short: "Manage text attachments on planning entities",
	Long: `Read, write, and list text attachments on planning entities.

Attachments are persistent documents stored on the platform, visible to
the dashboard UI and Telegram agent.

Examples:
  kindship attach read ROADMAP.md
  kindship attach write ROADMAP.md --file ROADMAP.md
  kindship attach list
  kindship attach read feature-requests.txt --entity <id>`,
}

var (
	attachEntityID string
	attachFile     string
)

var attachReadCmd = &cobra.Command{
	Use:   "read <name>",
	Short: "Read an attachment's content to stdout",
	Args:  cobra.ExactArgs(1),
	RunE:  runAttachRead,
}

var attachWriteCmd = &cobra.Command{
	Use:   "write <name> --file <path>",
	Short: "Create or update a text attachment from a local file",
	Args:  cobra.ExactArgs(1),
	RunE:  runAttachWrite,
}

var attachListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all attachments for the current entity",
	Args:  cobra.NoArgs,
	RunE:  runAttachList,
}

func init() {
	attachCmd.PersistentFlags().StringVar(&attachEntityID, "entity", "", "Entity ID (defaults to active run's entity)")
	attachWriteCmd.Flags().StringVar(&attachFile, "file", "", "Local file to upload (required)")
	_ = attachWriteCmd.MarkFlagRequired("file")

	attachCmd.AddCommand(attachReadCmd)
	attachCmd.AddCommand(attachWriteCmd)
	attachCmd.AddCommand(attachListCmd)
	rootCmd.AddCommand(attachCmd)
}

func resolveEntityID(ctx *auth.Context) (string, error) {
	if attachEntityID != "" {
		return attachEntityID, nil
	}

	repoCfg, err := config.LoadRepoConfig()
	if err != nil {
		return "", fmt.Errorf("not in a configured repository (run 'kindship setup'): %w", err)
	}

	active, err := fetchActiveRun(ctx, repoCfg)
	if err != nil {
		return "", fmt.Errorf("failed to fetch active run: %w", err)
	}
	if active == nil {
		return "", fmt.Errorf("no active run — use --entity or start a dev loop session first")
	}

	return active.EntityID, nil
}

func runAttachRead(cmd *cobra.Command, args []string) error {
	name := args[0]

	ctx, err := auth.GetAuthContext()
	if err != nil {
		return fmt.Errorf("not authenticated: %w", err)
	}

	entityID, err := resolveEntityID(ctx)
	if err != nil {
		return err
	}

	client := api.NewClient(ctx.APIBaseURL, verbose)
	content, err := client.ReadAttachmentWithBearer(entityID, name, ctx.Token)
	if err != nil {
		return fmt.Errorf("failed to read attachment: %w", err)
	}

	fmt.Print(content)
	return nil
}

func runAttachWrite(cmd *cobra.Command, args []string) error {
	name := args[0]

	content, err := os.ReadFile(attachFile)
	if err != nil {
		return fmt.Errorf("failed to read file %s: %w", attachFile, err)
	}

	ctx, err := auth.GetAuthContext()
	if err != nil {
		return fmt.Errorf("not authenticated: %w", err)
	}

	entityID, err := resolveEntityID(ctx)
	if err != nil {
		return err
	}

	client := api.NewClient(ctx.APIBaseURL, verbose)
	if err := client.UpsertAttachmentWithBearer(entityID, name, string(content), ctx.Token); err != nil {
		return fmt.Errorf("failed to write attachment: %w", err)
	}

	fmt.Fprintf(os.Stderr, "✓ Attachment '%s' written (%d bytes)\n", name, len(content))
	return nil
}

func runAttachList(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return fmt.Errorf("not authenticated: %w", err)
	}

	entityID, err := resolveEntityID(ctx)
	if err != nil {
		return err
	}

	client := api.NewClient(ctx.APIBaseURL, verbose)
	attachments, err := client.ListAttachmentsWithBearer(entityID, ctx.Token)
	if err != nil {
		return fmt.Errorf("failed to list attachments: %w", err)
	}

	if len(attachments) == 0 {
		fmt.Println("No attachments found.")
		return nil
	}

	fmt.Printf("%-30s %-12s %s\n", "NAME", "TYPE", "CREATED")
	fmt.Printf("%-30s %-12s %s\n", "----", "----", "-------")
	for _, a := range attachments {
		fmt.Printf("%-30s %-12s %s\n", a.Name, a.Type, a.CreatedAt)
	}

	return nil
}

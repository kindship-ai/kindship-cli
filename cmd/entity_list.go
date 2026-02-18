package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kindship-ai/kindship-cli/internal/api"
	"github.com/kindship-ai/kindship-cli/internal/auth"
	"github.com/spf13/cobra"
)

var entityListCmd = &cobra.Command{
	Use:   "list",
	Short: "List planning entities",
	Long: `List planning entities with optional filters.

Examples:
  # List all entities for the current agent
  kindship entity list

  # Filter by type
  kindship entity list --type TASK

  # Filter by status
  kindship entity list --status ACTIVE

  # List children of a parent
  kindship entity list --parent 550e8400-...

  # Tree view of all entities
  kindship entity list --format tree

  # JSON output
  kindship entity list --format json`,
	RunE: runEntityList,
}

var (
	entityListType           string
	entityListStatus         string
	entityListParent         string
	entityListLimit          int
	entityListFormat         string
	entityListIncludeDeleted bool
)

func runEntityList(cmd *cobra.Command, args []string) error {
	ctx, err := auth.GetAuthContext()
	if err != nil {
		return err
	}

	agentID := ""
	if ctx.IsLocalMode() {
		agentID, err = ctx.RequireAgentID()
		if err != nil {
			return err
		}
	}

	client := api.NewClient(ctx.APIBaseURL, verbose)

	opts := api.ListEntitiesOpts{
		AgentID:        agentID,
		Type:           entityListType,
		Status:         entityListStatus,
		ParentID:       entityListParent,
		Limit:          entityListLimit,
		IncludeDeleted: entityListIncludeDeleted,
	}

	resp, err := client.ListEntities(ctx, opts)
	if err != nil {
		return fmt.Errorf("failed to list entities: %w", err)
	}

	if entityListFormat == "json" {
		return printJSON(resp)
	}

	if entityListFormat == "tree" {
		printEntityTree(resp.Entities)
		return nil
	}

	printEntityTable(resp.Entities)
	return nil
}

func printEntityTable(entities []api.PlanningEntity) {
	if len(entities) == 0 {
		fmt.Println("No entities found.")
		return
	}

	// Header
	fmt.Printf("%-18s %-12s %4s  %s\n", "TYPE", "STATUS", "SEQ", "TITLE")

	for _, e := range entities {
		fmt.Printf("%-18s %-12s %4d  %s\n", e.Type, e.Status, e.SequenceOrder, e.Title)
	}

	fmt.Printf("\n%d entities\n", len(entities))
}

func printEntityTree(entities []api.PlanningEntity) {
	if len(entities) == 0 {
		fmt.Println("No entities found.")
		return
	}

	// Build parent->children map
	children := make(map[string][]api.PlanningEntity)
	var roots []api.PlanningEntity

	for _, e := range entities {
		if e.ParentID == nil {
			roots = append(roots, e)
		} else {
			children[*e.ParentID] = append(children[*e.ParentID], e)
		}
	}

	// Sort children by sequence order
	for k := range children {
		sort.Slice(children[k], func(i, j int) bool {
			return children[k][i].SequenceOrder < children[k][j].SequenceOrder
		})
	}

	// Sort roots by sequence order
	sort.Slice(roots, func(i, j int) bool {
		return roots[i].SequenceOrder < roots[j].SequenceOrder
	})

	for _, root := range roots {
		printTreeNode(root, children, "", true)
	}
}

func printTreeNode(e api.PlanningEntity, children map[string][]api.PlanningEntity, prefix string, isLast bool) {
	connector := "+-"
	if prefix == "" {
		connector = ""
	} else if isLast {
		connector = "`-"
	}

	statusMarker := statusIcon(e.Status)
	fmt.Printf("%s%s%s: %s [%s] %s\n", prefix, connector, e.Type, e.Title, e.Status, statusMarker)

	childPrefix := prefix
	if prefix != "" {
		if isLast {
			childPrefix += "  "
		} else {
			childPrefix += "| "
		}
	}

	kids := children[e.ID]
	for i, child := range kids {
		printTreeNode(child, children, childPrefix, i == len(kids)-1)
	}
}

func statusIcon(status string) string {
	switch strings.ToUpper(status) {
	case "COMPLETED":
		return "[done]"
	case "ACTIVE":
		return "<-"
	case "FAILED":
		return "[FAIL]"
	default:
		return ""
	}
}

func init() {
	entityListCmd.Flags().StringVar(&entityListType, "type", "", "Filter by entity type (e.g. TASK, PROJECT, PROCESS)")
	entityListCmd.Flags().StringVar(&entityListStatus, "status", "", "Filter by status (e.g. ACTIVE, DRAFT, COMPLETED)")
	entityListCmd.Flags().StringVar(&entityListParent, "parent", "", "Filter by parent entity ID")
	entityListCmd.Flags().IntVar(&entityListLimit, "limit", 50, "Maximum number of entities to return")
	entityListCmd.Flags().StringVar(&entityListFormat, "format", "text", "Output format: text|json|tree")
	entityListCmd.Flags().BoolVar(&entityListIncludeDeleted, "include-deleted", false, "Include soft-deleted entities")
}

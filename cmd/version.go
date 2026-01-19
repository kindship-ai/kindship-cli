package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"
)

// Version is set at build time via ldflags
var Version = "dev"

// GitCommit is set at build time via ldflags
var GitCommit = "unknown"

// BuildDate is set at build time via ldflags
var BuildDate = "unknown"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Display version information",
	Long: `Display version information for the Kindship CLI.

Examples:
  kindship version
  kindship version --json`,
	RunE: runVersion,
}

var (
	versionJSON bool
)

func init() {
	versionCmd.Flags().BoolVar(&versionJSON, "json", false, "Output in JSON format")
	rootCmd.AddCommand(versionCmd)
}

type VersionOutput struct {
	Version   string `json:"version"`
	GitCommit string `json:"git_commit"`
	BuildDate string `json:"build_date"`
	GoVersion string `json:"go_version"`
	Platform  string `json:"platform"`
}

func runVersion(cmd *cobra.Command, args []string) error {
	output := VersionOutput{
		Version:   Version,
		GitCommit: GitCommit,
		BuildDate: BuildDate,
		GoVersion: runtime.Version(),
		Platform:  fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	}

	if versionJSON {
		return printJSON(output)
	}

	fmt.Printf("kindship version %s\n", Version)
	fmt.Printf("  Git commit: %s\n", GitCommit)
	fmt.Printf("  Build date: %s\n", BuildDate)
	fmt.Printf("  Go version: %s\n", runtime.Version())
	fmt.Printf("  Platform:   %s/%s\n", runtime.GOOS, runtime.GOARCH)

	return nil
}

// printJSON is a helper to output JSON to stdout
func printJSON(v interface{}) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(v)
}

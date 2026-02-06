package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"

	"github.com/spf13/cobra"
)

// Binary download URL base - proxied through kindship.ai
const binaryBaseURL = "https://kindship.ai/cli/kindship"

// getBinaryURL returns the platform-specific download URL
func getBinaryURL() string {
	os := runtime.GOOS
	arch := runtime.GOARCH
	return fmt.Sprintf("%s?os=%s&arch=%s", binaryBaseURL, os, arch)
}

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update kindship CLI to latest version",
	Long: `Download and install the latest version of the kindship CLI.

Example:
  kindship update`,
	Args: cobra.NoArgs,
	RunE: runUpdate,
}

func runUpdate(cmd *cobra.Command, args []string) error {
	// Get current executable path
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Get platform-specific download URL
	downloadURL := getBinaryURL()

	fmt.Printf("Downloading latest kindship...\n")
	fmt.Printf("Platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("URL: %s\n", downloadURL)

	// Download to temp file
	resp, err := http.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
	}

	// Show version info from headers
	if version := resp.Header.Get("X-Version"); version != "" {
		fmt.Printf("Downloading version: %s\n", version)
	}
	if platform := resp.Header.Get("X-Platform"); platform != "" {
		fmt.Printf("Confirmed platform: %s\n", platform)
	}

	// Create temp file
	tmpFile, err := os.CreateTemp("", "kindship-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // Clean up on failure

	// Copy downloaded content
	_, err = io.Copy(tmpFile, resp.Body)
	tmpFile.Close()
	if err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	// Make executable
	if err := os.Chmod(tmpPath, 0755); err != nil {
		return fmt.Errorf("failed to chmod: %w", err)
	}

	// Verify it runs
	verifyCmd := exec.Command(tmpPath, "--help")
	if err := verifyCmd.Run(); err != nil {
		return fmt.Errorf("downloaded binary failed verification: %w", err)
	}
	fmt.Println("Binary verified.")

	// Replace current binary
	fmt.Printf("Replacing %s...\n", execPath)
	if err := os.Rename(tmpPath, execPath); err != nil {
		// On some systems, rename across filesystems fails
		// Fall back to copy
		src, err := os.Open(tmpPath)
		if err != nil {
			return fmt.Errorf("failed to open temp file: %w", err)
		}
		defer src.Close()

		dst, err := os.OpenFile(execPath, os.O_WRONLY|os.O_TRUNC, 0755)
		if err != nil {
			return fmt.Errorf("failed to open destination: %w", err)
		}
		defer dst.Close()

		if _, err := io.Copy(dst, src); err != nil {
			return fmt.Errorf("failed to copy binary: %w", err)
		}
	}

	fmt.Println("Update complete!")
	return nil
}

func init() {
	rootCmd.AddCommand(updateCmd)
}

package cmd

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

	// Create temp file in the SAME directory as the running binary. On Linux
	// agent containers, /tmp is a tmpfs while /home/autonomous/.local/bin lives
	// on a separate volume — using the system temp dir made os.Rename fail with
	// EXDEV (cross-device link), which then fell back to an O_TRUNC write that
	// hit ETXTBSY ("text file busy") because the binary was currently
	// executing. Same-directory rename is atomic and tolerated even for a
	// running binary: the kernel swaps the inode pointer, the running process
	// keeps its old inode, and new invocations get the new binary.
	execDir := filepath.Dir(execPath)
	tmpFile, err := os.CreateTemp(execDir, ".kindship-update-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file in %s: %w", execDir, err)
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

	// Atomic swap: rename within the same directory. Removes the previous
	// dentry but the running process keeps its open inode; subsequent
	// invocations resolve the new binary.
	fmt.Printf("Replacing %s...\n", execPath)
	if err := os.Rename(tmpPath, execPath); err != nil {
		return fmt.Errorf("failed to replace binary at %s: %w", execPath, err)
	}

	fmt.Println("Update complete!")
	return nil
}

func init() {
	rootCmd.AddCommand(updateCmd)
}

// ABOUTME: Command to rebuild and reinstall gt binary from source.
// ABOUTME: Detects stale binaries and rebuilds with proper versioning.

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/version"
)

var (
	updateForce  bool
	updateDryRun bool
)

var updateCmd = &cobra.Command{
	Use:     "update",
	GroupID: GroupConfig,
	Short:   "Rebuild and reinstall gt from source",
	Long: `Rebuild and reinstall the gt binary from source.

This command rebuilds gt from your local repository and installs it
to the same location as your current binary (or ~/.local/bin/gt by default).

The rebuild includes version information (git tag, commit, build time) so
staleness checks work correctly after update.

Examples:
  gt update               # Rebuild and install
  gt update --dry-run     # Show what would be done
  gt update --force       # Skip confirmation prompts`,
	RunE: runUpdate,
}

func init() {
	updateCmd.Flags().BoolVarP(&updateForce, "force", "f", false, "Skip confirmation prompts")
	updateCmd.Flags().BoolVar(&updateDryRun, "dry-run", false, "Show what would be done without doing it")
	rootCmd.AddCommand(updateCmd)
}

func runUpdate(cmd *cobra.Command, args []string) error {
	// Find the gt source repository
	repoRoot, err := version.GetRepoRoot()
	if err != nil {
		return fmt.Errorf("cannot locate gt source repository: %w\n\nMake sure you have the gastown repository cloned and set GT_ROOT if needed", err)
	}

	fmt.Printf("%s Found gt repository at %s\n\n", style.Bold.Render("🔍"), style.Dim.Render(repoRoot))

	// Check if binary is stale
	info := version.CheckStaleBinary(repoRoot)
	if info.Error == nil && !info.IsStale {
		fmt.Printf("%s Binary is already up to date (%s)\n", style.Success.Render("✓"), version.ShortCommit(info.BinaryCommit))
		if !updateForce {
			fmt.Println("\nUse --force to rebuild anyway")
			return nil
		}
		fmt.Println()
	} else if info.Error == nil && info.IsStale {
		msg := fmt.Sprintf("Binary is stale (built from %s, repo at %s)",
			version.ShortCommit(info.BinaryCommit), version.ShortCommit(info.RepoCommit))
		if info.CommitsBehind > 0 {
			msg = fmt.Sprintf("Binary is %d commits behind (built from %s, repo at %s)",
				info.CommitsBehind, version.ShortCommit(info.BinaryCommit), version.ShortCommit(info.RepoCommit))
		}
		fmt.Printf("%s %s\n\n", style.Warning.Render("⚠"), msg)
	}

	// Determine install location
	installPath, err := determineInstallLocation()
	if err != nil {
		return fmt.Errorf("determining install location: %w", err)
	}

	fmt.Printf("Build configuration:\n")
	fmt.Printf("  Source:  %s\n", repoRoot)
	fmt.Printf("  Target:  %s\n", installPath)
	fmt.Printf("  Go:      %s\n", runtime.Version())

	// Check if go is available
	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf("go command not found: %w\n\nMake sure Go is installed and in your PATH", err)
	}

	if updateDryRun {
		fmt.Printf("\n%s Dry run mode - no changes will be made\n", style.Dim.Render("ℹ"))
		fmt.Println("\nWould execute:")
		fmt.Printf("  1. Run go generate in %s\n", repoRoot)
		fmt.Printf("  2. Build binary with version info\n")
		fmt.Printf("  3. Install to %s\n", installPath)
		if runtime.GOOS == "darwin" {
			fmt.Printf("  4. Codesign binary for macOS\n")
		}
		return nil
	}

	// Prompt for confirmation unless --force
	if !updateForce {
		fmt.Print("\nContinue with rebuild? [Y/n] ")
		var response string
		fmt.Scanln(&response)
		response = strings.ToLower(strings.TrimSpace(response))
		if response == "n" || response == "no" {
			fmt.Println("Aborted.")
			return nil
		}
	}

	fmt.Println()
	fmt.Println("Building gt...")

	// Get version info for ldflags
	versionInfo, err := getVersionInfo(repoRoot)
	if err != nil {
		return fmt.Errorf("getting version info: %w", err)
	}

	// Run go generate
	fmt.Printf("  • Running go generate...\n")
	genCmd := exec.Command("go", "generate", "./...")
	genCmd.Dir = repoRoot
	genCmd.Stderr = os.Stderr
	if err := genCmd.Run(); err != nil {
		return fmt.Errorf("go generate failed: %w", err)
	}

	// Build binary to temporary location first
	tmpBinary := filepath.Join(os.TempDir(), fmt.Sprintf("gt-build-%d", time.Now().Unix()))
	defer os.Remove(tmpBinary)

	fmt.Printf("  • Building binary...\n")
	ldflags := fmt.Sprintf("-X github.com/steveyegge/gastown/internal/cmd.Version=%s -X github.com/steveyegge/gastown/internal/cmd.Commit=%s -X github.com/steveyegge/gastown/internal/cmd.BuildTime=%s",
		versionInfo.Version, versionInfo.Commit, versionInfo.BuildTime)

	buildCmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", tmpBinary, "./cmd/gt")
	buildCmd.Dir = repoRoot
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("go build failed: %w", err)
	}

	// Verify the binary was created
	if _, err := os.Stat(tmpBinary); err != nil {
		return fmt.Errorf("build succeeded but binary not found at %s: %w", tmpBinary, err)
	}

	// Install to target location
	fmt.Printf("  • Installing to %s...\n", installPath)

	// Ensure target directory exists
	targetDir := filepath.Dir(installPath)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return fmt.Errorf("creating target directory: %w", err)
	}

	// Copy the binary (handles overwriting current binary)
	// copyFile handles permissions and atomic replacement
	if err := copyFile(tmpBinary, installPath); err != nil {
		return fmt.Errorf("installing binary: %w", err)
	}

	// On macOS, codesign the binary
	if runtime.GOOS == "darwin" {
		fmt.Printf("  • Codesigning for macOS...\n")
		signCmd := exec.Command("codesign", "-s", "-", "-f", installPath)
		// Ignore errors - codesigning is optional
		_ = signCmd.Run()
	}

	fmt.Printf("\n%s gt updated successfully!\n", style.Success.Render("✓"))
	fmt.Printf("\nInstalled: %s\n", installPath)
	fmt.Printf("Version:   %s\n", versionInfo.Version)
	fmt.Printf("Commit:    %s\n", version.ShortCommit(versionInfo.Commit))

	return nil
}

// versionInfo contains build version information.
type versionInfo struct {
	Version   string
	Commit    string
	BuildTime string
}

// getVersionInfo extracts version information from the git repository.
func getVersionInfo(repoDir string) (*versionInfo, error) {
	info := &versionInfo{
		BuildTime: time.Now().UTC().Format(time.RFC3339),
	}

	// Get version from git describe
	versionCmd := exec.Command("git", "describe", "--tags", "--always", "--dirty")
	versionCmd.Dir = repoDir
	versionOutput, err := versionCmd.Output()
	if err != nil {
		info.Version = "dev"
	} else {
		info.Version = strings.TrimSpace(string(versionOutput))
	}

	// Get commit hash
	commitCmd := exec.Command("git", "rev-parse", "HEAD")
	commitCmd.Dir = repoDir
	commitOutput, err := commitCmd.Output()
	if err != nil {
		return nil, fmt.Errorf("getting commit hash: %w", err)
	}
	info.Commit = strings.TrimSpace(string(commitOutput))

	return info, nil
}

// determineInstallLocation finds where to install the gt binary.
// Priority:
// 1. Location of current running binary
// 2. ~/.local/bin/gt (default)
func determineInstallLocation() (string, error) {
	// Try to find current binary location
	currentBinary, err := os.Executable()
	if err == nil {
		// Resolve symlinks
		if resolved, err := filepath.EvalSymlinks(currentBinary); err == nil {
			currentBinary = resolved
		}

		// Check if it's in a reasonable location (not in /tmp or go build cache)
		if !strings.Contains(currentBinary, "/tmp") && !strings.Contains(currentBinary, "go-build") {
			return currentBinary, nil
		}
	}

	// Default to ~/.local/bin/gt
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("getting home directory: %w", err)
	}

	return filepath.Join(home, ".local", "bin", "gt"), nil
}

// copyFile copies a file from src to dst, handling the case where dst might be
// the currently running binary (which causes "text file busy" errors).
// The workaround is to write to a temp file and then atomically rename it.
func copyFile(src, dst string) error {
	// Read source file
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("reading source: %w", err)
	}

	// Write to temp file in same directory (for atomic rename)
	tmpDst := dst + ".new"
	if err := os.WriteFile(tmpDst, data, 0755); err != nil {
		return fmt.Errorf("writing temp file: %w", err)
	}

	// Atomically rename (this works even if dst is a running executable)
	if err := os.Rename(tmpDst, dst); err != nil {
		os.Remove(tmpDst) // Clean up temp file
		return fmt.Errorf("renaming temp file: %w", err)
	}

	return nil
}

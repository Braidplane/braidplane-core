package repostore

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// GitMetadata captures source metadata from a checked-out git repository.
type GitMetadata struct {
	SourceRef  *string
	LastCommit *string
}

// ResolveGitMetadata reads best-effort source metadata from repoPath.
func ResolveGitMetadata(ctx context.Context, repoPath string) GitMetadata {
	sourceRef := runGitOutput(ctx, repoPath, "symbolic-ref", "--quiet", "HEAD")
	if sourceRef == nil {
		branch := runGitOutput(ctx, repoPath, "rev-parse", "--abbrev-ref", "HEAD")
		if branch != nil && *branch != "HEAD" {
			ref := "refs/heads/" + *branch
			sourceRef = &ref
		}
	}

	return GitMetadata{
		SourceRef:  sourceRef,
		LastCommit: runGitOutput(ctx, repoPath, "rev-parse", "HEAD"),
	}
}

func runGitOutput(ctx context.Context, repoPath string, args ...string) *string {
	commandArgs := []string{"-C", repoPath}
	commandArgs = append(commandArgs, args...)

	cmd := exec.CommandContext(ctx, "git", commandArgs...)
	cmd.Env = sanitizedGitEnv(os.Environ(), nil)

	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	value := strings.TrimSpace(string(output))
	if value == "" {
		return nil
	}

	return &value
}

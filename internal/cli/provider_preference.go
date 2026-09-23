package cli

import (
	"bytes"
	"errors"
	"os/exec"
	"strings"
)

const repoLocalProviderGitKey = "crabbox.provider"

func applyRepoLocalProviderPreference(cfg *Config) error {
	provider, ok, err := repoLocalProviderPreference()
	if err != nil {
		return err
	}
	if ok {
		setProviderSelection(cfg, provider, providerSelectionRepoLocal)
	}
	return nil
}

func repoLocalProviderPreference() (string, bool, error) {
	boundary, err := findRepositoryBoundary()
	if err != nil {
		return "", false, err
	}
	if boundary.kind != repositoryBoundaryGit || strings.TrimSpace(boundary.root) == "" {
		return "", false, nil
	}
	return repoLocalProviderPreferenceForBoundary(boundary)
}

func repoLocalProviderPreferenceForBoundary(boundary repositoryBoundary) (string, bool, error) {
	cmd := exec.Command("git", "config", "--local", "--no-includes", "--get", repoLocalProviderGitKey)
	cmd.Dir = boundary.root
	cmd.Env = repositoryGitEnvironment()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return "", false, nil
		}
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return "", false, Exit(2, "read repository-local provider preference: %s", detail)
		}
		return "", false, Exit(2, "read repository-local provider preference: %v", err)
	}
	value := strings.TrimSpace(stdout.String())
	if value == "" {
		return "", false, nil
	}
	provider, err := ProviderFor(value)
	if err != nil {
		return "", false, Exit(2, "repository-local provider preference %q is invalid; run 'crabbox config set-provider --clear' or choose another provider", value)
	}
	return provider.Spec().Name, true, nil
}

func setRepoLocalProviderPreference(value string) (string, string, error) {
	provider, err := ProviderFor(strings.TrimSpace(value))
	if err != nil {
		return "", "", err
	}
	boundary, err := providerPreferenceGitBoundary()
	if err != nil {
		return "", "", err
	}
	canonical := provider.Spec().Name
	cmd := exec.Command("git", "config", "--local", repoLocalProviderGitKey, canonical)
	cmd.Dir = boundary.root
	cmd.Env = repositoryGitEnvironment()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return "", "", Exit(2, "write repository-local provider preference: %s", detail)
		}
		return "", "", Exit(2, "write repository-local provider preference: %v", err)
	}
	return canonical, canonicalRepositoryPath(boundary.root), nil
}

func clearRepoLocalProviderPreference() (string, bool, error) {
	boundary, err := providerPreferenceGitBoundary()
	if err != nil {
		return "", false, err
	}
	_, ok, err := repoLocalProviderPreferenceForBoundary(boundary)
	if err != nil {
		return "", false, err
	}
	root := canonicalRepositoryPath(boundary.root)
	if !ok {
		return root, false, nil
	}
	cmd := exec.Command("git", "config", "--local", "--unset-all", repoLocalProviderGitKey)
	cmd.Dir = boundary.root
	cmd.Env = repositoryGitEnvironment()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return "", false, Exit(2, "clear repository-local provider preference: %s", detail)
		}
		return "", false, Exit(2, "clear repository-local provider preference: %v", err)
	}
	return root, true, nil
}

func providerPreferenceGitBoundary() (repositoryBoundary, error) {
	boundary, err := findRepositoryBoundary()
	if err != nil {
		return repositoryBoundary{}, err
	}
	if boundary.kind != repositoryBoundaryGit || strings.TrimSpace(boundary.root) == "" {
		return repositoryBoundary{}, Exit(2, "repository-local provider preferences require a Git repository; use provider in user config for a machine-wide default")
	}
	return boundary, nil
}

package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func initProviderPreferenceRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	cmd := exec.Command("git", "init", "-q", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	t.Chdir(root)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CRABBOX_CONFIG", "")
	t.Setenv("CRABBOX_PROVIDER", "")
	return root
}

func TestConfigSetProviderRemembersRepoLocalSelection(t *testing.T) {
	clearConfigEnv(t)
	root := initProviderPreferenceRepo(t)
	var stdout, stderr bytes.Buffer
	app := App{Stdout: &stdout, Stderr: &stderr}

	if err := app.Run(context.Background(), []string{"config", "set-provider", "boxd"}); err != nil {
		t.Fatalf("set-provider: %v stderr=%q", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "provider=boxd") || !strings.Contains(stdout.String(), "scope=repo") {
		t.Fatalf("unexpected set-provider output: %q", stdout.String())
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "boxd" || cfg.providerSelectionSource != providerSelectionRepoLocal || !providerSelectionIsActionable(cfg) {
		t.Fatalf("provider=%q source=%q", cfg.Provider, cfg.providerSelectionSource)
	}

	cmd := exec.Command("git", "config", "--local", "--no-includes", "--get", repoLocalProviderGitKey)
	cmd.Dir = root
	got, err := cmd.Output()
	if err != nil || strings.TrimSpace(string(got)) != "boxd" {
		t.Fatalf("git config provider=%q err=%v", got, err)
	}
}

func TestRepoLocalProviderSelectionPrecedence(t *testing.T) {
	clearConfigEnv(t)
	root := initProviderPreferenceRepo(t)
	if _, _, err := setRepoLocalProviderPreference("boxd"); err != nil {
		t.Fatal(err)
	}

	userPath := userConfigPath()
	if err := os.MkdirAll(filepath.Dir(userPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(userPath, []byte("provider: hetzner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "boxd" || cfg.providerSelectionSource != providerSelectionRepoLocal {
		t.Fatalf("repo-local should override user config: provider=%q source=%q", cfg.Provider, cfg.providerSelectionSource)
	}

	if err := os.WriteFile(filepath.Join(root, "crabbox.yaml"), []byte("provider: aws\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "aws" || cfg.providerSelectionSource != providerSelectionRepoConfig {
		t.Fatalf("repo config should override repo-local preference: provider=%q source=%q", cfg.Provider, cfg.providerSelectionSource)
	}

	t.Setenv("CRABBOX_PROVIDER", "daytona")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "daytona" || cfg.providerSelectionSource != providerSelectionEnvironment {
		t.Fatalf("environment should override repo config: provider=%q source=%q", cfg.Provider, cfg.providerSelectionSource)
	}
}

func TestExplicitConfigBypassesRepoLocalProviderPreference(t *testing.T) {
	clearConfigEnv(t)
	initProviderPreferenceRepo(t)
	if _, _, err := setRepoLocalProviderPreference("boxd"); err != nil {
		t.Fatal(err)
	}
	explicit := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(explicit, []byte("provider: hetzner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CRABBOX_CONFIG", explicit)

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Provider != "hetzner" || cfg.providerSelectionSource != providerSelectionUserConfig {
		t.Fatalf("explicit config should isolate selection: provider=%q source=%q", cfg.Provider, cfg.providerSelectionSource)
	}
}

func TestConfigSetProviderClear(t *testing.T) {
	clearConfigEnv(t)
	initProviderPreferenceRepo(t)
	var stdout, stderr bytes.Buffer
	app := App{Stdout: &stdout, Stderr: &stderr}
	if err := app.configSetProvider([]string{"boxd"}); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if err := app.configSetProvider([]string{"--clear"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "cleared provider preference") {
		t.Fatalf("unexpected clear output: %q", stdout.String())
	}
	if _, ok, err := repoLocalProviderPreference(); err != nil || ok {
		t.Fatalf("preference still present: ok=%t err=%v", ok, err)
	}
}

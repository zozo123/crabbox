package cli

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestNormalizeCrewName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"alpha", "alpha"},
		{"  Alpha  ", "alpha"},
		{"alpha-bravo", "alpha-bravo"},
		{"Alpha_Bravo Charlie", "alpha-bravo-charlie"},
		{"---alpha---", "alpha"},
		{"alpha//bravo??", "alpha-bravo"},
		{"pond!!", "pond"},
		{"123-abc", "123-abc"},
	}
	for _, tc := range cases {
		if got := normalizeCrewName(tc.in); got != tc.want {
			t.Fatalf("normalizeCrewName(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestRequestedCrewNameValidates(t *testing.T) {
	got, err := requestedCrewName(" Alpha Pond ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "alpha-pond" {
		t.Fatalf("pond=%q want alpha-pond", got)
	}
	if got, err := requestedCrewName(""); err != nil || got != "" {
		t.Fatalf("empty input got=%q err=%v", got, err)
	}
	if _, err := requestedCrewName("!!"); err == nil {
		t.Fatalf("expected error for pond with no alnum")
	}
	tooLong := strings.Repeat("a", maxRequestedCrewNameLength+1)
	if _, err := requestedCrewName(tooLong); err == nil {
		t.Fatalf("expected error for pond %d chars", maxRequestedCrewNameLength+1)
	}
}

func TestDirectLeaseLabelsRecordCrew(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	cfg := Config{
		Class:       "standard",
		Profile:     "default",
		ProviderKey: "crabbox-cbx-abcdef123456",
		ServerType:  "cpx62",
		Pond:        "alpha",
		TTL:         15 * time.Minute,
		IdleTimeout: 4 * time.Minute,
	}
	labels := directLeaseLabels(cfg, "cbx_abcdef123456", "blue-lobster", "hetzner", "", true, now)
	if labels["pond"] != "alpha" {
		t.Fatalf("pond label=%q want alpha; full=%#v", labels["pond"], labels)
	}
}

func TestDirectLeaseLabelsOmitCrewWhenEmpty(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	cfg := Config{
		Class:       "standard",
		Profile:     "default",
		ProviderKey: "crabbox-cbx-abcdef123456",
		ServerType:  "cpx62",
		TTL:         15 * time.Minute,
		IdleTimeout: 4 * time.Minute,
	}
	labels := directLeaseLabels(cfg, "cbx_abcdef123456", "blue-lobster", "hetzner", "", true, now)
	if _, ok := labels["pond"]; ok {
		t.Fatalf("pond label should be omitted when cfg.Pond is empty; got %#v", labels)
	}
}

func TestFilterServersByCrew(t *testing.T) {
	servers := []Server{
		{Name: "a", Labels: map[string]string{"pond": "alpha"}},
		{Name: "b", Labels: map[string]string{"pond": "bravo"}},
		{Name: "c", Labels: map[string]string{}},
		{Name: "d", Labels: map[string]string{"pond": "Alpha"}},
	}
	got := filterServersByCrew(servers, "alpha")
	if len(got) != 2 {
		t.Fatalf("expected 2 servers for pond=alpha, got %d (%#v)", len(got), got)
	}
	if got[0].Name != "a" || got[1].Name != "d" {
		t.Fatalf("filtered set unexpected: %#v", got)
	}
	if same := filterServersByCrew(servers, ""); len(same) != len(servers) {
		t.Fatalf("empty filter should be a no-op, got %d", len(same))
	}
	if none := filterServersByCrew(servers, "charlie"); len(none) != 0 {
		t.Fatalf("expected zero matches for pond=charlie, got %d", len(none))
	}
}

func TestApplyLeaseCreateFlagsSetsCrew(t *testing.T) {
	defaults := Config{
		Provider:    "hetzner",
		Profile:     "default",
		Class:       "standard",
		TargetOS:    targetLinux,
		TTL:         time.Hour,
		IdleTimeout: 15 * time.Minute,
		Network:     NetworkAuto,
		Capacity:    CapacityConfig{Market: "spot"},
	}
	fs := flag.NewFlagSet("warmup", flag.ContinueOnError)
	values := registerLeaseCreateFlags(fs, defaults)
	if err := fs.Parse([]string{"--pond", "Alpha Pond"}); err != nil {
		t.Fatal(err)
	}
	cfg := defaults
	if err := applyLeaseCreateFlags(&cfg, fs, values); err != nil {
		t.Fatalf("applyLeaseCreateFlags: %v", err)
	}
	if cfg.Pond != "alpha-pond" {
		t.Fatalf("cfg.Pond=%q want alpha-pond", cfg.Pond)
	}
	opts := leaseOptionsFromConfig(cfg)
	if opts.Pond != "alpha-pond" {
		t.Fatalf("leaseOptionsFromConfig.Pond=%q want alpha-pond", opts.Pond)
	}
}

func TestApplyLeaseCreateFlagsRejectsBadCrew(t *testing.T) {
	defaults := Config{
		Provider:    "hetzner",
		Profile:     "default",
		Class:       "standard",
		TargetOS:    targetLinux,
		TTL:         time.Hour,
		IdleTimeout: 15 * time.Minute,
		Network:     NetworkAuto,
		Capacity:    CapacityConfig{Market: "spot"},
	}
	fs := flag.NewFlagSet("warmup", flag.ContinueOnError)
	values := registerLeaseCreateFlags(fs, defaults)
	if err := fs.Parse([]string{"--pond", "!!"}); err != nil {
		t.Fatal(err)
	}
	cfg := defaults
	if err := applyLeaseCreateFlags(&cfg, fs, values); err == nil {
		t.Fatalf("expected error for invalid --pond")
	}
}

func TestFilterJSONListViewByCrew(t *testing.T) {
	view := []any{
		map[string]any{"id": "a", "labels": map[string]any{"pond": "alpha"}},
		map[string]any{"id": "b", "labels": map[string]any{"pond": "bravo"}},
		map[string]any{"id": "c", "labels": map[string]any{}},
		map[string]any{"id": "d", "labels": map[string]any{"pond": "Alpha"}},
		"not-a-map",
	}
	filtered := filterJSONListViewByCrew(view, "alpha")
	out, ok := filtered.([]any)
	if !ok {
		t.Fatalf("expected []any, got %T", filtered)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 entries, got %d (%#v)", len(out), out)
	}
	if same := filterJSONListViewByCrew(view, ""); !sameAny(same, view) {
		t.Fatalf("empty filter should be identity")
	}
	if other := filterJSONListViewByCrew("not-a-list", "alpha"); other != "not-a-list" {
		t.Fatalf("non-slice view should pass through unchanged, got %#v", other)
	}
	unsupported := []any{
		map[string]any{"id": "native-a", "state": "ready"},
		map[string]any{"id": "native-b", "state": "leased"},
	}
	if same := filterJSONListViewByCrew(unsupported, "alpha"); fmt.Sprintf("%#v", same) != fmt.Sprintf("%#v", unsupported) {
		t.Fatalf("unlabeled JSON list should pass through unchanged, got %#v", same)
	}
}

func sameAny(a, b any) bool {
	as, aok := a.([]any)
	bs, bok := b.([]any)
	if !aok || !bok || len(as) != len(bs) {
		return false
	}
	for i := range as {
		if _, ok := as[i].(map[string]any); ok {
			continue
		}
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

func TestCrewTagOwnerTruncatesAndNormalizes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"yossi.eliaz@incredibuild.com", "yossi-e"},
		{"  Alpha.Bravo@example.com  ", "alpha-b"},
		{"a@b", "a"},
		{"!!", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := crewTagOwner(tc.in); got != tc.want {
			t.Fatalf("crewTagOwner(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestCrewTailscaleTagShape(t *testing.T) {
	if got := crewTailscaleTag("yossi.eliaz@incredibuild.com", "Alpha Pond"); got != "tag:cbx-pond-yossi-e-alpha-pond" {
		t.Fatalf("crewTailscaleTag=%q want tag:cbx-pond-yossi-e-alpha-pond", got)
	}
	if got := crewTailscaleTag("", "alpha"); got != "tag:cbx-pond-user-alpha" {
		t.Fatalf("crewTailscaleTag empty owner=%q want tag:cbx-pond-user-alpha", got)
	}
	if got := crewTailscaleTag("yossi", ""); got != "" {
		t.Fatalf("crewTailscaleTag empty pond=%q want empty", got)
	}
}

func TestAppendCrewTailscaleTag(t *testing.T) {
	cfg := Config{
		Pond: "alpha",
		Tailscale: TailscaleConfig{
			Enabled: true,
			Tags:    []string{"tag:crabbox"},
		},
	}
	appendCrewTailscaleTag(&cfg, true)
	found := false
	for _, tag := range cfg.Tailscale.Tags {
		if strings.HasPrefix(tag, "tag:cbx-pond-") && strings.HasSuffix(tag, "-alpha") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected pond tag appended, got %#v", cfg.Tailscale.Tags)
	}
	// Idempotent: a second call must not duplicate the entry.
	before := len(cfg.Tailscale.Tags)
	appendCrewTailscaleTag(&cfg, true)
	if len(cfg.Tailscale.Tags) != before {
		t.Fatalf("appendCrewTailscaleTag duplicated entry; got %#v", cfg.Tailscale.Tags)
	}
	// No-op when Tailscale is not enabled.
	cfg2 := Config{Pond: "alpha", Tailscale: TailscaleConfig{Tags: []string{"tag:crabbox"}}}
	appendCrewTailscaleTag(&cfg2, true)
	if len(cfg2.Tailscale.Tags) != 1 {
		t.Fatalf("expected no-op when Tailscale disabled, got %#v", cfg2.Tailscale.Tags)
	}
	// No-op when the provider does not advertise FeatureTailscale.
	cfg3 := Config{Pond: "alpha", Tailscale: TailscaleConfig{Enabled: true, Tags: []string{"tag:crabbox"}}}
	appendCrewTailscaleTag(&cfg3, false)
	if len(cfg3.Tailscale.Tags) != 1 {
		t.Fatalf("expected no-op when provider lacks FeatureTailscale, got %#v", cfg3.Tailscale.Tags)
	}
}

func TestApplyLeaseCreateFlagsAddsCrewTailscaleTag(t *testing.T) {
	defaults := Config{
		Provider:    "hetzner",
		Profile:     "default",
		Class:       "standard",
		TargetOS:    targetLinux,
		TTL:         time.Hour,
		IdleTimeout: 15 * time.Minute,
		Network:     NetworkAuto,
		Capacity:    CapacityConfig{Market: "spot"},
		Tailscale: TailscaleConfig{
			HostnameTemplate: "crabbox-{slug}",
			Tags:             []string{"tag:crabbox"},
		},
	}
	fs := flag.NewFlagSet("warmup", flag.ContinueOnError)
	values := registerLeaseCreateFlags(fs, defaults)
	if err := fs.Parse([]string{"--pond", "alpha", "--tailscale"}); err != nil {
		t.Fatal(err)
	}
	cfg := defaults
	if err := applyLeaseCreateFlags(&cfg, fs, values); err != nil {
		t.Fatalf("applyLeaseCreateFlags: %v", err)
	}
	if !cfg.Tailscale.Enabled {
		t.Fatalf("expected Tailscale enabled by --tailscale")
	}
	hasCrewTag := false
	for _, tag := range cfg.Tailscale.Tags {
		if strings.HasPrefix(tag, "tag:cbx-pond-") && strings.HasSuffix(tag, "-alpha") {
			hasCrewTag = true
		}
	}
	if !hasCrewTag {
		t.Fatalf("expected pond Tailscale tag on cfg, got %#v", cfg.Tailscale.Tags)
	}
}

func TestCloudInitCrewHostsBootstrapEmittedWhenCrewAndTailscale(t *testing.T) {
	cfg := baseConfig()
	cfg.SSHUser = "runner"
	cfg.Pond = "alpha"
	cfg.Tailscale.Enabled = true
	cfg.Tailscale.AuthKey = "tskey-secret"
	cfg.Tailscale.Tags = []string{"tag:crabbox", "tag:cbx-pond-user-alpha"}
	got := cloudInit(cfg, "ssh-ed25519 test")
	for _, want := range []string{
		"/etc/hosts.cbx",
		"/usr/local/bin/crabbox-pond-hosts",
		"/etc/systemd/system/crabbox-pond-hosts.service",
		"/etc/systemd/system/crabbox-pond-hosts.timer",
		"/etc/hosts",
		"# crabbox pond hosts begin",
		"# crabbox pond hosts end",
		"OnUnitActiveSec=30s",
		"ExecStart=/usr/local/bin/crabbox-pond-hosts",
		"tag:cbx-pond-",
		"tailscale status --json",
		"systemctl enable --now crabbox-pond-hosts.timer",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("cloudInit(pond) missing %q", want)
		}
	}
}

func TestCloudInitCrewHostsBootstrapAbsentWithoutCrew(t *testing.T) {
	cfg := baseConfig()
	cfg.Tailscale.Enabled = true
	cfg.Tailscale.AuthKey = "tskey-secret"
	got := cloudInit(cfg, "ssh-ed25519 test")
	if strings.Contains(got, "crabbox-pond-hosts") {
		t.Fatalf("cloudInit without --pond must not install pond hosts service")
	}
}

func TestCloudInitCrewHostsBootstrapAbsentWithoutTailscale(t *testing.T) {
	cfg := baseConfig()
	cfg.Pond = "alpha"
	got := cloudInit(cfg, "ssh-ed25519 test")
	if strings.Contains(got, "crabbox-pond-hosts") {
		t.Fatalf("cloudInit without --tailscale must not install pond hosts service even when --pond is set")
	}
}

// stubDoctorTailscaleACLClient lets unit tests exercise doctorCrewSummary
// without touching the real Tailscale API.
type stubDoctorTailscaleACLClient struct {
	policy string
	err    error
}

func (s stubDoctorTailscaleACLClient) PolicyHuJSON(_ context.Context, _ string) (string, error) {
	return s.policy, s.err
}

func TestDoctorCrewSummaryNoopsWithoutCrew(t *testing.T) {
	cfg := Config{Provider: "hetzner"}
	status, message, details := doctorCrewSummary(context.Background(), cfg)
	if status != "" || message != "" || details != nil {
		t.Fatalf("expected no pond check without cfg.Pond, got status=%q msg=%q details=%#v", status, message, details)
	}
}

func TestDoctorCrewSummarySkipsNonTailscaleProviderWithCrew(t *testing.T) {
	cfg := Config{Provider: "e2b", Pond: "alpha"}
	status, message, details := doctorCrewSummary(context.Background(), cfg)
	if status != "skip" {
		t.Fatalf("expected skip, got %q", status)
	}
	want := `pond "alpha": provider e2b does not support the Tailscale plane; pond networking unavailable`
	if message != want {
		t.Fatalf("verdict text drifted\n want: %q\n got:  %q", want, message)
	}
	if details["reason"] != "provider_not_tailscale_capable" {
		t.Fatalf("expected reason metadata, got %#v", details)
	}
}

func TestDoctorCrewSummarySkipsWhenAPIKeyMissing(t *testing.T) {
	t.Setenv("TS_API_KEY", "")
	cfg := Config{Provider: "hetzner", Pond: "alpha"}
	status, _, details := doctorCrewSummary(context.Background(), cfg)
	if status != "skip" {
		t.Fatalf("expected skip when TS_API_KEY is missing, got %q", status)
	}
	if details["reason"] != "ts_api_key_missing" {
		t.Fatalf("expected ts_api_key_missing reason, got %#v", details)
	}
}

func TestDoctorCrewSummaryOKWhenACLPresent(t *testing.T) {
	t.Setenv("TS_API_KEY", "tskey-api-stub")
	tag := crewTailscaleTag(localCoordinatorOwner(), "alpha")
	policy := crewPolicyFixture(tag)
	prev := doctorTailscaleACLClientFactory
	defer func() { doctorTailscaleACLClientFactory = prev }()
	doctorTailscaleACLClientFactory = func(_ string) doctorTailscaleACLClient {
		return stubDoctorTailscaleACLClient{policy: policy}
	}
	cfg := Config{Provider: "hetzner", Pond: "alpha"}
	status, message, details := doctorCrewSummary(context.Background(), cfg)
	if status != "ok" {
		t.Fatalf("expected ok, got %q (msg=%q details=%#v)", status, message, details)
	}
	wantTag := crewTailscaleTag(localCoordinatorOwner(), "alpha")
	want := fmt.Sprintf(`pond "alpha": Tailscale plane auto-managed (%s)`, wantTag)
	if message != want {
		t.Fatalf("verdict text drifted\n want: %q\n got:  %q", want, message)
	}
	if details["mode"] != "auto-managed" {
		t.Fatalf("expected mode=auto-managed when TS_API_KEY is set, got %#v", details)
	}
}

func TestDoctorCrewSummaryFailsWhenACLMissing(t *testing.T) {
	t.Setenv("TS_API_KEY", "tskey-api-stub")
	policy := crewPolicyFixture("tag:cbx-pond-user-bravo")
	prev := doctorTailscaleACLClientFactory
	defer func() { doctorTailscaleACLClientFactory = prev }()
	doctorTailscaleACLClientFactory = func(_ string) doctorTailscaleACLClient {
		return stubDoctorTailscaleACLClient{policy: policy}
	}
	cfg := Config{Provider: "hetzner", Pond: "alpha"}
	status, message, details := doctorCrewSummary(context.Background(), cfg)
	if status != "failed" {
		t.Fatalf("expected failed, got %q", status)
	}
	wantTag := crewTailscaleTag(localCoordinatorOwner(), "alpha")
	want := fmt.Sprintf(`pond "alpha": tailnet policy row missing for %s. Run with $TS_API_KEY exported to auto-install, or apply the snippet from docs/features/pond.md`, wantTag)
	if message != want {
		t.Fatalf("verdict text drifted\n want: %q\n got:  %q", want, message)
	}
	if details["remedy"] == "" {
		t.Fatalf("expected remedy in details, got %#v", details)
	}
}

func TestDoctorCrewSummaryFailsWhenAPIClientErrors(t *testing.T) {
	t.Setenv("TS_API_KEY", "tskey-api-stub")
	prev := doctorTailscaleACLClientFactory
	defer func() { doctorTailscaleACLClientFactory = prev }()
	doctorTailscaleACLClientFactory = func(_ string) doctorTailscaleACLClient {
		return stubDoctorTailscaleACLClient{err: fmt.Errorf("tailscale api 401: invalid api key")}
	}
	cfg := Config{Provider: "hetzner", Pond: "alpha"}
	status, message, _ := doctorCrewSummary(context.Background(), cfg)
	if status != "failed" {
		t.Fatalf("expected failed, got %q", status)
	}
	if !strings.Contains(message, "policy lookup failed") {
		t.Fatalf("expected policy lookup failure message, got %q", message)
	}
}

// TestDoctorCrewSummarySkipsWhenControlPlaneIncompatible covers self-hosted
// control planes (e.g. Headscale) whose policy endpoint is not byte-compatible
// with Tailscale's /api/v2/tailnet/.../acl shape. Doctor must skip with a
// helpful pointer at the manual snippet rather than reporting a failure.
func TestDoctorCrewSummarySkipsWhenControlPlaneIncompatible(t *testing.T) {
	t.Setenv("TS_API_KEY", "tskey-api-stub")
	t.Setenv("CRABBOX_TS_API_URL", "https://headscale.example.com")
	t.Setenv("TS_API_URL", "")
	prev := doctorTailscaleACLClientFactory
	defer func() { doctorTailscaleACLClientFactory = prev }()
	doctorTailscaleACLClientFactory = func(_ string) doctorTailscaleACLClient {
		return stubDoctorTailscaleACLClient{err: fmt.Errorf("%w: GET https://headscale.example.com/api/v2/tailnet/-/acl returned 404", ErrCrewACLAutoBootstrapUnavailable)}
	}
	cfg := Config{Provider: "hetzner", Pond: "alpha"}
	status, message, details := doctorCrewSummary(context.Background(), cfg)
	if status != "skip" {
		t.Fatalf("expected skip on incompatible control plane, got %q (msg=%q)", status, message)
	}
	if !strings.Contains(message, "https://headscale.example.com") {
		t.Fatalf("expected control plane URL in message, got %q", message)
	}
	if !strings.Contains(message, "docs/features/pond.md") {
		t.Fatalf("expected manual snippet pointer, got %q", message)
	}
	if details["reason"] != "control_plane_incompatible" {
		t.Fatalf("expected control_plane_incompatible reason, got %#v", details)
	}
	if details["api_url"] != "https://headscale.example.com" {
		t.Fatalf("expected api_url metadata, got %#v", details)
	}
}

func TestCrewACLRowPresentChecksConcreteTag(t *testing.T) {
	tag := "tag:cbx-pond-yossi-e-alpha"
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"both present", crewPolicyFixture(tag), true},
		{"grants present", crewGrantPolicyFixture(tag), true},
		{"commented sample before grants", `{
  // "tagOwners": { "tag:cbx-pond-yossi-e-alpha": ["autogroup:admin"] },
  "tagOwners": { "tag:cbx-pond-yossi-e-alpha": ["autogroup:admin"] },
  "grants": [{ "src": ["tag:cbx-pond-yossi-e-alpha"], "dst": ["tag:cbx-pond-yossi-e-alpha"], "ip": ["*"] }]
}`, true},
		{"different pond", crewPolicyFixture("tag:cbx-pond-yossi-e-bravo"), false},
		{"missing tagOwners", `{"acls":[{"src":["tag:cbx-pond-yossi-e-alpha"],"dst":["tag:cbx-pond-yossi-e-alpha:*"]}]}`, false},
		{"missing dst", `{"tagOwners":{"tag:cbx-pond-yossi-e-alpha":["autogroup:admin"]},"acls":[{"src":["tag:cbx-pond-yossi-e-alpha"],"dst":["tag:crabbox:*"]}]}`, false},
		{"grant src only", `{"tagOwners":{"tag:cbx-pond-yossi-e-alpha":["autogroup:admin"]},"grants":[{"src":["tag:cbx-pond-yossi-e-alpha"],"dst":["tag:crabbox"],"ip":["*"]}]}`, false},
		{"missing tag mention", `{"acls":[{"src":["*"]}],"tagOwners":{"tag:crabbox":["autogroup:admin"]}}`, false},
		{"empty body", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := crewACLRowPresent(tc.body, tag); got != tc.want {
				t.Fatalf("crewACLRowPresent(%q)=%v want %v", tc.name, got, tc.want)
			}
		})
	}
}

func crewPolicyFixture(tag string) string {
	return fmt.Sprintf(`{
  "tagOwners": { %q: ["autogroup:admin"] },
  "acls": [{ "action": "accept", "src": [%q], "dst": [%q] }]
}`, tag, tag, tag+":*")
}

func crewGrantPolicyFixture(tag string) string {
	return fmt.Sprintf(`{
  "tagOwners": { %q: ["autogroup:admin"] },
  "grants": [{ "src": [%q], "dst": [%q], "ip": ["*"] }]
}`, tag, tag, tag)
}

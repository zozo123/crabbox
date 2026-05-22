package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// doctorPondTimeout bounds the live Tailscale API call. Kept short so the
// doctor command stays responsive even when the user's tailnet is degraded.
const doctorPondTimeout = 4 * time.Second

// doctorTailscaleACLClient is satisfied by anything that can return the raw
// policy document for the user's tailnet. The real implementation hits the
// Tailscale API; tests inject a stub so unit tests never reach the network.
type doctorTailscaleACLClient interface {
	PolicyHuJSON(ctx context.Context, tailnet string) (string, error)
}

// doctorTailscaleACLClientFactory is overridden in tests. It returns nil when
// the live client is not available so the check can degrade gracefully.
var doctorTailscaleACLClientFactory = newDoctorTailscaleACLClient

// doctorPondSummary is the doctor entry point invoked from finish(). It
// always returns either ("","",nil) — meaning "no pond check applies" — or a
// triplet ready to feed into the existing record(...) helper. The check is
// intentionally bounded to a few seconds so doctor stays fast even on
// degraded tailnets.
func doctorPondSummary(ctx context.Context, cfg Config) (string, string, map[string]string) {
	pond := normalizePondName(cfg.Pond)
	if pond == "" {
		return "", "", nil
	}
	tag := pondTailscaleTag(localCoordinatorOwner(), pond)
	if tag == "" {
		return "", "", nil
	}
	if !providerCapableOfTailscale(cfg.Provider) {
		return "skip", fmt.Sprintf("pond %q: provider %s does not support the Tailscale plane; pond networking unavailable", pond, cfg.Provider), map[string]string{"provider": cfg.Provider, "pond": pond, "tag": tag, "plane": "tailscale", "reason": "provider_not_tailscale_capable"}
	}
	apiKey := strings.TrimSpace(os.Getenv("TS_API_KEY"))
	if apiKey == "" {
		return "skip", "TS_API_KEY missing; skipped ACL verification", map[string]string{"pond": pond, "tag": tag, "reason": "ts_api_key_missing"}
	}
	tailnet := strings.TrimSpace(os.Getenv("TS_TAILNET"))
	if tailnet == "" {
		tailnet = "-"
	}
	client := doctorTailscaleACLClientFactory(apiKey)
	if client == nil {
		return "skip", "tailscale api client unavailable", map[string]string{"pond": pond, "tag": tag, "reason": "client_unavailable"}
	}
	checkCtx, cancel := context.WithTimeout(ctx, doctorPondTimeout)
	defer cancel()
	body, err := client.PolicyHuJSON(checkCtx, tailnet)
	if err != nil {
		// Self-hosted control planes (e.g. Headscale) do not expose the
		// Tailscale `/api/v2/tailnet/.../acl` route. Skip with a helpful
		// message pointing operators at the manual snippet rather than
		// failing — the network plane still works against the client-side
		// Tailscale binary regardless of which control plane runs it.
		if errors.Is(err, ErrPondACLAutoBootstrapUnavailable) {
			return "skip", fmt.Sprintf("pond %q: control plane at %s does not expose a Tailscale-compatible policy API; apply the snippet from docs/features/pond.md (e.g. Headscale: `headscale policy set --file ./policy.hujson`)", pond, resolveTailnetAPIURL()), map[string]string{"pond": pond, "tag": tag, "tailnet": tailnet, "api_url": resolveTailnetAPIURL(), "reason": "control_plane_incompatible"}
		}
		return "failed", fmt.Sprintf("tailscale policy lookup failed: %v", err), map[string]string{"pond": pond, "tag": tag, "tailnet": tailnet, "error": err.Error()}
	}
	if pondACLRowPresent(body, tag) {
		return "ok", fmt.Sprintf("pond %q: Tailscale plane auto-managed (%s)", pond, tag), map[string]string{"pond": pond, "tag": tag, "tailnet": tailnet, "mode": "auto-managed"}
	}
	return "failed", fmt.Sprintf("pond %q: tailnet policy row missing for %s. Run with $TS_API_KEY exported to auto-install, or apply the snippet from docs/features/pond.md", pond, tag), map[string]string{"pond": pond, "tag": tag, "tailnet": tailnet, "remedy": "see_docs_features_pond_md"}
}

// normalizeHuJSON strips HuJSON line comments and trailing commas from a
// brace-balanced policy section so json.Unmarshal can parse it. Conservative:
// only removes // comments (not /* */) and trailing commas before ] or }.
func normalizeHuJSON(input string) string {
	lines := strings.Split(input, "\n")
	var out strings.Builder
	for i, line := range lines {
		if idx := strings.Index(line, "//"); idx >= 0 {
			line = line[:idx]
		}
		out.WriteString(line)
		if i < len(lines)-1 {
			out.WriteByte('\n')
		}
	}
	return strings.NewReplacer(",]", "]", ",}", "}").Replace(out.String())
}

// pondACLRowPresent checks for the concrete tag declaration and access row
// needed by a pond. The Tailscale policy file is HuJSON and not trivially
// JSON-parseable without an extra dependency, so keep the scan textual but
// exact enough: the tag must appear under tagOwners and either a legacy ACL row
// (`tag` -> `tag:*`) or a grants row (`tag` -> `tag`) must be present.
func pondACLRowPresent(policy, tag string) bool {
	tag = strings.TrimSpace(tag)
	if tag == "" {
		return false
	}
	quotedTag := `"` + tag + `"`
	if !policySectionContains(policy, "tagOwners", quotedTag) {
		return false
	}
	// Structural per-rule check (Codex P1, doctor_pond.go:95/100).
	// The earlier substring-based check matched the tag anywhere in the
	// acls/grants section — so a tag appearing as `src` in one rule and `dst`
	// in another would falsely satisfy the check. Parse each section into
	// rule objects and require a SINGLE rule that has the tag on both src and
	// dst (and a non-empty ip list for grants).
	if acls, ok := policySection(policy, "acls"); ok && pondACLSelfPeerRule(acls, tag) {
		return true
	}
	if grants, ok := policySection(policy, "grants"); ok && pondGrantSelfPeerRule(grants, tag) {
		return true
	}
	return false
}

// pondACLSelfPeerRule scans the acls section (raw section text) for a rule
// where the tag appears both in src and dst. HuJSON policies with comments
// or trailing commas are normalized before parsing.
func pondACLSelfPeerRule(section, tag string) bool {
	var rules []struct {
		Src []string `json:"src"`
		Dst []string `json:"dst"`
	}
	if err := json.Unmarshal([]byte(normalizeHuJSON(section)), &rules); err != nil {
		return false
	}
	for _, r := range rules {
		srcHit := false
		dstHit := false
		for _, s := range r.Src {
			if s == tag {
				srcHit = true
				break
			}
		}
		for _, d := range r.Dst {
			if d == tag || d == tag+":*" {
				dstHit = true
				break
			}
		}
		if srcHit && dstHit {
			return true
		}
	}
	return false
}

// pondGrantSelfPeerRule is the modern grants-shape counterpart of
// pondACLSelfPeerRule. Requires src AND dst AND a non-empty ip list, all in
// the same single rule.
func pondGrantSelfPeerRule(section, tag string) bool {
	var rules []struct {
		Src []string `json:"src"`
		Dst []string `json:"dst"`
		IP  []string `json:"ip"`
	}
	if err := json.Unmarshal([]byte(normalizeHuJSON(section)), &rules); err != nil {
		return false
	}
	for _, r := range rules {
		if len(r.IP) == 0 {
			continue
		}
		srcHit := false
		dstHit := false
		for _, s := range r.Src {
			if s == tag {
				srcHit = true
				break
			}
		}
		for _, d := range r.Dst {
			if d == tag {
				dstHit = true
				break
			}
		}
		if srcHit && dstHit {
			return true
		}
	}
	return false
}

func policySectionContains(policy, section, token string) bool {
	body, ok := policySection(policy, section)
	return ok && strings.Contains(body, token)
}

func policySection(policy, section string) (string, bool) {
	start := activePolicySectionStart(policy, section)
	if start < 0 {
		return "", false
	}
	rest := policy[start+len(section)+2:]
	colon := strings.IndexByte(rest, ':')
	if colon < 0 {
		return "", false
	}
	rest = rest[colon+1:]
	trimmed := strings.TrimLeft(rest, " \t\r\n")
	if trimmed == "" {
		return "", false
	}
	open := trimmed[0]
	close := byte(0)
	switch open {
	case '{':
		close = '}'
	case '[':
		close = ']'
	default:
		return "", false
	}
	depth := 0
	inString := false
	escaped := false
	for i := 0; i < len(trimmed); i++ {
		ch := trimmed[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		if ch == '"' {
			inString = true
			continue
		}
		switch ch {
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return trimmed[:i+1], true
			}
		}
	}
	return "", false
}

func activePolicySectionStart(policy, section string) int {
	offset := 0
	prefix := `"` + section + `"`
	for _, line := range strings.SplitAfter(policy, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if strings.HasPrefix(trimmed, prefix) {
			return offset + strings.Index(line, prefix)
		}
		offset += len(line)
	}
	return -1
}

// liveDoctorTailscaleACLClient is the production implementation. It targets
// the documented "Get tailnet policy file" endpoint and returns the response
// body verbatim so the substring scan above sees the user's HuJSON exactly
// as they wrote it.
type liveDoctorTailscaleACLClient struct {
	apiKey string
	http   *http.Client
}

func newDoctorTailscaleACLClient(apiKey string) doctorTailscaleACLClient {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return nil
	}
	return &liveDoctorTailscaleACLClient{apiKey: apiKey, http: &http.Client{Timeout: doctorPondTimeout}}
}

func (c *liveDoctorTailscaleACLClient) PolicyHuJSON(ctx context.Context, tailnet string) (string, error) {
	if tailnet == "" {
		tailnet = "-"
	}
	url := resolveTailnetAPIURL() + "/api/v2/tailnet/" + tailnet + "/acl"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.apiKey, "")
	req.Header.Set("Accept", "application/hujson")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if readErr != nil {
		return "", readErr
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNotImplemented {
		return "", fmt.Errorf("%w: GET %s returned %d", ErrPondACLAutoBootstrapUnavailable, url, resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Return body as error detail when the response is a JSON error
		// envelope so the caller surfaces actionable text.
		var envelope struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(body, &envelope) == nil && envelope.Message != "" {
			return "", fmt.Errorf("tailscale api %d: %s", resp.StatusCode, envelope.Message)
		}
		return "", fmt.Errorf("tailscale api %d", resp.StatusCode)
	}
	return string(body), nil
}

package cli

import (
	"crypto/sha256"
	"encoding/base32"
	"strings"
)

// pondLabelKey is the reserved provider-label key used to group leases into a
// pond. The key is part of the provider label index that every direct and
// brokered backend already writes, so `list --pond <name>` can select peers
// without growing a new verb tree.
//
// The label is emergent: there is no top-level pond object. A pond exists for
// as long as at least one active lease carries this label.
const pondLabelKey = "pond"

// pondTailscaleTagPrefix is the ACL tag namespace used for pond peers. Every
// member of pond `<name>` owned by `<owner>` is advertised as
// `tag:cbx-pond-<owner>-<name>` so one concrete ACL row gates that pond.
const pondTailscaleTagPrefix = "tag:cbx-pond-"

// pondHostsFile is written on every Tailscale-capable Linux peer. A timer
// refreshes it and a managed /etc/hosts block every 30s from the box-local
// `tailscale status --json` output, so peers stay reachable as `<slug>.cbx`
// without the broker ever seeing a Tailscale credential.
const pondHostsFile = "/etc/hosts.cbx"

// pondHostsRefreshPeriod is the refresh cadence baked into the systemd timer
// that rewrites pond host entries. Kept low so a new peer is discoverable
// within a single user-visible interaction window.
const pondHostsRefreshPeriod = "30s"

// maxRequestedPondNameLength bounds the user-visible portion of the label.
// Provider label values are already capped at 63 characters by
// sanitizeProviderLabelValue; we use a stricter limit here so the same name
// also fits inside future hostname-derived identifiers (e.g. `<pond>.<peer>`)
// without truncation.
const maxRequestedPondNameLength = 41

// maxPondTailscaleTagOwnerLength bounds the owner segment of the pond ACL
// tag. Tailscale tags are limited to 63 characters; with the `tag:cbx-pond-`
// prefix (14 chars) and the pond name suffix (up to 41 chars plus a `-`
// separator) we reserve seven characters for the owner. The owner is
// derived from the operator's git email local-part and truncated rather than
// rejected so the same email shape works for personal and shared tailnets.
const maxPondTailscaleTagOwnerLength = 7

// normalizePondName lowercases the requested name and replaces every character
// outside `[a-z0-9-]` with `-`, collapsing runs and trimming leading/trailing
// dashes. The shape matches normalizeLeaseSlug; pond names participate in the
// same DNS-ish identifier space so peer hostnames stay regular.
func normalizePondName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var out strings.Builder
	lastDash := false
	for _, r := range value {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			out.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			out.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(out.String(), "-")
}

// requestedPondName validates a user-supplied `--pond <name>` flag value.
// Empty input is allowed: callers treat that as "no pond assignment".
func requestedPondName(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	name := normalizePondName(value)
	if name == "" {
		return "", exit(2, "--pond must contain at least one letter or digit")
	}
	if len(name) > maxRequestedPondNameLength {
		return "", exit(2, "--pond must be %d characters or fewer after normalization", maxRequestedPondNameLength)
	}
	return name, nil
}

// serverPond returns the pond label value attached to a server, normalized for
// comparison. Servers without a pond label return the empty string.
func serverPond(server Server) string {
	if server.Labels == nil {
		return ""
	}
	return normalizePondName(server.Labels[pondLabelKey])
}

// filterServersByPond returns the subset of servers whose pond label matches
// the requested name. The filter is a no-op when pond is empty so callers can
// pass `--pond` through unconditionally.
func filterServersByPond(servers []Server, pond string) []Server {
	pond = normalizePondName(pond)
	if pond == "" {
		return servers
	}
	out := make([]Server, 0, len(servers))
	for _, server := range servers {
		if serverPond(server) == pond {
			out = append(out, server)
		}
	}
	return out
}

// pondTagOwner derives the tag-safe owner segment from an operator identity
// (typically `localCoordinatorOwner()` — a git email). Anything outside
// `[a-z0-9-]` collapses to `-`; the result is trimmed and bounded so the full
// tag stays within Tailscale's 63-character ceiling. Returns "" when the
// input does not yield any valid characters; callers fall back to the
// hard-coded "user" segment in that case so the tag prefix stays stable.
func pondTagOwner(identity string) string {
	identity = strings.ToLower(strings.TrimSpace(identity))
	if at := strings.IndexByte(identity, '@'); at > 0 {
		identity = identity[:at]
	}
	owner := normalizePondName(identity)
	if owner == "" {
		return ""
	}
	// Stable hash, not truncation (R4). The earlier 7-char left-truncate of the
	// email local-part risks silent collisions across operators with similar
	// prefixes (e.g. "yossi-eliaz" vs "yossi-elman") sharing tag scope on a
	// shared org tailnet. Replace with a base32(sha256) prefix so two distinct
	// identities never produce the same owner segment.
	if len(owner) > maxPondTailscaleTagOwnerLength {
		sum := sha256.Sum256([]byte(owner))
		encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:])
		if maxPondTailscaleTagOwnerLength > 0 && maxPondTailscaleTagOwnerLength <= len(encoded) {
			return strings.ToLower(encoded[:maxPondTailscaleTagOwnerLength])
		}
		return strings.ToLower(encoded)
	}
	return owner
}

// pondTailscaleTag renders the ACL tag advertised by every pond peer. Returns
// "" when either argument is empty so callers can compose the value
// unconditionally and skip emission when the lease is not actually a pond
// member.
func pondTailscaleTag(owner, pond string) string {
	owner = pondTagOwner(owner)
	if owner == "" {
		owner = "user"
	}
	pond = normalizePondName(pond)
	if pond == "" {
		return ""
	}
	return pondTailscaleTagPrefix + owner + "-" + pond
}

// appendPondTailscaleTag mutates cfg.Tailscale.Tags to include the pond tag
// when both `--pond` and Tailscale are in play. The mint happens entirely in
// user (CLI) context: the same auth key the operator already supplies is
// re-used with one extra advertised tag, so the broker never sees a
// Tailscale credential. No-op when the provider lacks FeatureTailscale or
// when Tailscale is not enabled on this lease.
func appendPondTailscaleTag(cfg *Config, providerSupportsTailscale bool) {
	if cfg == nil || !cfg.Tailscale.Enabled || !providerSupportsTailscale {
		return
	}
	tag := pondTailscaleTag(localCoordinatorOwner(), cfg.Pond)
	if tag == "" {
		return
	}
	cfg.Tailscale.Tags = appendUniqueStrings(cfg.Tailscale.Tags, tag)
}

// providerCapableOfTailscale reports whether the named provider advertises
// FeatureTailscale. Unknown providers return false, mirroring the
// conservative posture other capability checks already take.
func providerCapableOfTailscale(provider string) bool {
	p, err := ProviderFor(provider)
	if err != nil {
		return false
	}
	return featureSetHas(p.Spec().Features, FeatureTailscale)
}

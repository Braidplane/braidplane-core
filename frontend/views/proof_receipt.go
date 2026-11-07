package views

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

func ProofReceiptHref(orgSlug, projectSlug, environmentSlug string, receiptSlug api.Slug) string {
	return fmt.Sprintf("/orgs/%s/projects/%s/environments/%s/proof-receipts/%s", url.PathEscape(orgSlug), url.PathEscape(projectSlug), url.PathEscape(environmentSlug), url.PathEscape(receiptSlug))
}

func ProofHistoryHref(orgSlug, projectSlug, environmentSlug, cursor, direction string) string {
	values := url.Values{"environment_slug": {environmentSlug}}
	if cursor != "" {
		values.Set("cursor", cursor)
		values.Set("direction", direction)
	}
	return fmt.Sprintf("/orgs/%s/projects/%s/proof-receipts?%s", url.PathEscape(orgSlug), url.PathEscape(projectSlug), values.Encode())
}

func ProofFreshnessLabel(receipt api.ProofReceipt) string {
	if receipt.Operational != nil {
		return ProofOperationalAvailabilityLabel(receipt.OperationalEvidenceStatus)
	}
	var expires *time.Time
	observed := false
	for _, claim := range receipt.Claims {
		if claim.Verdict == api.ProofVerdictStale {
			return "Stale evidence"
		}
		observed = observed || claim.ObservedAt != nil
		if claim.ExpiresAt != nil && (expires == nil || claim.ExpiresAt.Before(*expires)) {
			expires = claim.ExpiresAt
		}
	}
	if expires != nil {
		return "Earliest expiry: " + ProofTimestampLabel(expires)
	}
	if observed {
		return "No evidence expiry recorded"
	}
	return "No observed evidence"
}

func ProofOperationalAvailabilityLabel(status *api.ProofReceiptOperationalEvidenceStatus) string {
	if status == nil {
		return "Supporting evidence unavailable"
	}
	switch *status {
	case "available":
		return "Supporting evidence available"
	case "missing":
		return "Supporting evidence missing or pruned"
	case "expired":
		return "Supporting evidence expired"
	case "changed":
		return "Supporting evidence changed or unsupported"
	default:
		return "Supporting evidence unavailable"
	}
}

func ProofLifecycleStatusLabel(status api.ProofLifecycleLinkStatus) string {
	return strings.ReplaceAll(string(status), "_", " ")
}

func ProofLifecycleStatusBadgeClass(status api.ProofLifecycleLinkStatus) string {
	if status == api.ProofLifecycleLinkStatusMatched {
		return badgeSuccessSoft
	}
	return badgeWarningSoft
}

func ProofVerdictBadgeClass(verdict api.ProofVerdict) string {
	switch verdict {
	case api.ProofVerdictPassed:
		return badgeSuccessSoft
	case api.ProofVerdictFailed:
		return badgeErrorSoft
	case api.ProofVerdictStale, api.ProofVerdictInconclusive:
		return badgeWarningSoft
	default:
		return badgeGhostSoft
	}
}

func ProofVerdictLabel(verdict api.ProofVerdict) string {
	return strings.ReplaceAll(string(verdict), "_", " ")
}

func ProofEvidenceLevelLabel(level *api.ProofEvidenceLevel) string {
	if level == nil {
		return "Not observed"
	}
	if *level == api.ProofEvidenceLevelStatic {
		return "Static (metadata only)"
	}
	label := strings.ReplaceAll(string(*level), "_", " ")
	if label == "" {
		return "Not observed"
	}
	return strings.ToUpper(label[:1]) + label[1:]
}

func ProofTimestampLabel(value *time.Time) string {
	if value == nil {
		return "not observed"
	}
	return value.UTC().Format("2006-01-02 15:04:05 UTC")
}

func ProofLifecycleHref(receipt *api.ProofReceipt, link api.ProofLifecycleLink) string {
	if receipt == nil {
		return ""
	}
	orgSlug := receipt.Candidate.OrganizationSlug
	projectSlug := receipt.Candidate.ProjectSlug
	environmentSlug := receipt.Candidate.EnvironmentSlug
	if link.RunSlug != nil {
		return buildEnvironmentRunDetailHref(orgSlug, projectSlug, environmentSlug, *link.RunSlug)
	}
	if link.PipelineSlug != nil {
		return PipelineDeploymentDetailHrefWithEnvironment(orgSlug, projectSlug, environmentSlug, *link.PipelineSlug)
	}
	return ""
}

func ProofClaimCount(receipt *api.ProofReceipt, verdict api.ProofVerdict) int {
	if receipt == nil {
		return 0
	}
	count := 0
	for _, claim := range receipt.Claims {
		if claim.Verdict == verdict {
			count++
		}
	}
	return count
}

func ProofClaimClass(verdict api.ProofVerdict) string {
	const base = "rounded-xl border p-4 "
	switch verdict {
	case api.ProofVerdictPassed:
		return base + "border-success/25 bg-success/5"
	case api.ProofVerdictFailed:
		return base + "border-error/30 bg-error/5"
	case api.ProofVerdictStale, api.ProofVerdictInconclusive:
		return base + "border-warning/25 bg-warning/5"
	default:
		return base + "border-base-300 bg-base-100"
	}
}

func proofReceiptSection(ctx context.Context) string {
	switch section := entityPageURL(ctx).Query().Get("section"); section {
	case "claims", "technical":
		return section
	default:
		return "overview"
	}
}

func proofHistoryBackHref(ctx context.Context, receipt *api.ProofReceipt) string {
	fallback := ProofHistoryHref(receipt.Candidate.OrganizationSlug, receipt.Candidate.ProjectSlug, receipt.Candidate.EnvironmentSlug, "", "")
	candidate := entityBackHref(ctx, fallback)
	u, err := url.Parse(candidate)
	if err != nil || u.Query().Get("environment_slug") != receipt.Candidate.EnvironmentSlug {
		return fallback
	}
	return ProofHistoryHref(receipt.Candidate.OrganizationSlug, receipt.Candidate.ProjectSlug, receipt.Candidate.EnvironmentSlug, u.Query().Get("cursor"), u.Query().Get("direction"))
}

func proofReceiptSectionHref(ctx context.Context, receipt *api.ProofReceipt, section string) string {
	return entityDetailHref(entitySectionHref(ctx, ProofReceiptHref(receipt.Candidate.OrganizationSlug, receipt.Candidate.ProjectSlug, receipt.Candidate.EnvironmentSlug, receipt.Id), section), proofHistoryBackHref(ctx, receipt))
}

func proofHistoryReceiptHref(ctx context.Context, shell AuthenticatedShellData, receipt api.ProofReceipt) string {
	list := ProofHistoryHref(shell.ActiveOrganizationSlug, shell.ActiveProjectSlug, shell.ActiveEnvironmentSlug, entityPageURL(ctx).Query().Get("cursor"), entityPageURL(ctx).Query().Get("direction"))
	return entityDetailHref(ProofReceiptHref(shell.ActiveOrganizationSlug, shell.ActiveProjectSlug, shell.ActiveEnvironmentSlug, receipt.Id), list)
}

// Summarize recorded levels only; required levels are not evidence of observation.
func ProofEvidenceScopeLabel(receipt *api.ProofReceipt) string {
	levels := map[api.ProofEvidenceLevel]int{}
	for _, claim := range receipt.Claims {
		if claim.ActualEvidenceLevel != nil && *claim.ActualEvidenceLevel != "" {
			levels[*claim.ActualEvidenceLevel]++
		} else {
			levels[""]++
		}
	}
	if len(receipt.Claims) == 0 {
		return "No recorded evidence"
	}
	keys := make([]string, 0, len(levels))
	for level := range levels {
		keys = append(keys, string(level))
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		level := api.ProofEvidenceLevel(key)
		parts = append(parts, fmt.Sprintf("%d %s", levels[level], ProofEvidenceLevelLabel(&level)))
	}
	return strings.Join(parts, " · ")
}

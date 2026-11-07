//go:generate -command enumer go tool -modfile=../../tools/go.mod enumer
//go:generate enumer -type=RunnerTrust,NoWriteGuarantee,Outcome -json -text -linecomment -output=policy_enumer.go

// Package proofevidence defines the fail-closed authorization boundary for
// live and bounded Braidplane Proof observations.
package proofevidence

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/hostguard"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const Capability = "proof_evidence_runner"

var ErrAuthorizationDenied = xerror.New(
	xerror.KindForbidden,
	"PROOF_EVIDENCE_AUTHORIZATION_DENIED",
	"Proof evidence authorization denied",
)

type RunnerTrust int

const (
	RunnerTrustUnknown  RunnerTrust = iota // unknown
	RunnerTrustManaged                     // managed_worker
	RunnerTrustCustomer                    // customer_runner
)

type NoWriteGuarantee int

const (
	NoWriteUnknown          NoWriteGuarantee = iota // unknown
	NoWriteReadOnlyAPI                              // provider_read_only_api
	NoWriteNativeValidation                         // native_validation
	NoWriteIsolatedTarget                           // isolated_disposable_target
)

type Endpoint struct {
	Scheme string `json:"scheme"`
	Host   string `json:"host"`
	Port   uint16 `json:"port"`
}

type Limits struct {
	Timeout        time.Duration `json:"timeout"`
	MaxRows        int64         `json:"max_rows"`
	MaxBytes       int64         `json:"max_bytes"`
	MaxRequests    int64         `json:"max_requests"`
	MaxConcurrency int64         `json:"max_concurrency"`
	MaxRetries     int64         `json:"max_retries"`
	RateWindow     time.Duration `json:"rate_window"`
	MaxCostUnits   int64         `json:"max_cost_units"`
}

// Worker identifies the admitted executor assignment, independently of its
// delivery transport or database login. Runtime composition authenticates it.
type Worker struct {
	Identity          string      `json:"identity"`
	Trust             RunnerTrust `json:"trust"`
	AttestationDigest string      `json:"attestation_digest,omitempty"`
	PrivateNetwork    bool        `json:"private_network"`
}

// Request contains identities and bounds only. It cannot carry credentials,
// provider payloads, query parameters, or receipt access authority.
type Request struct {
	JobID                    slug.Slug                    `json:"job_id"`
	OrganizationSlug         slug.Slug                    `json:"organization_slug"`
	ProjectSlug              slug.Slug                    `json:"project_slug"`
	EnvironmentSlug          slug.Slug                    `json:"environment_slug"`
	CandidateSlug            slug.Slug                    `json:"candidate_slug"`
	Candidate                model.ProofCandidateIdentity `json:"candidate"`
	Claim                    model.ProofClaimIdentity     `json:"claim"`
	DataSourceSlug           slug.Slug                    `json:"data_source_slug"`
	CredentialDataSourceSlug slug.Slug                    `json:"credential_data_source_slug"`
	CredentialVersion        string                       `json:"credential_version"`
	TargetFingerprint        string                       `json:"target_fingerprint"`
	ConnectorType            model.DataSourceType         `json:"connector_type"`
	Operation                string                       `json:"operation"`
	EvidenceLevel            model.ProofEvidenceLevel     `json:"evidence_level"`
	Target                   Endpoint                     `json:"target"`
	Egress                   []Endpoint                   `json:"egress"`
	Limits                   Limits                       `json:"limits"`
	Worker                   Worker                       `json:"worker"`
	PolicyVersion            string                       `json:"policy_version"`
	EvidenceRetainUntil      time.Time                    `json:"evidence_retain_until"`
	MetadataRetainUntil      time.Time                    `json:"metadata_retain_until"`
}

// Authorization is an external policy decision bound to the canonical request
// digest. The service issuing it remains responsible for user and tenant checks.
type Authorization struct {
	ID            slug.Slug `json:"id"`
	UserSlug      slug.Slug `json:"user_slug,omitempty"`
	Capability    string    `json:"capability"`
	RequestDigest string    `json:"request_digest"`
	PolicyVersion string    `json:"policy_version"`
	AuthorizedAt  time.Time `json:"authorized_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

type OperationPolicy struct {
	ConnectorType       model.DataSourceType
	Operation           string
	EvidenceLevel       model.ProofEvidenceLevel
	NoWriteGuarantee    NoWriteGuarantee
	ProviderEndpoints   []Endpoint
	Limits              Limits
	RunnerTrust         RunnerTrust
	AllowPrivateNetwork bool
}

type Policy struct {
	Enabled              bool
	Version              string
	MaxAuthorizationTTL  time.Duration
	MaxEvidenceRetention time.Duration
	MaxMetadataRetention time.Duration
	Operations           []OperationPolicy
}

// Grant is created only after the request, policy, and external authorization
// decision match. Executors receive the grant, never a bare request.
type Grant struct {
	request        Request
	authorization  Authorization
	admittedAt     time.Time
	deadline       time.Time
	referenceAt    time.Time
	referenceLocal time.Time
}

func (g Grant) Request() Request { return cloneRequest(g.request) }

// Check verifies that an admitted grant is still usable. Current policy and
// distributed dispatch admission must also be checked by the runner.
func (g Grant) Check() error {
	if g.admittedAt.IsZero() || g.Remaining() <= 0 {
		return denied("Proof evidence grant is absent or expired")
	}
	return nil
}

// Remaining uses local monotonic elapsed time against the authority's clock sample.
// Only authenticated delivery may supply a reference from another machine.
func (g Grant) Remaining() time.Duration { return g.Deadline().Sub(g.now()) }

func (g Grant) now() time.Time {
	return g.referenceAt.Add(time.Since(g.referenceLocal))
}

// Deadline includes time spent waiting for credentials or network access.
func (g Grant) Deadline() time.Time {
	deadline := g.admittedAt.Add(g.request.Limits.Timeout)
	for _, bound := range []time.Time{g.authorization.ExpiresAt, g.request.EvidenceRetainUntil} {
		if bound.Before(deadline) {
			deadline = bound
		}
	}
	if !g.deadline.IsZero() && g.deadline.Before(deadline) {
		deadline = g.deadline
	}
	return deadline
}

// Resume revalidates an encrypted delivery without renewing its durable budget.
// The caller authenticates delivery and supplies the original ledger deadline.
func (p Policy) Resume(request Request, authorization Authorization, deadline time.Time) (Grant, error) {
	return p.ResumeAt(request, authorization, deadline, time.Now())
}

// ResumeAt uses the authenticated coordinator clock sample, never executor wall time.
func (p Policy) ResumeAt(request Request, authorization Authorization, deadline, now time.Time) (Grant, error) {
	grant, err := p.authorizeAt(request, authorization, now)
	if err != nil {
		return Grant{}, err
	}
	if !deadline.After(now) || deadline.After(grant.Deadline()) {
		return Grant{}, ErrAuthorizationDenied
	}
	grant.deadline = deadline
	return grant, nil
}

type Outcome int

const (
	OutcomeUnknown           Outcome = iota // unknown
	OutcomeComplete                         // complete
	OutcomeUnsupported                      // unsupported
	OutcomeDenied                           // denied
	OutcomeUnreachable                      // unreachable
	OutcomeRateLimited                      // rate_limited
	OutcomeTimedOut                         // timed_out
	OutcomeOverBudget                       // over_budget
	OutcomeCanceled                         // canceled
	OutcomeWorkerLost                       // worker_lost
	OutcomePartial                          // partial
	OutcomeProviderError                    // provider_error
	OutcomeAttestationFailed                // attestation_failed
	OutcomeCleanupUncertain                 // cleanup_uncertain
)

type Usage struct {
	Duration       time.Duration `json:"duration"`
	Rows           int64         `json:"rows"`
	Bytes          int64         `json:"bytes"`
	Requests       int64         `json:"requests"`
	MaxConcurrency int64         `json:"max_concurrency"`
	Retries        int64         `json:"retries"`
	CostUnits      int64         `json:"cost_units"`
}

// Result intentionally has no provider-controlled text or raw payload field.
type Result struct {
	RequestDigest  string                   `json:"request_digest"`
	Outcome        Outcome                  `json:"outcome"`
	EvidenceLevel  model.ProofEvidenceLevel `json:"evidence_level"`
	EvidenceDigest string                   `json:"evidence_digest"`
	ObservedAt     time.Time                `json:"observed_at"`
	ExpiresAt      time.Time                `json:"expires_at"`
	Usage          Usage                    `json:"usage"`
}

// Accept rejects every incomplete, failed, stale, mismatched, or over-budget
// observation before it can support a passed or failed Proof claim.
func (g Grant) Accept(result Result) error {
	now := g.now()
	digest, err := g.request.Digest()
	if err != nil {
		return err
	}
	if result.RequestDigest != digest || result.Outcome != OutcomeComplete ||
		result.EvidenceLevel != g.request.EvidenceLevel || !isSHA256Digest(result.EvidenceDigest) ||
		g.Check() != nil || result.ObservedAt.IsZero() ||
		!result.ExpiresAt.After(now) ||
		result.ExpiresAt.After(g.request.EvidenceRetainUntil) || !usageWithin(result.Usage, g.request.Limits) {
		return denied("Proof evidence result is incomplete, mismatched, stale, or over budget")
	}
	return nil
}

func (r Request) Digest() (string, error) {
	encoded, err := json.Marshal(r)
	if err != nil {
		return "", xerror.InvalidInput(xerror.Pipeline).Detailf("Proof evidence request cannot be encoded: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

// Authorize is the only constructor for an executable Proof evidence grant.
func (p Policy) Authorize(request Request, authorization Authorization) (Grant, error) {
	return p.authorizeAt(request, authorization, time.Now())
}

func (p Policy) authorizeAt(request Request, authorization Authorization, now time.Time) (Grant, error) {
	now = now.UTC()
	if !p.Enabled {
		return Grant{}, denied("Proof evidence execution is disabled")
	}
	if strings.TrimSpace(p.Version) == "" || request.PolicyVersion != p.Version {
		return Grant{}, denied("Proof evidence policy version does not match")
	}
	if err := validateRequest(request, now); err != nil {
		return Grant{}, err
	}
	operation, ok := p.operation(request.ConnectorType, request.Operation, request.EvidenceLevel)
	if !ok {
		return Grant{}, denied("Proof evidence operation is not allowlisted")
	}
	if !validNoWriteGuarantee(request.EvidenceLevel, operation.NoWriteGuarantee) {
		return Grant{}, denied("Proof evidence operation lacks an accepted no-write guarantee")
	}
	if request.Worker.Trust != operation.RunnerTrust || request.Worker.PrivateNetwork && !operation.AllowPrivateNetwork {
		return Grant{}, denied("Proof evidence runner trust does not match policy")
	}
	if request.Worker.Trust == RunnerTrustCustomer && !isSHA256Digest(request.Worker.AttestationDigest) {
		return Grant{}, denied("customer Proof runner attestation is required")
	}
	if request.Worker.Trust == RunnerTrustManaged && request.Worker.AttestationDigest != "" {
		return Grant{}, denied("managed Proof runner cannot present customer attestation")
	}
	wantEgress := append([]Endpoint{request.Target}, operation.ProviderEndpoints...)
	if !slices.Equal(request.Egress, wantEgress) {
		return Grant{}, denied("Proof evidence egress does not match the selected target and provider allowlist")
	}
	for _, endpoint := range request.Egress {
		if hostguard.IsBlockedHostnameLiteral(endpoint.Host) &&
			(request.Worker.Trust != RunnerTrustCustomer || !request.Worker.PrivateNetwork || !operation.AllowPrivateNetwork) {
			return Grant{}, denied("Proof evidence private egress requires an authorized customer runner")
		}
	}
	if !within(request.Limits, operation.Limits) {
		return Grant{}, denied("Proof evidence limits exceed policy")
	}
	if p.MaxEvidenceRetention <= 0 || p.MaxMetadataRetention <= 0 ||
		request.EvidenceRetainUntil.After(now.Add(p.MaxEvidenceRetention)) ||
		request.MetadataRetainUntil.After(now.Add(p.MaxMetadataRetention)) ||
		request.EvidenceRetainUntil.After(request.MetadataRetainUntil) {
		return Grant{}, denied("Proof evidence retention exceeds policy")
	}
	if err := validateAuthorization(request, authorization, now, p.MaxAuthorizationTTL); err != nil {
		return Grant{}, err
	}
	return Grant{request: cloneRequest(request), authorization: authorization, admittedAt: now, referenceAt: now, referenceLocal: time.Now()}, nil
}

func cloneRequest(request Request) Request {
	request.Egress = slices.Clone(request.Egress)
	return request
}

func (p Policy) operation(connector model.DataSourceType, operation string, level model.ProofEvidenceLevel) (OperationPolicy, bool) {
	var match OperationPolicy
	found := false
	for _, candidate := range p.Operations {
		if candidate.ConnectorType == connector && candidate.Operation == operation && candidate.EvidenceLevel == level {
			if found {
				return OperationPolicy{}, false
			}
			match, found = candidate, true
		}
	}
	return match, found
}

func validateRequest(request Request, now time.Time) error {
	for _, value := range []slug.Slug{
		request.JobID, request.OrganizationSlug, request.ProjectSlug, request.EnvironmentSlug,
		request.CandidateSlug, request.DataSourceSlug, request.CredentialDataSourceSlug,
	} {
		if _, err := slug.New(value.String()); err != nil {
			return denied("Proof evidence request identity is incomplete")
		}
	}
	if request.OrganizationSlug != request.Candidate.OrganizationSlug ||
		request.ProjectSlug != request.Candidate.ProjectSlug ||
		request.EnvironmentSlug != request.Candidate.EnvironmentSlug ||
		request.Claim.PipelineVersionHash != request.Candidate.SpecHash ||
		request.DataSourceSlug != request.CredentialDataSourceSlug {
		return denied("Proof evidence tenant, candidate, claim, target, and credential identities must match")
	}
	if err := request.Candidate.Validate(); err != nil {
		return denied("Proof evidence candidate identity is invalid")
	}
	if strings.TrimSpace(request.CredentialVersion) == "" || !isSHA256Digest(request.TargetFingerprint) {
		return denied("Proof evidence exact target and credential version are required")
	}
	if !request.Claim.Family.IsAProofClaimFamily() || request.Claim.Family == model.ProofClaimFamilyUnknown ||
		!strings.HasPrefix(request.Claim.SemanticPath, "/") {
		return denied("Proof evidence claim identity is invalid")
	}
	if !request.ConnectorType.IsADataSourceType() || request.ConnectorType == model.DataSourceTypeUnknown ||
		!validIdentifier(request.Operation) ||
		request.EvidenceLevel != model.ProofEvidenceLevelLiveReadOnly && request.EvidenceLevel != model.ProofEvidenceLevelBoundedExecution {
		return denied("Proof evidence connector, operation, or evidence level is invalid")
	}
	if !request.Worker.Trust.IsARunnerTrust() || request.Worker.Trust == RunnerTrustUnknown {
		return denied("Proof evidence runner trust is invalid")
	}
	if !validIdentifier(request.Worker.Identity) {
		return denied("Proof evidence authenticated worker identity is required")
	}
	if err := validateEndpoint(request.Target); err != nil {
		return err
	}
	if len(request.Egress) == 0 {
		return denied("Proof evidence egress allowlist is required")
	}
	for _, endpoint := range request.Egress {
		if err := validateEndpoint(endpoint); err != nil {
			return err
		}
	}
	if !validLimits(request.Limits) {
		return denied("Proof evidence limits are invalid")
	}
	if !request.EvidenceRetainUntil.After(now) || !request.MetadataRetainUntil.After(now) {
		return denied("Proof evidence retention must expire in the future")
	}
	if request.MetadataRetainUntil.Before(now.Add(request.Limits.Timeout).Add(request.Limits.RateWindow)) {
		return denied("Proof evidence metadata must outlive execution and its rate reservation")
	}
	return nil
}

func validateAuthorization(request Request, authorization Authorization, now time.Time, maxTTL time.Duration) error {
	if _, err := slug.New(authorization.ID.String()); err != nil ||
		authorization.Capability != Capability ||
		authorization.PolicyVersion != request.PolicyVersion ||
		maxTTL <= 0 || authorization.AuthorizedAt.IsZero() ||
		authorization.AuthorizedAt.After(now) ||
		authorization.AuthorizedAt.Before(now.Add(-maxTTL)) ||
		!authorization.ExpiresAt.After(now) ||
		!authorization.ExpiresAt.After(authorization.AuthorizedAt) ||
		authorization.ExpiresAt.After(authorization.AuthorizedAt.Add(maxTTL)) {
		return denied("Proof evidence authorization is invalid or expired")
	}
	digest, err := request.Digest()
	if err != nil {
		return err
	}
	if authorization.RequestDigest != digest {
		return denied("Proof evidence authorization does not match the exact request")
	}
	return nil
}

func validNoWriteGuarantee(level model.ProofEvidenceLevel, guarantee NoWriteGuarantee) bool {
	if level == model.ProofEvidenceLevelLiveReadOnly {
		return guarantee == NoWriteReadOnlyAPI
	}
	return guarantee == NoWriteNativeValidation || guarantee == NoWriteIsolatedTarget
}

func validLimits(limits Limits) bool {
	return limits.Timeout > 0 && limits.MaxRows > 0 && limits.MaxBytes > 0 &&
		limits.MaxRequests > 0 && limits.MaxConcurrency > 0 && limits.MaxRetries >= 0 &&
		limits.RateWindow > 0 && limits.MaxCostUnits >= 0
}

func within(requested, maximum Limits) bool {
	return validLimits(maximum) && requested.Timeout <= maximum.Timeout &&
		requested.MaxRows <= maximum.MaxRows && requested.MaxBytes <= maximum.MaxBytes &&
		requested.MaxRequests <= maximum.MaxRequests && requested.MaxConcurrency <= maximum.MaxConcurrency &&
		requested.MaxRetries <= maximum.MaxRetries && requested.RateWindow >= maximum.RateWindow &&
		requested.MaxCostUnits <= maximum.MaxCostUnits
}

func usageWithin(usage Usage, limits Limits) bool {
	return usage.Duration >= 0 && usage.Rows >= 0 && usage.Bytes >= 0 && usage.Requests >= 0 &&
		usage.MaxConcurrency >= 0 && usage.Retries >= 0 && usage.CostUnits >= 0 &&
		usage.Duration <= limits.Timeout && usage.Rows <= limits.MaxRows && usage.Bytes <= limits.MaxBytes &&
		usage.Requests <= limits.MaxRequests && usage.MaxConcurrency <= limits.MaxConcurrency &&
		usage.Retries <= limits.MaxRetries && usage.CostUnits <= limits.MaxCostUnits
}

func validateEndpoint(endpoint Endpoint) error {
	if (endpoint.Scheme != "https" && endpoint.Scheme != "postgres" && endpoint.Scheme != "mysql" && endpoint.Scheme != "mongodb") || endpoint.Host == "" || endpoint.Host != strings.ToLower(endpoint.Host) ||
		strings.ContainsAny(endpoint.Host, "* /?#@") || endpoint.Port == 0 {
		return denied("Proof evidence endpoint must be an exact supported transport host and port")
	}
	host := strings.Trim(endpoint.Host, "[]")
	if net.ParseIP(host) == nil && !validHostname(host) {
		return denied("Proof evidence endpoint host is invalid")
	}
	return nil
}

func validIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !isLowerAlphaNumeric(r) && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

func validHostname(host string) bool {
	if len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.Contains(host, "..") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !isLowerAlphaNumeric(r) && r != '-' {
				return false
			}
		}
	}
	return true
}

func isLowerAlphaNumeric(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= '0' && r <= '9'
}

func isSHA256Digest(value string) bool {
	const prefix = "sha256:"
	raw := strings.TrimPrefix(value, prefix)
	if raw == value || len(raw) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}

func denied(detail string) error {
	return ErrAuthorizationDenied.Detail(detail)
}

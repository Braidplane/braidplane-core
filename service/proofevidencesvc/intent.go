package proofevidencesvc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
)

const intentKind = "proof-observation-intent-v2"

// Intent remains protected backend data. Only an explicitly authorized preview
// response may expose its selected target; it is never a worker job payload.
type Intent struct {
	Kind      string
	Actor     slug.Slug
	Request   proofevidence.Request
	Target    proofevidence.MetadataPlan
	ExpiresAt time.Time
}

func (s *Ledger) Policy(ctx context.Context, scope proofobservations.Scope) (*proofobservations.Policy, error) {
	return postgres.InTx(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*proofobservations.Policy, error) {
		return s.repo.LockPolicy(ctx, db, scope)
	})
}

func (s *Ledger) Job(ctx context.Context, scope proofobservations.Scope, job slug.Slug) (*proofobservations.Job, error) {
	return postgres.InRead(ctx, s.txm, func(ctx context.Context, db postgres.DBTX) (*proofobservations.Job, error) {
		return s.repo.FindScoped(ctx, db, scope, job)
	})
}

func (s *Ledger) SealIntent(actor slug.Slug, request proofevidence.Request, target proofevidence.MetadataPlan, expires time.Time) (string, error) {
	if _, err := slug.New(actor.String()); err != nil || !matchesRequest(target, request) || !expires.After(time.Now()) || expires.After(time.Now().Add(time.Minute)) {
		return "", proofevidence.ErrAuthorizationDenied
	}
	raw, err := json.Marshal(Intent{Kind: intentKind, Actor: actor, Request: request, Target: target, ExpiresAt: expires})
	if err != nil || len(raw) > 12000 {
		return "", proofevidence.ErrAuthorizationDenied
	}
	defer clear(raw)
	sealed, err := s.evidenceCipher.Encrypt(raw)
	if err != nil {
		return "", proofevidence.ErrAuthorizationDenied
	}
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *Ledger) OpenIntent(scope proofobservations.Scope, actor slug.Slug, token, requestDigest string) (*Intent, error) {
	if len(token) == 0 || len(token) > 16384 {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	raw, err := s.evidenceCipher.Decrypt(sealed)
	if err != nil {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	defer clear(raw)
	var intent Intent
	if json.Unmarshal(raw, &intent) != nil || intent.Kind != intentKind || intent.Actor != actor || !intent.ExpiresAt.After(time.Now()) || !matchesRequest(intent.Target, intent.Request) {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	r := intent.Request
	digest, err := r.Digest()
	if err != nil || digest != requestDigest || r.OrganizationSlug != scope.Organization || r.ProjectSlug != scope.Project || r.EnvironmentSlug != scope.Environment {
		return nil, proofevidence.ErrAuthorizationDenied
	}
	return &intent, nil
}

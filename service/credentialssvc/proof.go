package credentialssvc

import (
	"context"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
)

// ProofResolver exposes no ordinary runtime, Git, refresh, or secret-write APIs.
type ProofResolver interface {
	ResolveProofCredential(context.Context, proofevidence.Grant) (RuntimeCredential, error)
}

// ResolveProofCredential resolves only the admitted data-source version. It
// never refreshes OAuth tokens: refresh changes the version and needs admission
// again, and must not introduce unbudgeted provider calls into an observation.
func (s *service) ResolveProofCredential(ctx context.Context, grant proofevidence.Grant) (RuntimeCredential, error) {
	return s.proofCredential(ctx, grant, true)
}

// CheckProofCredential revalidates the version without decrypting secret values.
func (s *service) CheckProofCredential(ctx context.Context, grant proofevidence.Grant) error {
	_, err := s.proofCredential(ctx, grant, false)
	return err
}

func (s *service) proofCredential(ctx context.Context, grant proofevidence.Grant, decrypt bool) (RuntimeCredential, error) {
	if err := grant.Check(); err != nil {
		return RuntimeCredential{}, err
	}
	r := grant.Request()
	credentialType := StaticCredentialTypeHubSpot
	provider := "hubspot"
	switch r.ConnectorType {
	case model.DataSourceTypeHubSpot:
	case model.DataSourceTypeMongoDB:
		credentialType, provider = StaticCredentialTypeMongoDB, ""
	case model.DataSourceTypeS3:
		credentialType, provider = StaticCredentialTypeS3, ""
	case model.DataSourceTypePostgres:
		credentialType, provider = StaticCredentialTypePostgres, ""
	case model.DataSourceTypeMySQL:
		credentialType, provider = StaticCredentialTypeMySQL, ""
	case model.DataSourceTypeGoogleWorkspace:
		provider = "google_workspace"
	case model.DataSourceTypeBigQuery:
		credentialType = StaticCredentialTypeBigQuery
		provider = "bigquery"
	default:
		return RuntimeCredential{}, proofevidence.ErrAuthorizationDenied
	}
	ctx, cancel := context.WithDeadline(ctx, grant.Deadline())
	defer cancel()
	owner := CredentialOwner{Kind: CredentialOwnerKindDataSource, OrgSlug: r.OrganizationSlug, DataSourceSlug: r.CredentialDataSourceSlug}
	params := toRepoResolveParams(ResolveRuntimeCredentialParams{Owner: owner})
	runtime, err := postgres.InRead(postgres.WithoutTransaction(ctx), s.txm, func(ctx context.Context, db postgres.DBTX) (RuntimeCredential, error) {
		storedGrant, err := s.repo.FindOAuthGrant(ctx, db, params)
		if err == nil {
			current := fromRepoCredentialGrant(storedGrant)
			if provider == "" || current.Provider != provider || current.Kind != CredentialKindOAuth2Grant || current.Owner != owner ||
				current.Status != CredentialStatusActive || runtimeCredentialVersion(current.Slug, current.UpdatedAt) != r.CredentialVersion ||
				!current.AccessTokenExpires.After(grant.Deadline()) {
				return RuntimeCredential{}, proofevidence.ErrAuthorizationDenied
			}
			if !decrypt {
				return RuntimeCredential{Version: r.CredentialVersion}, nil
			}
			tokens, err := s.decryptTokenSet(current)
			if err != nil {
				return RuntimeCredential{}, err
			}
			runtime, err := grantRuntimeCredential(current, tokens)
			runtime.Version = r.CredentialVersion
			return runtime, err
		}
		if !xerror.Is(err, ErrCredentialNotFound) {
			return RuntimeCredential{}, err
		}
		stored, err := s.repo.FindStaticSecretByDataSource(ctx, db, params)
		if err != nil {
			return RuntimeCredential{}, err
		}
		current := fromRepoCredentialRecord(stored)
		if current.Owner != owner || current.Kind != CredentialKindStaticSecret || current.Status != CredentialStatusActive ||
			r.ConnectorType == model.DataSourceTypeGoogleWorkspace || current.CredentialType != credentialType || runtimeCredentialVersion(current.Slug, current.UpdatedAt) != r.CredentialVersion {
			return RuntimeCredential{}, proofevidence.ErrAuthorizationDenied
		}
		if !decrypt {
			return RuntimeCredential{Version: r.CredentialVersion}, nil
		}
		plaintext, err := s.decryptPayload(credentialScope(current.Owner.OrgSlug, current.Slug, "static-secret"), current.EncryptedPayload)
		return RuntimeCredential{Kind: CredentialKindStaticSecret, Version: r.CredentialVersion, PlaintextJSON: plaintext}, err
	})
	if err != nil || grant.Check() != nil || ctx.Err() != nil {
		clear(runtime.PlaintextJSON)
		// Repository and provider diagnostics cannot cross the Proof boundary.
		return RuntimeCredential{}, proofevidence.ErrAuthorizationDenied
	}
	return runtime, nil
}

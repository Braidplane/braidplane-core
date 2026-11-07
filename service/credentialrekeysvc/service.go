// Package credentialrekeysvc owns the privileged, resumable product-key sweep.
package credentialrekeysvc

import (
	"context"
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/credentialrekey"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
)

type Service struct {
	txm   postgres.TransactionManager
	repo  credentialrekey.Repository
	ring  *crypto.CredentialKeyring
	audit eventssvc.Service
}

type BatchResult struct {
	After     credentialrekey.Cursor `json:"after"`
	Scanned   int                    `json:"scanned"`
	Rekeyed   int                    `json:"rekeyed"`
	Current   int                    `json:"current"`
	Conflicts int                    `json:"conflicts"`
	Done      bool                   `json:"done"`
}

func New(txm postgres.TransactionManager, repo credentialrekey.Repository, ring *crypto.CredentialKeyring, audit eventssvc.Service) (*Service, error) {
	if txm == nil || repo == nil || ring == nil || audit == nil {
		return nil, xerror.Unexpected().Detail("credential rekey dependencies are required")
	}
	return &Service{txm: txm, repo: repo, ring: ring, audit: audit}, nil
}

// Batch commits a bounded sweep and its audit together. Advance the caller's
// checkpoint only on success; repeat a full sweep after concurrent conflicts.
func (s *Service) Batch(ctx context.Context, after credentialrekey.Cursor, limit int32) (BatchResult, error) {
	if limit < 1 || limit > 100 || after.Family < 0 || after.Family > 6 || after.ID < 0 || after.Family == 0 && after.ID != 0 {
		return BatchResult{}, xerror.ValidationFailed().Detail("invalid credential rekey cursor or batch size")
	}
	result := BatchResult{After: after}
	err := s.txm.RunInTx(postgres.WithoutTransaction(ctx), func(ctx context.Context, db postgres.DBTX) error {
		records, err := s.repo.List(ctx, db, after, limit)
		if err != nil {
			return xerror.Unexpected().Detail("credential rekey census failed")
		}
		for _, record := range records {
			scope := crypto.CredentialScope{Tenant: record.Tenant, Resource: record.Resource, Purpose: record.Purpose, SchemaVersion: 1}
			if record.Family == 6 {
				identity, err := json.Marshal([2]string{record.Resource, record.ResourceAux})
				if err != nil {
					return crypto.ErrCredentialScope
				}
				scope.Resource = string(identity)
			}
			replacement, changed, err := s.ring.Rekey(scope, record.Ciphertext)
			if err != nil {
				return err
			}
			result.Scanned++
			result.After = record.Cursor
			if !changed {
				result.Current++
				continue
			}
			updated, err := s.repo.Replace(ctx, db, record, replacement)
			if err != nil {
				return xerror.Unexpected().Detail("credential rekey replacement failed")
			}
			if !updated {
				result.Conflicts++
				continue
			}
			eventSlug, err := slug.WithRandomSuffix(slug.FromString("encryption-rekey"), 16)
			if err != nil {
				return err
			}
			tenant := slug.FromString(record.Tenant)
			_, err = s.audit.CreateEvent(ctx, eventssvc.CreateEventParams{
				OrgSlug: tenant, Slug: eventSlug, EventClass: model.EventClassSecurityAudit, ActorType: model.EventActorTypeSystem,
				EntityType: model.EventEntityTypeOrganization, EntitySlug: &tenant, Action: model.EventActionRotate,
				Metadata: map[string]any{"operation": "credential_encryption_rekey", "cipher_family": record.Family, "resource_ref": scope.Resource, "encryption_key_id": s.ring.CurrentKeyID()},
			})
			if err != nil {
				return xerror.Unexpected().Detail("credential rekey audit failed")
			}
			result.Rekeyed++
		}
		result.Done = len(records) < int(limit)
		return nil
	})
	if err != nil {
		return BatchResult{}, err
	}
	return result, nil
}

package credentialssvc

import (
	"context"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/postgres"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/stretchr/testify/require"
)

type proofCredentialRepo struct {
	*credentialRepo
	static repocredentials.CredentialRecord
	reads  []repocredentials.ResolveRuntimeCredentialParams
}

func (r *proofCredentialRepo) FindOAuthGrant(ctx context.Context, db postgres.DBTX, params repocredentials.ResolveRuntimeCredentialParams) (repocredentials.CredentialGrant, error) {
	r.reads = append(r.reads, params)
	return r.credentialRepo.FindOAuthGrant(ctx, db, params)
}

func (r *proofCredentialRepo) FindStaticSecretByDataSource(_ context.Context, _ postgres.DBTX, params repocredentials.ResolveRuntimeCredentialParams) (repocredentials.CredentialRecord, error) {
	r.reads = append(r.reads, params)
	return r.static, nil
}

func TestResolveProofCredentialRequiresExactActiveStaticVersionBeforeDecrypt(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"Exact", "RotatedVersion", "Revoked", "GitCredential", "OtherTenant", "OtherDataSource", "ZeroGrant"} {
		t.Run(scenario, func(t *testing.T) {
			now := time.Now().UTC()
			repo := &proofCredentialRepo{credentialRepo: newCredentialRepo(), static: repocredentials.CredentialRecord{
				Slug: "credential-1", OrgSlug: "acme", OwnerKind: repocredentials.OwnerKindDataSource, DataSourceSlug: "warehouse",
				Kind: "static_secret", CredentialType: "bigquery", Status: "active", UpdatedAt: now, EncryptedPayload: []byte(`{"service_account_json":"fixture"}`),
			}}
			r := testutil.ProofEvidenceRequest()
			r.CredentialVersion = runtimeCredentialVersion(repo.static.Slug, now)
			grant := testutil.AdmitProofEvidence(t, r)
			switch scenario {
			case "RotatedVersion":
				repo.static.UpdatedAt = now.Add(time.Second)
			case "Revoked":
				repo.static.Status = "revoked"
			case "GitCredential":
				repo.static.CredentialType = "git"
			case "OtherTenant":
				repo.static.OrgSlug = "other"
			case "OtherDataSource":
				repo.static.DataSourceSlug = "other"
			case "ZeroGrant":
				grant = proofevidence.Grant{}
			}
			decrypts := 0
			enc := testutil.NewFakeEncryptor()
			enc.DecryptFn = func(b []byte) ([]byte, error) { decrypts++; return b, nil }
			svc, err := New(testutil.FakeTransactionManager{}, repo, enc, nil, testutil.FakeClock{}, false)
			require.NoError(t, err)
			checkErr := svc.CheckProofCredential(t.Context(), grant)
			require.Zero(t, decrypts, "authorization monitoring must not decrypt credentials")
			if scenario == "Exact" {
				require.NoError(t, checkErr)
			} else {
				require.ErrorIs(t, checkErr, proofevidence.ErrAuthorizationDenied)
			}
			repo.reads = nil
			got, err := svc.ResolveProofCredential(t.Context(), grant)
			if scenario == "Exact" {
				require.NoError(t, err)
				require.Equal(t, r.CredentialVersion, got.Version)
				require.Equal(t, repo.static.EncryptedPayload, got.PlaintextJSON)
				require.Equal(t, 1, decrypts)
			} else {
				require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
				require.Empty(t, got.PlaintextJSON)
				require.Zero(t, decrypts)
			}
			if scenario == "ZeroGrant" {
				require.Empty(t, repo.reads)
			} else {
				require.Len(t, repo.reads, 2)
				for _, params := range repo.reads {
					require.Equal(t, repocredentials.ResolveRuntimeCredentialParams{OwnerKind: "data_source", OrgSlug: "acme", DataSourceSlug: "warehouse"}, params)
				}
			}
		})
	}
}

func TestResolveProofCredentialNeverRefreshesOrFallsBackFromOAuth(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"hubspot", "bigquery", "google_workspace"} {
		t.Run(provider, func(t *testing.T) {
			for _, scenario := range []string{"Exact", "Expired", "UnknownExpiry", "Revoked", "RotatedVersion", "WrongProvider", "OtherTenant", "OtherDataSource"} {
				t.Run(scenario, func(t *testing.T) {
					now := time.Now().UTC()
					repo := &proofCredentialRepo{credentialRepo: newCredentialRepo()}
					repo.grant = activeRepoGrant(t, now.Add(time.Hour))
					repo.grant.Provider = provider
					repo.grant.OwnerKind = repocredentials.OwnerKindDataSource
					r := testutil.ProofEvidenceRequest()
					if provider == "hubspot" {
						r.ConnectorType = model.DataSourceTypeHubSpot
						r.Operation = "hubspot.contact_metadata"
						r.Target.Host = "api.hubapi.com"
						r.Egress = []proofevidence.Endpoint{r.Target}
					}
					if provider == "google_workspace" {
						r.ConnectorType = model.DataSourceTypeGoogleWorkspace
						r.Operation = "google_workspace.worksheet_metadata"
						r.Target.Host = "sheets.googleapis.com"
						r.Egress = []proofevidence.Endpoint{r.Target, {Scheme: "https", Host: "www.googleapis.com", Port: 443}}
					}
					r.CredentialVersion = runtimeCredentialVersion(repo.grant.Slug, repo.grant.UpdatedAt)
					grant := testutil.AdmitProofEvidence(t, r)
					switch scenario {
					case "Expired":
						repo.grant.AccessTokenExpires = now.Add(-time.Second)
					case "UnknownExpiry":
						repo.grant.AccessTokenExpires = time.Time{}
					case "Revoked":
						repo.grant.Status = "revoked"
					case "RotatedVersion":
						repo.grant.UpdatedAt = now.Add(time.Second)
					case "OtherTenant":
						repo.grant.OrgSlug = "other"
					case "OtherDataSource":
						repo.grant.DataSourceSlug = "other"
					case "WrongProvider":
						repo.grant.Provider = "github"
					}
					decrypts := 0
					enc := testutil.NewFakeEncryptor()
					enc.DecryptFn = func(b []byte) ([]byte, error) { decrypts++; return b, nil }
					svc, err := New(testutil.FakeTransactionManager{}, repo, enc, nil, testutil.FakeClock{}, false)
					require.NoError(t, err)
					got, err := svc.ResolveProofCredential(t.Context(), grant)
					if scenario == "Exact" {
						require.NoError(t, err)
						require.Equal(t, r.CredentialVersion, got.Version)
						require.Contains(t, string(got.PlaintextJSON), `"access_token":"old-access"`)
						require.NotContains(t, string(got.PlaintextJSON), "old-refresh")
						require.Equal(t, 1, decrypts)
					} else {
						require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
						require.Empty(t, got.PlaintextJSON)
						require.Zero(t, decrypts)
					}
					require.Len(t, repo.reads, 1)
				})
			}
		})
	}
}

func TestNativeProofCredentialsCannotUseAnotherConnectorOrTenant(t *testing.T) {
	t.Parallel()
	for _, kind := range []model.DataSourceType{model.DataSourceTypePostgres, model.DataSourceTypeMySQL, model.DataSourceTypeMongoDB, model.DataSourceTypeS3} {
		for _, mismatch := range []string{"none", "connector", "tenant", "revision"} {
			t.Run(kind.String()+"/"+mismatch, func(t *testing.T) {
				now := time.Now().UTC()
				repo := &proofCredentialRepo{credentialRepo: newCredentialRepo(), static: repocredentials.CredentialRecord{Slug: "credential", OrgSlug: "acme", OwnerKind: repocredentials.OwnerKindDataSource, DataSourceSlug: "warehouse", Kind: "static_secret", CredentialType: kind.String(), Status: "active", UpdatedAt: now, EncryptedPayload: []byte(`{"fixture":true}`)}}
				r := testutil.ProofEvidenceRequest()
				r.ConnectorType = kind
				r.Operation = kind.String() + ".metadata"
				r.CredentialVersion = runtimeCredentialVersion(repo.static.Slug, now)
				grant := testutil.AdmitProofEvidence(t, r)
				switch mismatch {
				case "connector":
					repo.static.CredentialType = "git"
				case "tenant":
					repo.static.OrgSlug = "other"
				case "revision":
					repo.static.UpdatedAt = now.Add(time.Second)
				}
				decrypts := 0
				cipher := testutil.NewFakeEncryptor()
				cipher.DecryptFn = func(raw []byte) ([]byte, error) { decrypts++; return raw, nil }
				service, err := New(testutil.FakeTransactionManager{}, repo, cipher, nil, testutil.FakeClock{}, false)
				require.NoError(t, err)
				got, err := service.ResolveProofCredential(t.Context(), grant)
				if mismatch == "none" {
					require.NoError(t, err)
					require.NotEmpty(t, got.PlaintextJSON)
					require.Equal(t, 1, decrypts)
				} else {
					require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
					require.Empty(t, got.PlaintextJSON)
					require.Zero(t, decrypts)
				}
			})
		}
	}
}

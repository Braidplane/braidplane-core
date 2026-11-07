package proofevidencesvc

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/proofevidence"
	"github.com/Braidplane/braidplane-core/postgres/proofobservations"
	"github.com/stretchr/testify/require"
)

func TestIntentCannotBeTransferredChangedOrUsedAfterExpiry(t *testing.T) {
	t.Parallel()
	cipher, err := crypto.NewAES256(bytes.Repeat([]byte{3}, 32))
	require.NoError(t, err)
	ledger := &Ledger{evidenceCipher: cipher}
	target := tableTarget()
	request := requestFor(t, target)
	plan, err := target.Plan()
	require.NoError(t, err)
	scope := proofobservations.Scope{Organization: request.OrganizationSlug, Project: request.ProjectSlug, Environment: request.EnvironmentSlug}
	digest, err := request.Digest()
	require.NoError(t, err)
	token, err := ledger.SealIntent("user-one", request, plan, time.Now().Add(time.Minute))
	require.NoError(t, err)
	intent, err := ledger.OpenIntent(scope, "user-one", token, digest)
	require.NoError(t, err)
	require.Equal(t, request, intent.Request)
	_, err = ledger.OpenIntent(scope, "user-two", token, digest)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	changedScope := scope
	changedScope.Organization = "other-org"
	_, err = ledger.OpenIntent(changedScope, "user-one", token, digest)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	_, err = ledger.OpenIntent(scope, "user-one", token, "sha256:wrong")
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	require.NoError(t, err)
	sealed[len(sealed)-1] ^= 1
	_, err = ledger.OpenIntent(scope, "user-one", base64.RawURLEncoding.EncodeToString(sealed), digest)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
	intent.ExpiresAt = time.Now().Add(-time.Second)
	raw, err := json.Marshal(intent)
	require.NoError(t, err)
	expired, err := cipher.Encrypt(raw)
	require.NoError(t, err)
	_, err = ledger.OpenIntent(scope, "user-one", base64.RawURLEncoding.EncodeToString(expired), digest)
	require.ErrorIs(t, err, proofevidence.ErrAuthorizationDenied)
}

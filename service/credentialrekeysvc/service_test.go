package credentialrekeysvc

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/credentialrekey"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/stretchr/testify/require"
)

type sweepRepo struct {
	records             []credentialrekey.Record
	listErr, replaceErr error
	conflict            bool
	replaced            int
}

func (r *sweepRepo) List(_ context.Context, _ postgres.DBTX, after credentialrekey.Cursor, limit int32) ([]credentialrekey.Record, error) {
	var found []credentialrekey.Record
	for _, record := range r.records {
		if record.Family > after.Family || record.Family == after.Family && record.ID > after.ID {
			found = append(found, record)
			if len(found) == int(limit) {
				break
			}
		}
	}
	return found, r.listErr
}

func (r *sweepRepo) Replace(_ context.Context, _ postgres.DBTX, record credentialrekey.Record, replacement []byte) (bool, error) {
	if r.conflict || r.replaceErr != nil {
		return false, r.replaceErr
	}
	for i := range r.records {
		if r.records[i].Cursor == record.Cursor {
			r.records[i].Ciphertext = replacement
			r.replaced++
			return true, nil
		}
	}
	return false, nil
}

type sweepAudit struct {
	eventssvc.Service
	events []eventssvc.CreateEventParams
	err    error
}

func (a *sweepAudit) CreateEvent(_ context.Context, params eventssvc.CreateEventParams) (*model.Event, error) {
	a.events = append(a.events, params)
	return &model.Event{}, a.err
}

func testSweep(t *testing.T) (*Service, *sweepRepo, *sweepAudit) {
	t.Helper()
	oldKey := bytes.Repeat([]byte{2}, 32)
	ring, err := crypto.NewCredentialKeyring("new", map[string][]byte{"old": oldKey, "new": bytes.Repeat([]byte{3}, 32)}, oldKey)
	require.NoError(t, err)
	legacy, err := crypto.NewAES256(oldKey)
	require.NoError(t, err)
	sealed, err := legacy.Encrypt([]byte("synthetic-value-only"))
	require.NoError(t, err)
	repo := &sweepRepo{records: []credentialrekey.Record{{Cursor: credentialrekey.Cursor{Family: 1, ID: 1}, Tenant: "synthetic-org", Resource: "synthetic-record", Purpose: "static-secret", Ciphertext: sealed}}}
	audit := &sweepAudit{}
	svc, err := New(testutil.FakeTransactionManager{}, repo, ring, audit)
	require.NoError(t, err)
	return svc, repo, audit
}

func TestBatchResumeAndIdempotency(t *testing.T) {
	t.Parallel()
	svc, repo, audit := testSweep(t)
	first, err := svc.Batch(t.Context(), credentialrekey.Cursor{}, 1)
	require.NoError(t, err)
	require.Equal(t, 1, first.Rekeyed)
	require.False(t, first.Done)
	require.Equal(t, repo.records[0].Cursor, first.After)
	require.Len(t, audit.events, 1)
	event := audit.events[0]
	require.Equal(t, model.EventClassSecurityAudit, event.EventClass)
	require.Equal(t, "new", event.Metadata["encryption_key_id"])
	_, err = eventssvc.PrepareCreateEventParams(t.Context(), event)
	require.NoError(t, err)
	encoded, err := json.Marshal(event)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "synthetic-value-only")
	end, err := svc.Batch(t.Context(), first.After, 1)
	require.NoError(t, err)
	require.True(t, end.Done)
	require.Zero(t, end.Scanned)
	before := bytes.Clone(repo.records[0].Ciphertext)
	again, err := svc.Batch(t.Context(), credentialrekey.Cursor{}, 100)
	require.NoError(t, err)
	require.Equal(t, 1, again.Current)
	require.Zero(t, again.Rekeyed)
	require.Equal(t, before, repo.records[0].Ciphertext)
	require.Len(t, audit.events, 1)
}

func TestBatchConflictDoesNotAuditOrOverwrite(t *testing.T) {
	t.Parallel()
	svc, repo, audit := testSweep(t)
	repo.conflict = true
	before := bytes.Clone(repo.records[0].Ciphertext)
	result, err := svc.Batch(t.Context(), credentialrekey.Cursor{}, 100)
	require.NoError(t, err)
	require.Equal(t, 1, result.Conflicts)
	require.Zero(t, result.Rekeyed)
	require.Empty(t, audit.events)
	require.Equal(t, before, repo.records[0].Ciphertext)
}

func TestBatchFailuresDoNotExposeValuesOrAdvanceCheckpoint(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"list", "replace", "audit", "decrypt"} {
		t.Run(stage, func(t *testing.T) {
			svc, repo, audit := testSweep(t)
			sensitive := xerror.Unexpected().Detail("synthetic-value-only")
			switch stage {
			case "list":
				repo.listErr = sensitive
			case "replace":
				repo.replaceErr = sensitive
			case "audit":
				audit.err = sensitive
			case "decrypt":
				repo.records[0].Ciphertext = []byte("synthetic-value-only")
			}
			result, err := svc.Batch(t.Context(), credentialrekey.Cursor{}, 100)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "synthetic-value-only")
			require.Equal(t, BatchResult{}, result)
		})
	}
}

func TestBatchRejectsInvalidBounds(t *testing.T) {
	t.Parallel()
	svc, _, _ := testSweep(t)
	for _, limit := range []int32{0, -1, 101} {
		_, err := svc.Batch(t.Context(), credentialrekey.Cursor{}, limit)
		require.Error(t, err)
	}
	for _, cursor := range []credentialrekey.Cursor{{Family: -1}, {Family: 7}, {Family: 1, ID: -1}, {ID: 1}} {
		_, err := svc.Batch(t.Context(), cursor, 10)
		require.Error(t, err)
	}
}

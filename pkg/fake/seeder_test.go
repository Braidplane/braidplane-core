package fake

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelinerun "github.com/Braidplane/braidplane-core/pkg/pipeline/run"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
)

func TestSeededODCSCatalogDocumentsAreExecutable(t *testing.T) {
	t.Parallel()

	for _, contract := range seededODCSContracts {
		contract := contract
		t.Run(contract.slug.String(), func(t *testing.T) {
			t.Parallel()

			conversion, err := odcs.Convert(contract.document, contract.objectID)
			require.NoError(t, err)
			require.True(t, conversion.DocumentValid)
			require.Empty(t, conversion.Diagnostics)
			require.NotNil(t, conversion.Shape)
			require.Equal(t, contract.objectID, conversion.Shape.Name)
		})
	}

	event, err := pipelinerun.NewVerifiedJSONEventInput("event", json.RawMessage(`{"id":"evt-1","event_type":"user.created","occurred_at":"2026-10-21T09:30:00Z"}`))
	require.NoError(t, err)
	require.JSONEq(t, `{"id":"evt-1","event_type":"user.created","occurred_at":"2026-10-21T09:30:00Z"}`, string(event.Payload().Document))
	eventContract, err := odcs.Convert(seededODCSContracts[0].document, seededEventSchemaObjectID)
	require.NoError(t, err)
	require.Nil(t, payload.ValidateContractFieldValues(eventContract.Shape, []map[string]any{{"id": "evt-1", "event_type": "user.created", "occurred_at": "2026-10-21T09:30:00Z"}}))

	users, err := odcs.Convert(seededODCSContracts[1].document, seededUserSchemaObjectID)
	require.NoError(t, err)
	require.Nil(t, payload.ValidateContractFieldValues(users.Shape, []map[string]any{{"id": int64(1), "email": "owner@example.com", "full_name": "Fake Owner"}}))
}

func TestSeededGitHelpers(t *testing.T) {
	t.Parallel()

	require.Contains(t, gitRepoURL("demo"), "/demo")
	require.Equal(t, gitRepoURL(seededGitRepoName), seededGitRepoURL())
	require.Equal(t, "data-pipelines", seededExampleProjectSlug.String())
	require.Positive(t, gitSSHPort())
	require.NotEmpty(t, gitPrivateKeyPath())

	queryData, err := seededGitQueryData()
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(queryData, &payload))
	require.InEpsilon(t, float64(30), payload["timeout_seconds"], 0)
	var statement map[string]any
	require.NoError(t, json.Unmarshal([]byte(payload["statement"].(string)), &statement))
	require.Equal(t, "git", statement["type"])
	require.Equal(t, seededExampleGitQueryPath, statement["path"])
}

func TestIsolatedSeededGitRepoNameIsUnique(t *testing.T) {
	t.Parallel()

	const generatorCount = 64
	start := make(chan struct{})
	names := make(chan string, generatorCount)
	var generators sync.WaitGroup
	generators.Add(generatorCount)
	for range generatorCount {
		go func() {
			defer generators.Done()
			<-start
			names <- isolatedSeededGitRepoName()
		}()
	}

	close(start)
	generators.Wait()
	close(names)

	uniqueNames := make(map[string]struct{}, generatorCount)
	for name := range names {
		require.Regexp(t, `^seeded-datasource-e2e-[0-9]+-[0-9]+-[0-9]+$`, name)
		uniqueNames[name] = struct{}{}
	}
	require.Len(t, uniqueNames, generatorCount)
}

func TestWriteGitFixtureRepoDisablesInheritedCommitSigning(t *testing.T) {
	t.Parallel()

	repoDir := t.TempDir()
	_, err := writeGitFixtureRepo(context.Background(), repoDir, "base")
	require.NoError(t, err)

	command := exec.CommandContext(t.Context(), "git", "config", "--get", "commit.gpgsign")
	command.Dir = repoDir
	command.Env = cleanGitEnv(os.Environ())
	output, err := command.Output()
	require.NoError(t, err)
	require.Equal(t, "false", strings.TrimSpace(string(output)))

	pipeline, err := pipelinevalidate.LoadFile(filepath.Join(repoDir, filepath.FromSlash(seededExamplePipelineRepoPath)))
	require.NoError(t, err)
	require.Equal(t, "acme-users-sync", pipeline.Metadata.Slug.String())
}

func TestWriteGitFixtureRepoPipelineStates(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		state       string
		wantPresent bool
	}{
		{state: "base", wantPresent: true},
		{state: "deleted", wantPresent: false},
		{state: "restored", wantPresent: true},
	} {
		t.Run(test.state, func(t *testing.T) {
			t.Parallel()
			repoDir := t.TempDir()
			commitSHA, err := writeGitFixtureRepo(t.Context(), repoDir, test.state)
			require.NoError(t, err)
			require.Len(t, commitSHA, 40)
			command := exec.CommandContext(t.Context(), "git", "rev-parse", "HEAD")
			command.Dir = repoDir
			command.Env = cleanGitEnv(os.Environ())
			head, err := command.Output()
			require.NoError(t, err)
			require.Equal(t, commitSHA, strings.TrimSpace(string(head)))

			_, err = os.Stat(filepath.Join(repoDir, filepath.FromSlash(seededExamplePipelineRepoPath)))
			if test.wantPresent {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}

func TestSoftServeUnauthorizedDetection(t *testing.T) {
	t.Parallel()

	require.True(t, isSoftServeUnauthorized("Unauthorized"))
	require.True(t, isSoftServeUnauthorized("permission denied (publickey)"))
	require.False(t, isSoftServeUnauthorized("repository already exists"))
}

func TestCaptureEmailServiceStoresVerificationTokens(t *testing.T) {
	t.Parallel()

	emailService := newCaptureEmailService()
	user := &model.User{Email: "user@example.test"}
	_, err := emailService.EnqueueEmailVerification(context.Background(), nil, user, "https://example.test/verify", "verification-token")
	require.NoError(t, err)
	token, ok := emailService.tokenForEmail(user.Email)
	require.True(t, ok)
	require.Equal(t, "verification-token", token)
	_, err = emailService.EnqueuePasswordReset(context.Background(), nil, user, "https://example.test/reset", "reset-token")
	require.NoError(t, err)

	_, err = emailService.EnqueueEmailVerification(context.Background(), nil, user, "%", "verification-token")
	require.Error(t, err)
	_, err = emailService.EnqueueEmailVerification(context.Background(), nil, user, "https://example.test/verify", "")
	require.Error(t, err)
}

func TestNoopConnectorReturnsSQLResult(t *testing.T) {
	t.Parallel()

	conn, err := newNoopConnector(context.Background(), model.DataSourceTypePostgres, nil, nil)
	require.NoError(t, err)
	require.NoError(t, conn.TestConnection(context.Background()))
	require.NoError(t, conn.Close())

	result, err := conn.Execute(context.Background(), nil, nil)
	require.NoError(t, err)
	sql, ok := result.SQL()
	require.True(t, ok)
	require.Equal(t, 1, sql.RowCount)
	require.Equal(t, []string{"ok"}, sql.ColumnNames)
	require.Equal(t, true, sql.Rows[0]["ok"])
}

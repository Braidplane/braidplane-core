package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"

	"github.com/jackc/pgx/v5/pgxpool"

	backendconfig "github.com/Braidplane/braidplane-core/config/backend"
	"github.com/Braidplane/braidplane-core/pkg/clock"
	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/depsports"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/queue"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/apikeys"
	"github.com/Braidplane/braidplane-core/postgres/compiledplans"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/Braidplane/braidplane-core/postgres/datasourcerefs"
	"github.com/Braidplane/braidplane-core/postgres/datasources"
	driftimpactrepo "github.com/Braidplane/braidplane-core/postgres/driftimpact"
	"github.com/Braidplane/braidplane-core/postgres/environments"
	eventrepo "github.com/Braidplane/braidplane-core/postgres/events"
	"github.com/Braidplane/braidplane-core/postgres/memberinvitations"
	"github.com/Braidplane/braidplane-core/postgres/members"
	reponotifications "github.com/Braidplane/braidplane-core/postgres/notifications"
	"github.com/Braidplane/braidplane-core/postgres/organizations"
	"github.com/Braidplane/braidplane-core/postgres/permissions"
	pipelinesrepo "github.com/Braidplane/braidplane-core/postgres/pipelines"
	"github.com/Braidplane/braidplane-core/postgres/pipelinetriggers"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/postgres/queries"
	retentionrepo "github.com/Braidplane/braidplane-core/postgres/retention"
	"github.com/Braidplane/braidplane-core/postgres/roles"
	"github.com/Braidplane/braidplane-core/postgres/users"
	"github.com/Braidplane/braidplane-core/service/admissionsvc"
	"github.com/Braidplane/braidplane-core/service/apikeyssvc"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcerefssvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/types"
	"github.com/Braidplane/braidplane-core/service/environmentssvc"
	"github.com/Braidplane/braidplane-core/service/eventrecorder"
	"github.com/Braidplane/braidplane-core/service/eventssvc"
	"github.com/Braidplane/braidplane-core/service/memberssvc"
	"github.com/Braidplane/braidplane-core/service/notificationsvc"
	"github.com/Braidplane/braidplane-core/service/odcscontractsvc"
	"github.com/Braidplane/braidplane-core/service/orgsvc"
	"github.com/Braidplane/braidplane-core/service/pipelinesvc"
	"github.com/Braidplane/braidplane-core/service/pipelinetriggersvc"
	"github.com/Braidplane/braidplane-core/service/projectssvc"
	"github.com/Braidplane/braidplane-core/service/queriessvc"
	"github.com/Braidplane/braidplane-core/service/rolessvc"
	"github.com/Braidplane/braidplane-core/service/userssvc"
	"github.com/Braidplane/braidplane-core/service/worker/jobs"
	workerproducer "github.com/Braidplane/braidplane-core/service/worker/producer"
)

// Summary contains the key entities produced by one fake seed run.
type Summary struct {
	RunID                  string
	OwnerUserSlug          string
	MemberUserSlug         string
	OwnerUserEmail         string
	MemberUserEmail        string
	UserPassword           string
	OrganizationSlug       string
	EmptyOrganizationSlug  string
	ProjectSlug            string
	EnvironmentSlug        string
	APIKeySlug             string
	APIKeyToken            string
	HTTPDataSourceSlug     string
	HTTPQuerySlug          string
	DataSourceSlug         string
	QuerySlug              string
	SQLDataSourceSlug      string
	SQLQuerySlug           string
	MySQLDataSourceSlug    string
	MySQLQuerySlug         string
	MySQLSinkQuerySlug     string
	MongoDataSourceSlug    string
	MongoQuerySlug         string
	HubSpotDataSourceSlug  string
	HubSpotQuerySlug       string
	FTPDataSourceSlug      string
	FTPQuerySlug           string
	GitDataSourceSlug      string
	GitQuerySlug           string
	SeededGitCommitSHA     string
	PipelineSlug           string
	NotificationRecordSlug string
	RetentionRunSlug       string
}

type seededDataSourceBinding struct {
	dataSource   *datasourcessvc.DataSource
	capabilities []model.DataSourceRefCapability
}

type fakeDataSourceSchemaImportProducer struct{}

func (fakeDataSourceSchemaImportProducer) EnqueueDataSourceSchemaImport(
	context.Context,
	postgres.DBTX,
	jobs.DataSourceSchemaImportArgs,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{Kind: "data_source_schema_import", Queue: jobs.DataSourceSchemaImportQueue, State: "queued"}, nil
}

const seededUserPassword = "testpass"

var isolatedSeededGitRepoSequence atomic.Uint64

const (
	seededGitPipelineStateBase       = "base"
	seededGitPipelineStateDeleted    = "deleted"
	seededGitPipelineStateRestored   = "restored"
	seededExampleSQLDataSourceSlug   = slug.Slug("acme-postgres")
	seededExampleSQLQuerySlug        = slug.Slug("list-active-users")
	seededExampleMySQLDataSourceSlug = slug.Slug("acme-mysql")
	seededExampleMySQLSinkQuerySlug  = slug.Slug("upsert-users")
	seededExampleGitDataSourceSlug   = slug.Slug("acme-git")
	seededExampleGitQuerySlug        = slug.Slug("read-users-csv")
	seededExampleGitQueryPath        = "users.csv"
	seededExamplePipelinePath        = "examples/pipelines/yaml/single-query.pipeline.yaml"
	seededExamplePipelineRepoPath    = "pipelines/acme-users-sync.pipeline.yaml"
	seededGitRepoName                = "seeded-datasource-repo"
	seededGitTagOnlyRepoName         = "tag-only-datasource-repo"
	seededExampleProjectSlug         = slug.Slug("data-pipelines")
	seededEventContractSlug          = slug.Slug("incoming-events")
	seededEventContractRevisionSlug  = slug.Slug("v1")
	seededEventSchemaObjectID        = "event"
	seededUserContractSlug           = slug.Slug("users-rows")
	seededUserContractRevisionSlug   = slug.Slug("v1")
	seededUserSchemaObjectID         = "user"
)

type seededODCSContract struct {
	slug, revision slug.Slug
	objectID       string
	name, odcsID   string
	document       json.RawMessage
}

var seededODCSContracts = []seededODCSContract{
	{
		slug: seededEventContractSlug, revision: seededEventContractRevisionSlug,
		objectID: seededEventSchemaObjectID, name: "Incoming events", odcsID: "incoming-events",
		document: json.RawMessage(`{
	"apiVersion":"v3.2.0","kind":"DataContract","id":"incoming-events","version":"1","status":"active",
	"description":{"purpose":"Reusable JSON event input for local pipeline examples."},
	"schema":[{"id":"event","name":"Incoming event","logicalType":"object","description":"One verified event payload.","properties":[
		{"name":"id","logicalType":"string","required":true},
		{"name":"event_type","logicalType":"string","required":true},
		{"name":"occurred_at","logicalType":"string","required":true}
	]}]}`),
	},
	{
		slug: seededUserContractSlug, revision: seededUserContractRevisionSlug,
		objectID: seededUserSchemaObjectID, name: "User rows", odcsID: "users-rows",
		document: json.RawMessage(`{
	"apiVersion":"v3.2.0","kind":"DataContract","id":"users-rows","version":"1","status":"active",
	"description":{"purpose":"Reusable user records for the seeded SQL query and row transforms."},
	"schema":[{"id":"user","name":"User row","logicalType":"object","description":"One user record returned by the seeded SQL query.","properties":[
		{"name":"id","logicalType":"integer","required":true},
		{"name":"email","logicalType":"string","required":true},
		{"name":"full_name","logicalType":"string","required":true}
	]}]}`),
	},
}

func gitRepoURL(repoName string) string {
	return "ssh://admin@127.0.0.1:" + strconv.Itoa(depsports.GitSSHPort()) + "/" + repoName
}

func seededGitRepoURL() string {
	return gitRepoURL(seededGitRepoName)
}

func isolatedSeededGitRepoName() string {
	return fmt.Sprintf(
		"seeded-datasource-e2e-%d-%d-%d",
		os.Getpid(),
		time.Now().UnixNano(),
		isolatedSeededGitRepoSequence.Add(1),
	)
}

func gitSSHPort() int {
	return depsports.GitSSHPort()
}

func gitPrivateKeyPath() string {
	return depsports.GitTestPrivateKeyPath()
}

func seededGitQueryData() ([]byte, error) {
	gitStatementJSON, err := json.Marshal(map[string]any{
		"type":      "git",
		"operation": "read",
		"path":      seededExampleGitQueryPath,
		"format":    "csv",
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	gitQueryDataBytes, err := json.Marshal(map[string]any{
		"statement":       string(gitStatementJSON),
		"timeout_seconds": 30,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	return gitQueryDataBytes, nil
}

func bootstrapGitFixtureRepo(
	ctx context.Context,
	privateKeyPath, repoName, pipelineState string,
) (commitSHA string, bootstrapErr error) {
	// Convert relative path to absolute for SSH command
	absKeyPath, err := filepath.Abs(privateKeyPath)
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	knownHostsFile, err := softServeKnownHostsFile(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(knownHostsFile) }()

	repoCreated, err := ensureSoftServeRepo(ctx, absKeyPath, knownHostsFile, repoName)
	if err != nil {
		return "", err
	}
	if repoCreated {
		defer func() {
			if bootstrapErr == nil {
				return
			}
			cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
			defer cleanupCancel()
			_ = deleteSoftServeRepo(cleanupCtx, absKeyPath, repoName)
		}()
	}

	tmpDir, err := os.MkdirTemp("", "braidplane-fake-seed-git-*")
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	commitSHA, err = writeGitFixtureRepo(ctx, tmpDir, pipelineState)
	if err != nil {
		return "", err
	}

	gitSSHCommand := fmt.Sprintf(
		"ssh -i %s -o StrictHostKeyChecking=yes -o UserKnownHostsFile=%s -o IdentitiesOnly=yes -o ConnectTimeout=10 -o BatchMode=yes",
		absKeyPath,
		knownHostsFile,
	)

	if err := repostore.PushRemote(
		ctx,
		tmpDir,
		gitRepoURL(repoName),
		[]string{"GIT_SSH_COMMAND=" + gitSSHCommand},
		repostore.PushOptions{
			Force:   true,
			Refspec: "HEAD:main",
		},
	); err != nil {
		if isSoftServeUnauthorized(err.Error()) {
			return "", xerror.Wrapf(
				"push Git fixtures to remote repository: local Soft Serve auth is out of sync with this worktree; run `task connectors-reset-git` and retry: %w",
				err,
			)
		}
		return "", xerror.Wrapf("push Git fixtures to remote repository: %w", err)
	}

	return commitSHA, nil
}

// bootstrapGitTagOnlyFixtureRepo seeds a reachable remote with tags but no branches.
// It supports Bruno coverage for branch-listing remotes that connect successfully but expose no heads.
func bootstrapGitTagOnlyFixtureRepo(ctx context.Context, privateKeyPath, repoName string) error {
	absKeyPath, err := filepath.Abs(privateKeyPath)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	knownHostsFile, err := softServeKnownHostsFile(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(knownHostsFile) }()

	if _, err := ensureSoftServeRepo(ctx, absKeyPath, knownHostsFile, repoName); err != nil {
		return err
	}

	tmpDir, err := os.MkdirTemp("", "braidplane-fake-seed-git-tag-only-*")
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	if err := writeGitTagOnlyFixtureRepo(ctx, tmpDir); err != nil {
		return err
	}

	gitSSHCommand := fmt.Sprintf(
		"ssh -i %s -o StrictHostKeyChecking=yes -o UserKnownHostsFile=%s -o IdentitiesOnly=yes -o ConnectTimeout=10 -o BatchMode=yes",
		absKeyPath,
		knownHostsFile,
	)

	repoURL := gitRepoURL(repoName)
	env := []string{"GIT_SSH_COMMAND=" + gitSSHCommand}
	if err := repostore.PushRemote(
		ctx,
		tmpDir,
		repoURL,
		env,
		repostore.PushOptions{
			Force:   true,
			Refspec: "refs/tags/v0.1.0:refs/tags/v0.1.0",
		},
	); err != nil {
		if verifyErr := verifyGitTagOnlyFixtureRemote(ctx, repoURL, env); verifyErr == nil {
			return nil
		}
		if isSoftServeUnauthorized(err.Error()) {
			return xerror.Wrapf(
				"push tag-only Git fixtures to remote repository: local Soft Serve auth is out of sync with this worktree; run `task connectors-reset-git` and retry: %w",
				err,
			)
		}
		return xerror.Wrapf("push tag-only Git fixtures to remote repository: %w", err)
	}

	if err := verifyGitTagOnlyFixtureRemote(ctx, repoURL, env); err != nil {
		return err
	}

	return nil
}

// verifyGitTagOnlyFixtureRemote confirms the tag-only remote is reachable but has no heads.
// Soft Serve can report a non-zero status after accepting a tag-only push, so this checks state.
func verifyGitTagOnlyFixtureRemote(ctx context.Context, repoURL string, env []string) error {
	if err := repostore.CheckRemote(ctx, repoURL, env); err != nil {
		return xerror.Wrapf("verify tag-only Git fixture remote refs: %w", err)
	}
	branches, err := repostore.ListRemoteBranches(ctx, repoURL, env)
	// A branchless remote can make ListRemoteBranches report a repository setup error.
	if err != nil {
		if xerror.Is(err, repostore.ErrRepositorySetup) && isBranchlessGitRemoteError(err) {
			return nil
		}
		return xerror.Wrapf("verify tag-only Git fixture remote branches: %w", err)
	}
	if len(branches) > 0 {
		return xerror.Unexpected().Detail("tag-only Git fixture unexpectedly has branch heads")
	}
	return nil
}

// isBranchlessGitRemoteError reports whether git failed because the remote exposes no branch heads.
func isBranchlessGitRemoteError(err error) bool {
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "no matching refs") ||
		strings.Contains(msg, "no branch") ||
		strings.Contains(msg, "no head")
}

func writeGitFixtureRepo(ctx context.Context, repoDir, pipelineState string) (string, error) {
	if pipelineState != seededGitPipelineStateBase &&
		pipelineState != seededGitPipelineStateDeleted &&
		pipelineState != seededGitPipelineStateRestored {
		return "", xerror.ValidationFailed().Detailf("unsupported Git fixture pipeline state %q", pipelineState)
	}
	if output, err := runGitInDir(ctx, repoDir, "init", "--object-format=sha1"); err != nil {
		return "", xerror.Wrapf("init Git fixture repository: %s: %w", output, err)
	}
	if output, err := runGitInDir(ctx, repoDir, "config", "user.name", "braidplane-fixture"); err != nil {
		return "", xerror.Wrapf("configure Git fixture user name: %s: %w", output, err)
	}
	if output, err := runGitInDir(
		ctx,
		repoDir,
		"config",
		"user.email",
		"braidplane-fixture@example.com",
	); err != nil {
		return "", xerror.Wrapf("configure Git fixture user email: %s: %w", output, err)
	}
	if output, err := runGitInDir(ctx, repoDir, "config", "commit.gpgsign", "false"); err != nil {
		return "", xerror.Wrapf("disable Git fixture commit signing: %s: %w", output, err)
	}

	if err := writeGitFixtureFile(
		repoDir,
		"users.csv",
		[]byte("id,email\n1,alice@example.com\n2,bob@example.com\n"),
	); err != nil {
		return "", xerror.Wrapf("seed users.csv fixture: %w", err)
	}

	if err := writeGitFixtureFile(
		repoDir,
		"users.json",
		[]byte(
			`[{"id":1,"email":"alice@example.com","profile":{"locale":"en"}},{"id":2,"email":"bob@example.com","profile":{"locale":"pl"}}]`,
		),
	); err != nil {
		return "", xerror.Wrapf("seed users.json fixture: %w", err)
	}

	seededPipelineSource, err := loadSeededPipelineSource()
	if err != nil {
		return "", err
	}
	if err := writeGitFixtureFile(repoDir, seededExamplePipelineRepoPath, seededPipelineSource); err != nil {
		return "", xerror.Wrapf("seed pipeline fixture: %w", err)
	}
	if output, err := runGitInDir(ctx, repoDir, "add", "-A"); err != nil {
		return "", xerror.Wrapf("stage Git fixture files: %s: %w", output, err)
	}
	if output, err := runGitInDir(ctx, repoDir, "commit", "-m", "seed git data source fixtures"); err != nil {
		return "", xerror.Wrapf("commit Git fixture files: %s: %w", output, err)
	}

	if pipelineState == seededGitPipelineStateDeleted || pipelineState == seededGitPipelineStateRestored {
		if err := os.Remove(filepath.Join(repoDir, filepath.FromSlash(seededExamplePipelineRepoPath))); err != nil {
			return "", xerror.Wrapf("remove seeded pipeline fixture: %w", err)
		}
		if output, err := runGitInDir(ctx, repoDir, "add", "-A"); err != nil {
			return "", xerror.Wrapf("stage deleted pipeline fixture: %s: %w", output, err)
		}
		if output, err := runGitInDir(ctx, repoDir, "commit", "-m", "delete pipeline fixture"); err != nil {
			return "", xerror.Wrapf("commit deleted pipeline fixture: %s: %w", output, err)
		}
	}

	if pipelineState == seededGitPipelineStateRestored {
		if err := writeGitFixtureFile(repoDir, seededExamplePipelineRepoPath, seededPipelineSource); err != nil {
			return "", xerror.Wrapf("restore seeded pipeline fixture: %w", err)
		}
		if output, err := runGitInDir(ctx, repoDir, "add", "-A"); err != nil {
			return "", xerror.Wrapf("stage restored pipeline fixture: %s: %w", output, err)
		}
		if output, err := runGitInDir(ctx, repoDir, "commit", "-m", "restore pipeline fixture"); err != nil {
			return "", xerror.Wrapf("commit restored pipeline fixture: %s: %w", output, err)
		}
	}

	commitSHA, err := runGitInDir(ctx, repoDir, "rev-parse", "HEAD")
	if err != nil {
		return "", xerror.Wrapf("resolve Git fixture commit: %s: %w", commitSHA, err)
	}
	return commitSHA, nil
}

// writeGitTagOnlyFixtureRepo creates a local repository with one tagged commit and no pushed branch.
// Callers choose a tag-only refspec so the remote remains branchless.
func writeGitTagOnlyFixtureRepo(ctx context.Context, repoDir string) error {
	if output, err := runGitInDir(ctx, repoDir, "init"); err != nil {
		return xerror.Wrapf("init tag-only Git fixture repository: %s: %w", output, err)
	}
	if output, err := runGitInDir(ctx, repoDir, "config", "user.name", "braidplane-fixture"); err != nil {
		return xerror.Wrapf("configure tag-only Git fixture user name: %s: %w", output, err)
	}
	if output, err := runGitInDir(
		ctx,
		repoDir,
		"config",
		"user.email",
		"braidplane-fixture@example.com",
	); err != nil {
		return xerror.Wrapf("configure tag-only Git fixture user email: %s: %w", output, err)
	}
	if output, err := runGitInDir(ctx, repoDir, "config", "commit.gpgsign", "false"); err != nil {
		return xerror.Wrapf("disable tag-only Git fixture commit signing: %s: %w", output, err)
	}

	if err := writeGitFixtureFile(
		repoDir,
		"README.md",
		[]byte("# Tag-only fixture\n"),
	); err != nil {
		return xerror.Wrapf("seed tag-only README fixture: %w", err)
	}
	if output, err := runGitInDir(ctx, repoDir, "add", "-A"); err != nil {
		return xerror.Wrapf("stage tag-only Git fixture files: %s: %w", output, err)
	}
	if output, err := runGitInDir(ctx, repoDir, "commit", "-m", "seed tag-only git fixture"); err != nil {
		return xerror.Wrapf("commit tag-only Git fixture files: %s: %w", output, err)
	}
	if output, err := runGitInDir(ctx, repoDir, "tag", "v0.1.0"); err != nil {
		return xerror.Wrapf("tag tag-only Git fixture: %s: %w", output, err)
	}

	return nil
}

func writeGitFixtureFile(repoDir, repoPath string, data []byte) error {
	fullPath := filepath.Join(repoDir, filepath.FromSlash(repoPath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o750); err != nil {
		return xerror.Wrapf("create Git fixture directory for %q: %w", repoPath, err)
	}
	if err := os.WriteFile(fullPath, data, 0o600); err != nil {
		return xerror.Wrapf("write Git fixture file %q: %w", repoPath, err)
	}
	return nil
}

func runGitInDir(ctx context.Context, repoDir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = repoDir
	command.Env = cleanGitEnv(os.Environ())
	output, err := command.CombinedOutput()
	return strings.TrimSpace(string(output)), err
}

func seedDataSourceBinding(
	ctx context.Context,
	dataSourceRefsService datasourcerefssvc.Service,
	orgSlug, projectSlug, pipelineSlug, environmentSlug slug.Slug,
	binding seededDataSourceBinding,
	actor model.EventActor,
) error {
	if binding.dataSource == nil {
		return xerror.Unexpected().Detail("seed data source binding requires data source")
	}

	if _, err := dataSourceRefsService.CreateDataSourceRef(ctx, orgSlug, projectSlug, pipelineSlug, model.NewDataSourceRef{
		Name:                 binding.dataSource.Name,
		Slug:                 binding.dataSource.Slug,
		DataSourceType:       binding.dataSource.Type,
		RequiredCapabilities: binding.capabilities,
	}, actor); err != nil {
		return xerror.Wrapf("create runtime data source ref %s: %w", binding.dataSource.Slug, err)
	}
	if _, err := dataSourceRefsService.UpsertEnvironmentBinding(
		ctx,
		orgSlug,
		projectSlug,
		pipelineSlug,
		environmentSlug,
		binding.dataSource.Slug,
		model.UpsertDataSourceBinding{DataSourceSlug: binding.dataSource.Slug},
		actor,
	); err != nil {
		return xerror.Wrapf("bind runtime data source ref %s: %w", binding.dataSource.Slug, err)
	}

	return nil
}

func seedPipelineDeployment(
	ctx context.Context,
	txm postgres.TransactionManager,
	pipelineStore pipelinesvc.PipelineStore,
	orgSlug, projectSlug, environmentSlug, pipelineSlug slug.Slug,
	actor model.EventActor,
) error {
	spec, err := pipelineStore.FindSpec(ctx, orgSlug, projectSlug, pipelineSlug)
	if err != nil {
		return xerror.Wrapf("find seeded pipeline version %s: %w", pipelineSlug, err)
	}
	if spec.CurrentVersionID == nil {
		return xerror.Unexpected().Detailf("seeded pipeline %s has no current version", pipelineSlug)
	}

	return txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		_, deployErr := pipelineStore.Deploy(
			ctx,
			orgSlug,
			projectSlug,
			environmentSlug,
			pipelinesrepo.DeployParams{
				PipelineSlug:        pipelineSlug,
				VersionID:           *spec.CurrentVersionID,
				DeployedByActorType: actor.Type.String(),
				DeployedByActorSlug: actor.Slug,
			},
		)
		if deployErr != nil {
			return xerror.Wrapf("deploy seeded pipeline %s to %s: %w", pipelineSlug, environmentSlug, deployErr)
		}
		return nil
	})
}

func cleanGitEnv(base []string) []string {
	result := make([]string, 0, len(base))
	for _, entry := range base {
		if strings.HasPrefix(entry, "GIT_") {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func ensureSoftServeRepo(
	ctx context.Context,
	absoluteKeyPath, knownHostsFile, repoName string,
) (bool, error) {
	args := []string{
		"-p", strconv.Itoa(gitSSHPort()),
		"-i", absoluteKeyPath,
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + knownHostsFile,
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"admin@127.0.0.1",
		"repo", "create", repoName,
	}

	command := exec.CommandContext(ctx, "ssh", args...)
	output, err := command.CombinedOutput()
	if err == nil {
		return true, nil
	}

	message := strings.ToLower(strings.TrimSpace(string(output)))
	if strings.Contains(message, "already") || strings.Contains(message, "exists") {
		return false, nil
	}

	return false, xerror.Wrapf(
		"create Soft Serve repository %q: %s: %w",
		repoName,
		strings.TrimSpace(string(output)),
		err,
	)
}

func deleteSoftServeRepo(ctx context.Context, privateKeyPath, repoName string) error {
	absKeyPath, err := filepath.Abs(privateKeyPath)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	knownHostsFile, err := softServeKnownHostsFile(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(knownHostsFile) }()

	args := []string{
		"-p", strconv.Itoa(gitSSHPort()),
		"-i", absKeyPath,
		"-o", "StrictHostKeyChecking=yes",
		"-o", "UserKnownHostsFile=" + knownHostsFile,
		"-o", "IdentitiesOnly=yes",
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
		"admin@127.0.0.1",
		"repo", "delete", repoName,
	}

	command := exec.CommandContext(ctx, "ssh", args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return xerror.Wrapf(
			"delete Soft Serve repository %q: %s: %w",
			repoName,
			strings.TrimSpace(string(output)),
			err,
		)
	}

	return nil
}

func softServeKnownHosts(ctx context.Context) (string, error) {
	output, err := exec.CommandContext(
		ctx,
		"ssh-keyscan",
		"-p", strconv.Itoa(gitSSHPort()),
		"127.0.0.1",
	).CombinedOutput()
	if err != nil {
		return "", xerror.Wrapf(
			"scan Soft Serve host key: %s: %w",
			strings.TrimSpace(string(output)),
			err,
		)
	}

	knownHosts := strings.TrimSpace(string(output))
	if knownHosts == "" {
		return "", xerror.Unexpected().Detail("scan Soft Serve host key: empty known_hosts output")
	}

	return knownHosts + "\n", nil
}

func softServeKnownHostsFile(ctx context.Context) (string, error) {
	knownHosts, err := softServeKnownHosts(ctx)
	if err != nil {
		return "", err
	}

	tmpFile, err := os.CreateTemp("", "braidplane-soft-serve-known-hosts-*")
	if err != nil {
		return "", xerror.Unexpected().With(err)
	}
	if _, err := tmpFile.WriteString(knownHosts); err != nil {
		_ = os.Remove(tmpFile.Name())
		return "", xerror.Unexpected().With(err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpFile.Name())
		return "", xerror.Unexpected().With(err)
	}

	return tmpFile.Name(), nil
}

func BootstrapSeededGitFixtures(ctx context.Context) (string, error) {
	repoURL, _, err := BootstrapSeededGitFixtureState(ctx, seededGitPipelineStateBase)
	if err != nil {
		return "", err
	}
	if err := bootstrapGitTagOnlyFixtureRepo(ctx, gitPrivateKeyPath(), seededGitTagOnlyRepoName); err != nil {
		return "", err
	}
	return repoURL, nil
}

// BootstrapSeededGitFixtureState force-pushes one deterministic lifecycle state
// to the worktree-local Soft Serve repository and returns its exact commit.
func BootstrapSeededGitFixtureState(ctx context.Context, pipelineState string) (string, string, error) {
	privateKeyPath := gitPrivateKeyPath()
	if !filepath.IsAbs(privateKeyPath) {
		privateKeyPath = filepath.Clean(privateKeyPath)
	}

	if _, err := os.ReadFile(privateKeyPath); err != nil {
		return "", "", xerror.Unexpected().With(err)
	}

	commitSHA, err := bootstrapGitFixtureRepo(ctx, privateKeyPath, seededGitRepoName, pipelineState)
	if err != nil {
		return "", "", err
	}
	return seededGitRepoURL(), commitSHA, nil
}

// BootstrapIsolatedSeededGitFixture creates a caller-owned Git fixture repository.
// The returned cleanup must run after the caller finishes using the repository.
func BootstrapIsolatedSeededGitFixture(
	ctx context.Context,
) (string, func(context.Context) error, error) {
	privateKeyPath, err := filepath.Abs(gitPrivateKeyPath())
	if err != nil {
		return "", nil, xerror.Unexpected().With(err)
	}

	if _, err = os.Stat(privateKeyPath); err != nil {
		return "", nil, xerror.Unexpected().With(err)
	}

	repoName := isolatedSeededGitRepoName()
	if _, err := bootstrapGitFixtureRepo(ctx, privateKeyPath, repoName, seededGitPipelineStateBase); err != nil {
		return "", nil, err
	}

	cleanup := func(cleanupCtx context.Context) error {
		return deleteSoftServeRepo(cleanupCtx, privateKeyPath, repoName)
	}

	return gitRepoURL(repoName), cleanup, nil
}

func isSoftServeUnauthorized(message string) bool {
	message = strings.ToLower(strings.TrimSpace(message))
	return strings.Contains(message, "unauthorized") ||
		strings.Contains(message, "permission denied")
}

func seededPipelineCandidates() []string {
	candidates := []string{
		seededExamplePipelinePath,
		filepath.Join("..", seededExamplePipelinePath),
		filepath.Join("..", "..", seededExamplePipelinePath),
	}
	if _, filePath, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Join(filepath.Dir(filePath), "..", "..", seededExamplePipelinePath))
	}
	return candidates
}

func loadSeededPipelineSource() ([]byte, error) {
	for _, candidate := range seededPipelineCandidates() {
		// #nosec G304 -- candidates are fixed repository-relative fixture paths plus the package path from runtime.Caller.
		data, err := os.ReadFile(candidate)
		if err != nil {
			if xerror.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, xerror.Wrapf("read seeded pipeline example %s: %w", candidate, err)
		}
		return data, nil
	}

	return nil, xerror.Unexpected().Detailf(
		"seeded pipeline example not found at %s",
		seededExamplePipelinePath,
	)
}

func loadSeededPipelineSpec() (*pipelinespec.Pipeline, error) {
	for _, candidate := range seededPipelineCandidates() {
		if _, err := os.Stat(candidate); err != nil {
			if xerror.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, xerror.Wrapf("stat seeded pipeline example %s: %w", candidate, err)
		}

		pipeline, err := pipelinevalidate.LoadFile(candidate)
		if err != nil {
			return nil, xerror.Wrapf("load seeded pipeline example %s: %w", candidate, err)
		}

		return pipeline, nil
	}

	return nil, xerror.Unexpected().Detailf(
		"seeded pipeline example not found at %s",
		seededExamplePipelinePath,
	)
}

type captureEmailService struct {
	tokensByEmail map[string]string
}

func newCaptureEmailService() *captureEmailService {
	return &captureEmailService{tokensByEmail: map[string]string{}}
}

func (n *captureEmailService) EnqueueEmailVerification(
	_ context.Context,
	_ postgres.DBTX,
	user *model.User,
	link string,
	token string,
) (queue.QueuedJob, error) {
	parsed, err := url.Parse(link)
	if err != nil {
		return queue.QueuedJob{}, xerror.Unexpected().With(err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return queue.QueuedJob{}, xerror.Unexpected().Detail("verification link invalid")
	}

	if token == "" {
		return queue.QueuedJob{}, xerror.Unexpected().Detail("verification token missing")
	}

	n.tokensByEmail[user.Email] = token
	return queue.QueuedJob{}, nil
}

func (n *captureEmailService) EnqueuePasswordReset(
	_ context.Context,
	_ postgres.DBTX,
	_ *model.User,
	_ string,
	_ string,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

func (n *captureEmailService) EnqueueSessionMFA(
	_ context.Context,
	_ postgres.DBTX,
	_ *model.User,
	_ string,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

func (n *captureEmailService) EnqueueMessage(context.Context, postgres.DBTX, *model.User, string, string) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

func (n *captureEmailService) EnqueueMemberInvitation(
	_ context.Context,
	_ postgres.DBTX,
	_ model.MemberInvitationEmail,
	_ string,
	_ string,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{}, nil
}

func (n *captureEmailService) EnqueueNotification(
	_ context.Context,
	_ postgres.DBTX,
	_ slug.Slug,
	_ model.NotificationEmailRecipient,
	_ model.NotificationEmail,
	_ slug.Slug,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{Kind: "email_send", Queue: "email", State: "queued"}, nil
}

func (n *captureEmailService) tokenForEmail(email string) (string, bool) {
	token, ok := n.tokensByEmail[email]
	return token, ok
}

type noopConnector struct{}

func (n *noopConnector) TestConnection(context.Context) error {
	return nil
}

func (n *noopConnector) Close() error {
	return nil
}

func (n *noopConnector) Execute(
	context.Context,
	json.RawMessage,
	map[string]types.TypedParam,
	...types.ExecutionLimits,
) (*result.ExecutionResult, error) {
	rows := []map[string]any{{"ok": true}}
	sql := &result.SQLResult{Rows: rows, ColumnNames: []string{"ok"}, RowCount: 1}
	return result.NewSQLResult("fake", sql, 0, nil), nil
}

func newNoopConnector(
	context.Context,
	model.DataSourceType,
	[]byte,
	[]byte,
) (connector.Connector, error) {
	return &noopConnector{}, nil
}

type fakeEventProducer struct {
	events eventssvc.Service
}

func (p fakeEventProducer) EnqueueRecord(
	ctx context.Context,
	tx postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) (workerproducer.EventRecordJob, bool) {
	params, err := eventrecorder.ParamsFor(orgSlug, actor, event)
	if err != nil {
		return workerproducer.EventRecordJob{}, false
	}
	created, err := p.events.CreateEvent(ctx, params)
	if err != nil {
		return workerproducer.EventRecordJob{}, false
	}
	if created != nil {
		return workerproducer.EventRecordJob{EventSlug: created.Slug}, true
	}
	return workerproducer.EventRecordJob{EventSlug: params.Slug}, true
}

func (p fakeEventProducer) EnqueueRequired(
	ctx context.Context,
	_ postgres.DBTX,
	orgSlug slug.Slug,
	actor model.EventActor,
	event eventrecorder.Event,
) (workerproducer.EventRecordJob, error) {
	params, err := eventrecorder.ParamsFor(orgSlug, actor, event)
	if err != nil {
		return workerproducer.EventRecordJob{}, err
	}
	params, err = eventssvc.PrepareCreateEventParams(ctx, params)
	if err != nil {
		return workerproducer.EventRecordJob{}, err
	}
	if params.EventClass != model.EventClassSecurityAudit {
		return workerproducer.EventRecordJob{}, xerror.InvalidInput(xerror.Event).
			Detail("required durable event must use the security audit class")
	}
	created, err := p.events.CreateEvent(ctx, params)
	if err != nil {
		return workerproducer.EventRecordJob{}, err
	}
	if created != nil {
		return workerproducer.EventRecordJob{EventSlug: created.Slug}, nil
	}
	return workerproducer.EventRecordJob{EventSlug: params.Slug}, nil
}

type seedNotificationDeliveryProducer struct{}

// EnqueueNotificationDelivery records a deterministic queued delivery handoff for fake seeded notifications.
func (p seedNotificationDeliveryProducer) EnqueueNotificationDelivery(
	context.Context,
	postgres.DBTX,
	notificationsvc.NotificationDeliveryJobParams,
) (queue.QueuedJob, error) {
	return queue.QueuedJob{Kind: "notification_delivery", Queue: "notification_delivery", State: "queued"}, nil
}

// seedNotificationDiagnostics creates one visible notification and delivery attempt for UI and API smoke coverage.
func seedNotificationDiagnostics(
	ctx context.Context,
	service notificationsvc.Service,
	orgSlug slug.Slug,
	ownerSlug slug.Slug,
	runID string,
	pipelineSlug slug.Slug,
) (slug.Slug, error) {
	principal := notificationsvc.AlertManagementPrincipal{UserSlug: ownerSlug}
	destinationSlug := slug.Slug("fake-alert-destination-" + runID)
	policySlug := slug.Slug("fake-alert-policy-" + runID)
	recordSlug := slug.Slug("fake-notification-" + runID)
	notificationType := model.NotificationTypePipelineRetryExhausted
	severity := model.NotificationSeverityCritical

	if _, err := service.CreateAlertDestination(ctx, principal, notificationsvc.CreateAlertDestinationParams{
		OrgSlug: orgSlug,
		Slug:    destinationSlug,
		Name:    "Seeded operations email",
		Type:    model.AlertDestinationTypeEmail,
		NonSecretConfig: map[string]any{
			"recipients": []string{"alerts-" + runID + "@example.com"},
		},
	}); err != nil {
		return "", xerror.Wrapf("seed alert destination: %w", err)
	}

	if _, err := service.CreateAlertPolicy(ctx, principal, notificationsvc.CreateAlertPolicyParams{
		OrgSlug:          orgSlug,
		Slug:             policySlug,
		Name:             "Seeded pipeline retry exhausted policy",
		Scope:            model.AlertPolicyScopeOrganization,
		NotificationType: &notificationType,
		Severity:         &severity,
		DestinationSlugs: []slug.Slug{destinationSlug},
	}); err != nil {
		return "", xerror.Wrapf("seed alert policy: %w", err)
	}

	if _, err := service.CreateCandidate(ctx, notificationsvc.CreateCandidateParams{
		OrgSlug:           orgSlug,
		RecordSlug:        &recordSlug,
		NotificationType:  model.NotificationTypePipelineRetryExhausted,
		Severity:          model.NotificationSeverityCritical,
		DedupeFingerprint: "fake-seed:pipeline.retry_exhausted:" + runID,
		SourceEntityType:  model.EventEntityTypePipelineRun,
		SourceAction:      model.EventActionExecute,
		SourceRefs: map[string]string{
			"pipeline_slug": pipelineSlug.String(),
			"run_slug":      "fake-run-" + runID,
		},
		SafeMetadata: map[string]any{
			"summary": "Seeded pipeline retry exhausted needs attention",
		},
	}); err != nil {
		return "", xerror.Wrapf("seed notification record: %w", err)
	}

	if err := service.DeliverNotification(ctx, notificationsvc.DeliverNotificationParams{
		OrgSlug:         orgSlug,
		RecordSlug:      recordSlug,
		DestinationSlug: destinationSlug,
		AlertPolicySlug: policySlug,
	}); err != nil {
		return "", xerror.Wrapf("seed alert delivery attempt: %w", err)
	}

	return recordSlug, nil
}

// Seed seeds a small, deterministic set of data using service-layer APIs.
func Seed(ctx context.Context, cfg *backendconfig.Config, coreOnly bool) (*Summary, error) {
	pool, err := pgxpool.New(ctx, cfg.Database.ConnectionString())
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	defer pool.Close()

	txm := postgres.NewTransactionManager(pool)

	hasher := crypto.NewArgon2WithParams(
		cfg.Security.Argon2Memory,
		cfg.Security.Argon2Iterations,
		cfg.Security.Argon2Parallelism,
		cfg.Security.Argon2SaltLength,
		cfg.Security.Argon2KeyLength,
	)

	usersRepo := users.NewRepository()
	orgsRepo := organizations.NewRepository()
	membersRepo := members.NewRepository()
	memberInvitationsRepo := memberinvitations.NewRepository()
	rolesRepo := roles.NewRepository()
	permissionsRepo := permissions.NewRepository()
	apiKeysRepo := apikeys.NewRepository()
	datasourcesRepo := datasources.New()
	schemaRepo := datasources.NewSchemaMetadataRepository()
	queriesRepo := queries.New()
	pipelineRepo := pipelinesrepo.New()
	projectsRepo := projects.NewRepository()
	environmentsRepo := environments.NewRepository()
	dataSourceRefsRepo := datasourcerefs.NewRepository()
	eventsService := eventssvc.New(txm, eventrepo.New(), clock.New())
	eventProducer := fakeEventProducer{events: eventsService}

	rolesService := rolessvc.New(txm, rolesRepo, permissionsRepo, eventProducer)
	apiKeysService := apikeyssvc.New(txm, apiKeysRepo, rolesService, eventProducer)
	emailService := newCaptureEmailService()
	usersService := userssvc.New(
		txm,
		hasher,
		usersRepo,
		emailService,
		eventProducer,
		cfg.FrontendURL,
	)
	membersService := memberssvc.New(
		txm,
		membersRepo,
		memberInvitationsRepo,
		orgsRepo,
		rolesService,
		usersService,
		emailService,
		eventProducer,
		cfg.FrontendURL,
		clock.New(),
	)
	credentialCipher, err := cfg.Security.CredentialKeyring()
	if err != nil {
		return nil, err
	}
	credentialsService, err := credentialssvc.New(
		txm,
		repocredentials.New(),
		credentialCipher,
		credentialssvc.ProviderRegistry{},
		clock.New(), true)
	if err != nil {
		return nil, xerror.Wrapf("create credentials service: %w", err)
	}
	admissionService := admissionsvc.New(txm, orgsRepo)

	datasourcesService, err := datasourcessvc.New(
		txm,
		datasourcesRepo,
		schemaRepo,
		driftimpactrepo.New(),
		credentialsService,
		datasourcessvc.NewNoopGitHubAppManager(),
		datasourcessvc.NewNoopGitLabWebhookManager(),
		admissionService,
		newNoopConnector,
		eventProducer,
		fakeDataSourceSchemaImportProducer{},
		datasourcessvc.ServiceConfig{CustomerConfigPolicy: datasourcessvc.CustomerConfigPolicy{AllowPrivateDataSourceHosts: true, AllowPlaintextConnectors: true}},
	)
	if err != nil {
		return nil, xerror.Wrapf("create data sources service: %w", err)
	}
	dataSourceRefsService := datasourcerefssvc.New(txm, dataSourceRefsRepo, datasourcesService, eventProducer)
	environmentsService := environmentssvc.New(txm, environmentsRepo, eventProducer)
	orgService := orgsvc.New(
		txm,
		orgsRepo,
		environmentsService,
		membersService,
		rolesService,
		eventProducer,
		admissionService,
	)

	queryRuntimePool, err := connector.NewManager(newNoopConnector, connector.RuntimePoolSettings{})
	if err != nil {
		return nil, xerror.Wrapf("create query connector runtime pool: %w", err)
	}
	defer func() { _ = queryRuntimePool.Close() }()

	queryConnectorExecutor, err := queriessvc.NewLocalConnectorExecutor(queryRuntimePool)
	if err != nil {
		return nil, xerror.Wrapf("create local query connector executor: %w", err)
	}

	queriesService, err := queriessvc.NewWithExecutionLimits(
		txm,
		queriesRepo,
		datasourcesService,
		queryConnectorExecutor,
		credentialsService,
		clock.New(),
		queriessvc.ExecutionLimits{
			QueryTimeout:                      cfg.ExecutionLimits.QueryTimeout.TimeDuration(),
			ConnectorTimeout:                  cfg.ExecutionLimits.ConnectorTimeout.TimeDuration(),
			MaxResultRows:                     cfg.ExecutionLimits.MaxResultRows,
			MaxResponseBytes:                  cfg.ExecutionLimits.MaxResponseBytes,
			ConnectorConcurrencyPerDataSource: cfg.ExecutionLimits.ConnectorConcurrencyPerDataSource,
			HTTPTimeout:                       cfg.ExecutionLimits.HTTPTimeout.TimeDuration(),
			HTTPMaxRedirects:                  cfg.ExecutionLimits.HTTPMaxRedirects,
			HTTPMaxRequestBytes:               cfg.ExecutionLimits.HTTPMaxRequestBytes,
			HTTPMaxResponseBytes:              cfg.ExecutionLimits.HTTPMaxResponseBytes,
			HTTPRetryAttempts:                 cfg.ExecutionLimits.HTTPRetryAttempts,
			HTTPRetryBackoff:                  cfg.ExecutionLimits.HTTPRetryBackoff.TimeDuration(),
		},
		eventProducer,
	)
	if err != nil {
		return nil, xerror.Wrapf("create queries service: %w", err)
	}

	pipelineStore, err := pipelinesvc.NewPipelineStore(pool, txm, pipelineRepo, odcscontracts.NewRepository())
	if err != nil {
		return nil, xerror.Wrapf("create pipeline store: %w", err)
	}
	projectsService := projectssvc.New(
		txm,
		projectsRepo,
		orgsRepo,
		environmentsRepo,
		datasourcesService,
		pipelineStore,
		nil,
		nil,
		eventProducer,
	)
	notificationsService := notificationsvc.New(
		txm,
		reponotifications.New(),
		clock.New(),
		seedNotificationDeliveryProducer{},
		emailService,
		notificationsvc.LifecycleEventProducerFunc(func(
			ctx context.Context,
			tx postgres.DBTX,
			orgSlug slug.Slug,
			actor model.EventActor,
			event eventrecorder.Event,
		) {
			eventProducer.EnqueueRecord(ctx, tx, orgSlug, actor, event)
		}),
		notificationsvc.ConnectionProviders{},
		cfg.FrontendURL,
	)
	retentionRepo := retentionrepo.New()

	runAt := time.Now().UTC()
	runID := runAt.Format("20060102-150405") + "-" + strconv.FormatInt(runAt.UnixNano()%1_000_000_000, 10)
	ownerEmail := "fake-owner-" + runID + "@example.com"
	memberEmail := "fake-member-" + runID + "@example.com"

	owner, err := usersService.CreateUser(ctx, model.NewUser{
		FullName: "Fake Owner " + runID,
		Email:    ownerEmail,
		Password: seededUserPassword,
		Role:     model.RoleUser,
		Locale:   cfg.DefaultLanguage,
	})
	if err != nil {
		return nil, xerror.Wrapf("create owner user: %w", err)
	}

	ownerToken, ok := emailService.tokenForEmail(ownerEmail)
	if !ok {
		return nil, xerror.Unexpected().Detail("owner verification token not captured")
	}

	if err := usersService.VerifyEmail(ctx, ownerToken); err != nil {
		return nil, xerror.Wrapf("verify owner user email: %w", err)
	}

	memberUser, err := usersService.CreateUser(ctx, model.NewUser{
		FullName: "Fake Member " + runID,
		Email:    memberEmail,
		Password: seededUserPassword,
		Role:     model.RoleUser,
		Locale:   cfg.DefaultLanguage,
	})
	if err != nil {
		return nil, xerror.Wrapf("create member user: %w", err)
	}

	memberToken, ok := emailService.tokenForEmail(memberEmail)
	if !ok {
		return nil, xerror.Unexpected().Detail("member verification token not captured")
	}

	if err := usersService.VerifyEmail(ctx, memberToken); err != nil {
		return nil, xerror.Wrapf("verify member user email: %w", err)
	}

	ownerActor := model.UserEventActor(owner.Slug)

	emptyOrg, err := orgService.CreateOrganization(ctx, owner.Slug, ownerActor, model.NewOrganization{
		Name: "Fake Empty Dashboard Org " + runID,
	})
	if err != nil {
		return nil, xerror.Wrapf("create empty dashboard organization: %w", err)
	}

	org, err := orgService.CreateOrganization(ctx, owner.Slug, ownerActor, model.NewOrganization{
		Name: "Fake Org " + runID,
	})
	if err != nil {
		return nil, xerror.Wrapf("create organization: %w", err)
	}

	project, err := projectsService.CreateProject(ctx, org.Slug, model.NewProject{
		Name: "Data pipelines",
		Slug: seededExampleProjectSlug,
	}, ownerActor)
	if err != nil {
		return nil, xerror.Wrapf("create project: %w", err)
	}

	apiKeyDescription := "Seeded API key for local CLI testing"
	seededAPIKey, err := apiKeysService.CreateAPIKey(
		ctx,
		org.Slug,
		ownerActor,
		model.NewAPIKey{
			Name:        "Fake CLI " + runID,
			Slug:        slug.Slug("fake-cli"),
			Description: &apiKeyDescription,
			Role:        slug.Slug("admin"),
		},
	)
	if err != nil {
		return nil, xerror.Wrapf("create seeded API key: %w", err)
	}

	viewerRole, err := slug.New("viewer")
	if err != nil {
		return nil, xerror.Wrapf("build viewer role slug: %w", err)
	}

	_, err = membersService.CreateMember(ctx, org.Slug, ownerActor, model.NewMember{
		UserSlug: memberUser.Slug,
		Role:     viewerRole,
	})
	if err != nil {
		return nil, xerror.Wrapf("create organization member: %w", err)
	}

	if coreOnly {
		return &Summary{
			RunID: runID, OwnerUserSlug: owner.Slug.String(), MemberUserSlug: memberUser.Slug.String(),
			OwnerUserEmail: ownerEmail, MemberUserEmail: memberEmail, UserPassword: seededUserPassword,
			OrganizationSlug: org.Slug.String(), EmptyOrganizationSlug: emptyOrg.Slug.String(),
			ProjectSlug: project.Slug.String(), EnvironmentSlug: model.DefaultDevelopmentEnvironmentSlug.String(),
			APIKeySlug: seededAPIKey.Slug.String(), APIKeyToken: seededAPIKey.Token,
		}, nil
	}

	odcsCatalog := odcscontractsvc.New(txm, odcscontracts.NewRepository(), projectsRepo, compiledplans.NewRepository())
	if err := seedODCSCatalog(ctx, odcsCatalog, org.Slug, project.Slug); err != nil {
		return nil, err
	}

	configJSON, err := json.Marshal(map[string]any{
		"base_url": fmt.Sprintf(
			"http://localhost:%d",
			depsports.HTTPConnectorPort(),
		),
		"health_path":         "/health",
		"timeout_seconds":     5,
		"allow_private_hosts": true,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	ds, err := datasourcesService.Create(ctx, org.Slug, ownerActor, &datasourcessvc.CreateParams{
		Name:       "Fake HTTP " + runID,
		Type:       model.DataSourceTypeHTTP,
		ConfigJSON: configJSON,
		Credentials: map[string]any{
			"bearer_token": "fake-token",
		},
	})
	if err != nil {
		return nil, xerror.Wrapf("create data source: %w", err)
	}

	httpStatementJSON, err := json.Marshal(map[string]any{
		"method": "GET",
		"path":   "/items",
		"type":   "http",
		"query_params": map[string]any{
			"status": "{{status}}",
		},
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	queryData, err := json.Marshal(map[string]any{
		"statement":       string(httpStatementJSON),
		"timeout_seconds": 30,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	httpStatusDefault := "pending"

	q, err := queriesService.Create(ctx, org.Slug, ds.Slug, ownerActor, &queriessvc.CreateParams{
		Name: "Fake Query " + runID,
		Data: queryData,
		Parameters: []*queriessvc.QueryParameter{
			{
				Name:         "status",
				DataType:     queriessvc.ParameterDataTypeText,
				IsRequired:   false,
				DefaultValue: &httpStatusDefault,
			},
		},
	})
	if err != nil {
		return nil, xerror.Wrapf("create query: %w", err)
	}

	sqlConfigJSON, err := json.Marshal(map[string]any{
		"host":                "localhost",
		"port":                depsports.PostgresConnectorPort(),
		"database":            "braidplane",
		"ssl_mode":            "disable",
		"allow_private_hosts": true,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	sqlDS, err := datasourcesService.Create(ctx, org.Slug, ownerActor, &datasourcessvc.CreateParams{
		Name:       "Fake Postgres " + runID,
		Slug:       seededExampleSQLDataSourceSlug,
		Type:       model.DataSourceTypePostgres,
		ConfigJSON: sqlConfigJSON,
		Credentials: map[string]any{
			"username": "postgres",
			"password": "postgres",
		},
	})
	if err != nil {
		return nil, xerror.Wrapf("create sql data source: %w", err)
	}

	sqlStatementJSON, err := json.Marshal(map[string]any{
		"type": "postgres",
		"sql":  "SELECT id, email, full_name FROM users WHERE {{is_active}} IN (true, false) ORDER BY id LIMIT {{limit}}",
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	sqlQueryData, err := json.Marshal(map[string]any{
		"statement":       string(sqlStatementJSON),
		"timeout_seconds": 30,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	sqlLimitDefault := "100"
	sqlIsActiveDefault := "true"

	sqlQ, err := queriesService.Create(ctx, org.Slug, sqlDS.Slug, ownerActor, &queriessvc.CreateParams{
		Name: "Fake SQL Query " + runID,
		Slug: seededExampleSQLQuerySlug,
		Data: sqlQueryData,
		Parameters: []*queriessvc.QueryParameter{
			{
				Name:         "is_active",
				DataType:     queriessvc.ParameterDataTypeBoolean,
				IsRequired:   false,
				DefaultValue: &sqlIsActiveDefault,
			},
			{
				Name:         "limit",
				DataType:     queriessvc.ParameterDataTypeInteger,
				IsRequired:   false,
				DefaultValue: &sqlLimitDefault,
			},
		},
	})
	if err != nil {
		return nil, xerror.Wrapf("create sql query: %w", err)
	}

	mySQLConfigJSON, err := json.Marshal(map[string]any{
		"host":                "localhost",
		"port":                depsports.MySQLConnectorPort(),
		"database":            "braidplane",
		"allow_private_hosts": true,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	mySQLDS, err := datasourcesService.Create(ctx, org.Slug, ownerActor, &datasourcessvc.CreateParams{
		Name:       "Fake MySQL " + runID,
		Slug:       seededExampleMySQLDataSourceSlug,
		Type:       model.DataSourceTypeMySQL,
		ConfigJSON: mySQLConfigJSON,
		Credentials: map[string]any{
			"username": "braidplane",
			"password": "braidplanepass",
		},
	})
	if err != nil {
		return nil, xerror.Wrapf("create mysql data source: %w", err)
	}

	mySQLStatementJSON, err := json.Marshal(map[string]any{
		"type": "mysql",
		"sql":  "SELECT id, email, full_name FROM users WHERE id > {{min_id}} ORDER BY id LIMIT 10",
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	mySQLQueryData, err := json.Marshal(map[string]any{
		"statement":       string(mySQLStatementJSON),
		"timeout_seconds": 30,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	mySQLMinIDDefault := "0"

	mySQLQ, err := queriesService.Create(ctx, org.Slug, mySQLDS.Slug, ownerActor, &queriessvc.CreateParams{
		Name: "Fake MySQL Query " + runID,
		Data: mySQLQueryData,
		Parameters: []*queriessvc.QueryParameter{
			{
				Name:         "min_id",
				DataType:     queriessvc.ParameterDataTypeInteger,
				IsRequired:   false,
				DefaultValue: &mySQLMinIDDefault,
			},
		},
	})
	if err != nil {
		return nil, xerror.Wrapf("create mysql query: %w", err)
	}

	mySQLSinkStatementJSON, err := json.Marshal(map[string]any{
		"type": "mysql",
		"sql": `INSERT INTO users (id, email, full_name)
SELECT id, email, full_name
FROM input
ON DUPLICATE KEY UPDATE
    email = VALUES(email),
    full_name = VALUES(full_name)`,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	mySQLSinkQueryData, err := json.Marshal(map[string]any{
		"statement":       string(mySQLSinkStatementJSON),
		"timeout_seconds": 30,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	mySQLSinkQ, err := queriesService.Create(ctx, org.Slug, mySQLDS.Slug, ownerActor, &queriessvc.CreateParams{
		Name: "Fake MySQL Sink Query " + runID,
		Slug: seededExampleMySQLSinkQuerySlug,
		Data: mySQLSinkQueryData,
	})
	if err != nil {
		return nil, xerror.Wrapf("create mysql sink query: %w", err)
	}

	seededPipeline, err := loadSeededPipelineSpec()
	if err != nil {
		return nil, err
	}

	mongoConfigJSON, err := json.Marshal(map[string]any{
		"host":                "localhost",
		"port":                depsports.MongoConnectorPort(),
		"database":            "braidplane",
		"auth_database":       "admin",
		"allow_private_hosts": true,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	mongoDS, err := datasourcesService.Create(ctx, org.Slug, ownerActor, &datasourcessvc.CreateParams{
		Name:       "Fake MongoDB " + runID,
		Type:       model.DataSourceTypeMongoDB,
		ConfigJSON: mongoConfigJSON,
		Credentials: map[string]any{
			"username": "root",
			"password": "rootpass",
		},
	})
	if err != nil {
		return nil, xerror.Wrapf("create mongodb data source: %w", err)
	}

	mongoStatementJSON, err := json.Marshal(map[string]any{
		"type":       "mongodb",
		"collection": "orders",
		"operation":  "find",
		"filter": map[string]any{
			"status": "{{status}}",
		},
		"limit": 10,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	mongoQueryData, err := json.Marshal(map[string]any{
		"statement":       string(mongoStatementJSON),
		"timeout_seconds": 30,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	mongoStatusDefault := "pending"

	mongoQ, err := queriesService.Create(ctx, org.Slug, mongoDS.Slug, ownerActor, &queriessvc.CreateParams{
		Name: "Fake Mongo Query " + runID,
		Data: mongoQueryData,
		Parameters: []*queriessvc.QueryParameter{
			{
				Name:         "status",
				DataType:     queriessvc.ParameterDataTypeText,
				IsRequired:   false,
				DefaultValue: &mongoStatusDefault,
			},
		},
	})
	if err != nil {
		return nil, xerror.Wrapf("create mongodb query: %w", err)
	}

	hubSpotConfigJSON, err := json.Marshal(map[string]any{
		"base_url":            depsports.HubSpotMockBaseURL(),
		"timeout_seconds":     5,
		"allow_private_hosts": true,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	hubSpotDS, err := datasourcesService.Create(ctx, org.Slug, ownerActor, &datasourcessvc.CreateParams{
		Name:       "Fake HubSpot " + runID,
		Type:       model.DataSourceTypeHubSpot,
		ConfigJSON: hubSpotConfigJSON,
		Credentials: map[string]any{
			"private_app_token": "pat-na1-test-token",
		},
	})
	if err != nil {
		return nil, xerror.Wrapf("create hubspot data source: %w", err)
	}

	hubSpotStatementJSON, err := json.Marshal(map[string]any{
		"type":        "hubspot",
		"object_type": "contacts",
		"operation":   "list",
		"properties":  []string{"email", "firstname", "lastname"},
		"limit":       100,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	hubSpotQueryData, err := json.Marshal(map[string]any{
		"statement":       string(hubSpotStatementJSON),
		"timeout_seconds": 30,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	hubSpotQ, err := queriesService.Create(ctx, org.Slug, hubSpotDS.Slug, ownerActor, &queriessvc.CreateParams{
		Name: "Fake HubSpot Query " + runID,
		Data: hubSpotQueryData,
	})
	if err != nil {
		return nil, xerror.Wrapf("create hubspot query: %w", err)
	}

	ftpConfigJSON, err := json.Marshal(map[string]any{
		"host":                "localhost",
		"port":                depsports.FTPConnectorPort(),
		"allow_private_hosts": true,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	ftpDS, err := datasourcesService.Create(ctx, org.Slug, ownerActor, &datasourcessvc.CreateParams{
		Name:       "Fake FTP " + runID,
		Type:       model.DataSourceTypeFTP,
		ConfigJSON: ftpConfigJSON,
		Credentials: map[string]any{
			"username": "braidplane",
			"password": "braidplanepass",
		},
	})
	if err != nil {
		return nil, xerror.Wrapf("create ftp data source: %w", err)
	}

	ftpStatementJSON, err := json.Marshal(map[string]any{
		"type":      "ftp",
		"operation": "read",
		"path":      "users.csv",
		"format":    "csv",
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	ftpQueryDataBytes, err := json.Marshal(map[string]any{
		"statement":       string(ftpStatementJSON),
		"timeout_seconds": 30,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	ftpQ, err := queriesService.Create(ctx, org.Slug, ftpDS.Slug, ownerActor, &queriessvc.CreateParams{
		Name: "Fake FTP Query " + runID,
		Data: ftpQueryDataBytes,
	})
	if err != nil {
		return nil, xerror.Wrapf("create ftp query: %w", err)
	}

	privateKeyPath := gitPrivateKeyPath()
	if !filepath.IsAbs(privateKeyPath) {
		privateKeyPath = filepath.Clean(privateKeyPath)
	}

	privateKeyBytes, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	seededGitCommitSHA, err := bootstrapGitFixtureRepo(
		ctx,
		privateKeyPath,
		seededGitRepoName,
		seededGitPipelineStateBase,
	)
	if err != nil {
		return nil, xerror.Wrapf("bootstrap Git fixture repository: %w", err)
	}
	if err := bootstrapGitTagOnlyFixtureRepo(ctx, privateKeyPath, seededGitTagOnlyRepoName); err != nil {
		return nil, xerror.Wrapf("bootstrap tag-only Git fixture repository: %w", err)
	}
	knownHosts, err := softServeKnownHosts(ctx)
	if err != nil {
		return nil, xerror.Wrapf("scan Git fixture host key: %w", err)
	}

	gitConfigJSON, err := json.Marshal(map[string]any{
		"repo_url":            seededGitRepoURL(),
		"default_branch":      "main",
		"allow_private_hosts": true,
	})
	if err != nil {
		return nil, xerror.Unexpected().With(err)
	}

	gitDS, err := datasourcesService.Create(ctx, org.Slug, ownerActor, &datasourcessvc.CreateParams{
		Name:       "Fake Git " + runID,
		Slug:       seededExampleGitDataSourceSlug,
		Type:       model.DataSourceTypeGit,
		ConfigJSON: gitConfigJSON,
		Credentials: map[string]any{
			"auth_type":       "ssh",
			"ssh_private_key": string(privateKeyBytes),
			"ssh_known_hosts": knownHosts,
		},
	})
	if err != nil {
		return nil, xerror.Wrapf("create git data source: %w", err)
	}
	project, err = projectsService.SetProjectPipelineBackend(
		ctx,
		org.Slug,
		project.Slug,
		model.SetProjectPipelineBackend{
			DataSourceSlug: gitDS.Slug,
			TrackedBranch:  "main",
			PipelineDir:    "pipelines",
			Mode:           model.SetProjectPipelineBackendModeAdoptExisting,
		},
	)
	if err != nil {
		return nil, xerror.Wrapf("configure seeded project pipeline backend: %w", err)
	}
	if project.PipelineBackend == nil {
		return nil, xerror.Unexpected().Detail("configured seeded project has no pipeline backend")
	}

	gitQueryDataBytes, err := seededGitQueryData()
	if err != nil {
		return nil, err
	}

	gitQ, err := queriesService.Create(ctx, org.Slug, gitDS.Slug, ownerActor, &queriessvc.CreateParams{
		Name: "Fake Git Query " + runID,
		Slug: seededExampleGitQuerySlug,
		Data: gitQueryDataBytes,
	})
	if err != nil {
		return nil, xerror.Wrapf("create git query: %w", err)
	}

	environmentSlug := model.DefaultDevelopmentEnvironmentSlug
	bindings := []seededDataSourceBinding{
		{dataSource: sqlDS, capabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource}},
		{dataSource: mySQLDS, capabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource, model.DataSourceRefCapabilityPipelineSink}},
		{dataSource: ds, capabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource, model.DataSourceRefCapabilityPipelineSink}},
		{dataSource: mongoDS, capabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource, model.DataSourceRefCapabilityPipelineSink}},
		{dataSource: hubSpotDS, capabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource}},
		{dataSource: ftpDS, capabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource, model.DataSourceRefCapabilityPipelineSink}},
		{dataSource: gitDS, capabilities: []model.DataSourceRefCapability{model.DataSourceRefCapabilityPipelineSource, model.DataSourceRefCapabilityPipelineSink}},
	}
	for _, binding := range bindings {
		if err := seedDataSourceBinding(
			ctx,
			dataSourceRefsService,
			org.Slug,
			project.Slug,
			seededPipeline.Metadata.Slug,
			environmentSlug,
			binding,
			ownerActor,
		); err != nil {
			return nil, err
		}
	}
	if err := seedPipelineDeployment(
		ctx,
		txm,
		pipelineStore,
		org.Slug,
		project.Slug,
		environmentSlug,
		seededPipeline.Metadata.Slug,
		ownerActor,
	); err != nil {
		return nil, err
	}
	triggerService := pipelinetriggersvc.New(false, txm, pipelinetriggers.NewRepository(), seedCompletionValidator{}, eventProducer, nil)
	for _, schedule := range []struct{ name, slug, cron string }{
		{"Daily pipeline sync", "daily-pipeline-sync", "0 9 * * *"},
		{"Weekly pipeline sync", "weekly-pipeline-sync", "0 9 * * 1"},
	} {
		_, err := triggerService.CreatePipelineTrigger(ctx, org.Slug, project.Slug, environmentSlug, model.NewPipelineTrigger{
			Name: schedule.name, Slug: slug.Slug(schedule.slug),
			Type:           model.PipelineTriggerTypeSchedule,
			PipelineSlug:   seededPipeline.Metadata.Slug,
			VersionPolicy:  model.PipelineTriggerVersionPolicy{Mode: model.PipelineTriggerVersionPolicyModeLatestDeployed},
			RetryPolicy:    model.PipelineTriggerRetryPolicy{MaxAttempts: 1, BackoffStrategy: model.PipelineTriggerBackoffStrategyNone},
			ScheduleConfig: &model.PipelineTriggerScheduleConfig{Cron: schedule.cron, Timezone: "UTC", MisfirePolicy: model.PipelineTriggerScheduleMisfirePolicySkip},
		}, ownerActor)
		if err != nil {
			return nil, xerror.Wrapf("seed pipeline schedule: %w", err)
		}
	}
	notificationRecordSlug, err := seedNotificationDiagnostics(
		ctx,
		notificationsService,
		org.Slug,
		owner.Slug,
		runID,
		seededPipeline.Metadata.Slug,
	)
	if err != nil {
		return nil, err
	}
	retentionRunSlug, err := seedRetentionMaintenanceRun(ctx, txm, retentionRepo, org.Slug, runID, runAt)
	if err != nil {
		return nil, err
	}

	return &Summary{
		RunID:                  runID,
		OwnerUserSlug:          owner.Slug.String(),
		MemberUserSlug:         memberUser.Slug.String(),
		OwnerUserEmail:         ownerEmail,
		MemberUserEmail:        memberEmail,
		UserPassword:           seededUserPassword,
		OrganizationSlug:       org.Slug.String(),
		EmptyOrganizationSlug:  emptyOrg.Slug.String(),
		ProjectSlug:            project.Slug.String(),
		EnvironmentSlug:        environmentSlug.String(),
		APIKeySlug:             seededAPIKey.Slug.String(),
		APIKeyToken:            seededAPIKey.Token,
		HTTPDataSourceSlug:     ds.Slug.String(),
		HTTPQuerySlug:          q.Slug.String(),
		DataSourceSlug:         ds.Slug.String(),
		QuerySlug:              q.Slug.String(),
		SQLDataSourceSlug:      sqlDS.Slug.String(),
		SQLQuerySlug:           sqlQ.Slug.String(),
		MySQLDataSourceSlug:    mySQLDS.Slug.String(),
		MySQLQuerySlug:         mySQLQ.Slug.String(),
		MySQLSinkQuerySlug:     mySQLSinkQ.Slug.String(),
		MongoDataSourceSlug:    mongoDS.Slug.String(),
		MongoQuerySlug:         mongoQ.Slug.String(),
		HubSpotDataSourceSlug:  hubSpotDS.Slug.String(),
		HubSpotQuerySlug:       hubSpotQ.Slug.String(),
		FTPDataSourceSlug:      ftpDS.Slug.String(),
		FTPQuerySlug:           ftpQ.Slug.String(),
		GitDataSourceSlug:      gitDS.Slug.String(),
		GitQuerySlug:           gitQ.Slug.String(),
		SeededGitCommitSHA:     seededGitCommitSHA,
		PipelineSlug:           seededPipeline.Metadata.Slug.String(),
		NotificationRecordSlug: notificationRecordSlug.String(),
		RetentionRunSlug:       retentionRunSlug.String(),
	}, nil
}

func seedODCSCatalog(ctx context.Context, catalog odcscontractsvc.Service, org, project slug.Slug) error {
	for _, contract := range seededODCSContracts {
		if _, err := catalog.CreateContract(ctx, org, project, model.NewODCSContract{
			Slug: contract.slug, Name: contract.name, ODCSID: contract.odcsID,
		}); err != nil {
			return xerror.Wrapf("seed ODCS contract %s: %w", contract.slug, err)
		}
		if _, err := catalog.CreateRevision(ctx, org, project, contract.slug, contract.revision, contract.document); err != nil {
			return xerror.Wrapf("seed ODCS revision %s/%s: %w", contract.slug, contract.revision, err)
		}
	}
	return nil
}

func seedRetentionMaintenanceRun(
	ctx context.Context,
	txm postgres.TransactionManager,
	repo retentionrepo.Repository,
	orgSlug slug.Slug,
	runID string,
	runAt time.Time,
) (slug.Slug, error) {
	runSlug, err := slug.New("retention-run-seed-" + runID)
	if err != nil {
		return "", xerror.Wrapf("create fake retention run slug: %w", err)
	}
	policy := model.DefaultOrganizationRetentionPolicy()
	err = txm.RunInTx(ctx, func(ctx context.Context, tx postgres.DBTX) error {
		return repo.CreateRun(ctx, tx, orgSlug, runAt.UnixNano(), model.OrganizationRetentionRun{
			Slug:                     runSlug,
			Attempt:                  2,
			Status:                   model.OrganizationRetentionRunStatusSucceeded,
			PolicySnapshot:           &policy,
			QueryExecutionsDeleted:   4,
			ReplayCheckpointsDeleted: 3,
			PipelineRunsDeleted:      2,
			EventsDeleted:            1,
			StartedAt:                runAt.Add(-2 * time.Minute),
			CompletedAt:              runAt.Add(-time.Minute),
		})
	})
	if err != nil {
		return "", xerror.Wrapf("seed retention maintenance run: %w", err)
	}
	return runSlug, nil
}

type seedCompletionValidator struct{}

func (seedCompletionValidator) ValidateCompletionExecutionContext(context.Context, slug.Slug, slug.Slug, slug.Slug, *model.PipelineCompletionSnapshot) error {
	return xerror.ValidationFailed().Detail("completion triggers are disabled in the standard fixture seed")
}

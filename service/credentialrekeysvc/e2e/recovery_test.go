package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/crypto"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/testutil"
	"github.com/Braidplane/braidplane-core/pkg/testutil/e2edb"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/credentialrekey"
	repocredentials "github.com/Braidplane/braidplane-core/postgres/credentials"
	"github.com/Braidplane/braidplane-core/service/credentialssvc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// This is a logical restore to a fresh database on the same disposable server,
// not physical host recovery or release qualification.
func TestSyntheticBackupRecoversAfterRuntimeConfigLoss(t *testing.T) {
	if testing.Short() {
		t.Skip("requires disposable PostgreSQL and matching pg_dump/pg_restore clients")
	}
	started := time.Now()
	for _, name := range []string{"pg_dump", "pg_restore"} {
		_, err := exec.LookPath(name)
		require.NoError(t, err, "install PostgreSQL clients compatible with the disposable fixture server")
	}
	runtimePath := filepath.Join(t.TempDir(), "runtime.keys")
	recoveryPath := filepath.Join(t.TempDir(), "independent-recovery.keys")
	writeSyntheticRecoveryInputs(t, runtimePath, recoveryPath)
	source := e2edb.OpenPostgresPool(t, "synthetic backup source")
	archive := filepath.Join(t.TempDir(), "synthetic.dump")
	org, schemaVersion, schemaDirty := backupSyntheticRuntime(t, source, runtimePath, archive)
	digest, archiveBytes := fileDigest(t, archive)
	require.Positive(t, archiveBytes)
	info, err := os.Stat(archive)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	// The source service and cipher have gone out of scope. Only the separately
	// protected recovery file supplies keys from this point onward.
	require.NoError(t, os.Remove(runtimePath))
	_, err = os.Stat(runtimePath)
	require.True(t, os.IsNotExist(err))
	adminConfig := source.Config().Copy()
	adminConfig.ConnConfig.Database = "postgres"
	admin, err := pgxpool.NewWithConfig(t.Context(), adminConfig)
	require.NoError(t, err)
	t.Cleanup(admin.Close)
	database := fmt.Sprintf("credential_recovery_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{database}.Sanitize()
	_, err = admin.Exec(t.Context(), "CREATE DATABASE "+identifier+" TEMPLATE template0")
	require.NoError(t, err)
	var restored *pgxpool.Pool
	t.Cleanup(func() {
		if restored != nil {
			restored.Close()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := admin.Exec(ctx, "DROP DATABASE "+identifier)
		require.NoError(t, err)
	})
	restoredConfig := source.Config().Copy()
	restoredConfig.ConnConfig.Database = database
	restored, err = pgxpool.NewWithConfig(t.Context(), restoredConfig)
	require.NoError(t, err)
	var tables int
	require.NoError(t, restored.QueryRow(t.Context(), `SELECT count(*) FROM information_schema.tables WHERE table_schema='public'`).Scan(&tables))
	require.Zero(t, tables)
	verifiedDigest, verifiedBytes := fileDigest(t, archive)
	require.Equal(t, digest, verifiedDigest)
	require.Equal(t, archiveBytes, verifiedBytes)
	runPGClient(t, "pg_restore", restoredConfig.ConnConfig, "--no-owner", "--no-privileges", "--exit-on-error", archive)
	var restoredVersion int64
	var restoredDirty bool
	require.NoError(t, restored.QueryRow(t.Context(), `SELECT version,dirty FROM core_schema_migrations`).Scan(&restoredVersion, &restoredDirty))
	require.Equal(t, schemaVersion, restoredVersion)
	require.Equal(t, schemaDirty, restoredDirty)
	require.False(t, restoredDirty)
	before := ciphertextDigest(t, restored)
	fixture := rekeyFixture{pool: restored, org: org}

	// Neither an unrelated key ID nor incorrect retained material may expose
	// plaintext or mutate the restored records through real resolution paths.
	missing, err := crypto.NewCredentialKeyring("unrelated", map[string][]byte{"unrelated": bytes.Repeat([]byte{7}, 32)}, nil)
	require.NoError(t, err)
	wrong, err := crypto.NewCredentialKeyring("old", map[string][]byte{"old": bytes.Repeat([]byte{8}, 32)}, bytes.Repeat([]byte{9}, 32))
	require.NoError(t, err)
	for _, failure := range []struct {
		ring *crypto.CredentialKeyring
		err  error
	}{{missing, crypto.ErrCredentialKeyUnavailable}, {wrong, crypto.ErrCredentialCiphertext}} {
		svc, err := credentialssvc.New(postgres.NewTransactionManager(restored), repocredentials.New(), failure.ring, nil, testutil.FakeClock{NowFn: time.Now}, false)
		require.NoError(t, err)
		for _, owner := range []credentialssvc.CredentialOwner{
			{OrgSlug: org, DataSourceSlug: "connector"},
			{Kind: credentialssvc.CredentialOwnerKindEnvironmentSecret, OrgSlug: org, EnvironmentSlug: "prod", EnvironmentSecretKey: "API_TOKEN"},
			{OrgSlug: org, DataSourceSlug: "oauth"},
		} {
			got, err := svc.ResolveRuntimeCredential(t.Context(), credentialssvc.ResolveRuntimeCredentialParams{Owner: owner})
			require.ErrorIs(t, err, failure.err)
			require.Empty(t, got.PlaintextJSON)
		}
		require.Equal(t, before, ciphertextDigest(t, restored))
	}
	recovered := readSyntheticRecoveryRing(t, recoveryPath)
	require.Equal(t, "old", recovered.CurrentKeyID())
	fixture.assertResolution(t, recovered)
	require.Equal(t, before, ciphertextDigest(t, restored))
	executable, err := os.Executable()
	require.NoError(t, err)
	buildDigest, _ := fileDigest(t, executable)
	t.Logf("synthetic logical recovery: archive_sha256=%x bytes=%d schema_version=%d dirty=%t key_id=%s elapsed=%s outcome=passed scope=fresh_database_same_server synthetic_test_build_sha256=%x; not release qualification", digest, archiveBytes, restoredVersion, restoredDirty, recovered.CurrentKeyID(), time.Since(started), buildDigest)
}

// Each file stores two synthetic 32-byte keys: old product key, then legacy key.
// This fixture format is local to the test; it is not a production config format.
func writeSyntheticRecoveryInputs(t *testing.T, runtimePath, recoveryPath string) {
	t.Helper()
	material := make([]byte, 64)
	defer clear(material)
	_, err := rand.Read(material)
	require.NoError(t, err)
	for _, path := range []string{recoveryPath, runtimePath} {
		require.NoError(t, os.WriteFile(path, material, 0o600))
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
}

func readSyntheticRecoveryRing(t *testing.T, path string) *crypto.CredentialKeyring {
	t.Helper()
	material, err := os.ReadFile(path)
	require.NoError(t, err)
	defer clear(material)
	require.Len(t, material, 64)
	ring, err := crypto.NewCredentialKeyring("old", map[string][]byte{"old": material[:32]}, material[32:])
	require.NoError(t, err)
	return ring
}

func backupSyntheticRuntime(t *testing.T, source *pgxpool.Pool, runtimePath, archive string) (slug.Slug, int64, bool) {
	t.Helper()
	material, err := os.ReadFile(runtimePath)
	require.NoError(t, err)
	defer clear(material)
	require.Len(t, material, 64)
	ring, err := crypto.NewCredentialKeyring("old", map[string][]byte{"old": material[:32]}, material[32:])
	require.NoError(t, err)
	fixture := seedRekeyFixture(t, source, ring, material[32:])
	fixture.assertResolution(t, ring)
	var version int64
	var dirty bool
	require.NoError(t, source.QueryRow(t.Context(), `SELECT version,dirty FROM core_schema_migrations`).Scan(&version, &dirty))
	require.False(t, dirty)
	file, err := os.OpenFile(archive, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	require.NoError(t, err)
	require.NoError(t, file.Close())
	runPGClient(t, "pg_dump", source.Config().ConnConfig, "--format=custom", "--no-owner", "--no-privileges", "--file", archive)
	return fixture.org, version, dirty
}

func runPGClient(t *testing.T, name string, config *pgx.ConnConfig, extra ...string) {
	t.Helper()
	// The fixture server uses local plaintext transport; its password is supplied
	// only in the child environment, never arguments or retained subprocess output.
	require.Nil(t, config.TLSConfig, "this drill only supports the disposable local PostgreSQL fixture")
	args := []string{"--host", config.Host, "--port", strconv.Itoa(int(config.Port)), "--username", config.User, "--dbname", config.Database}
	args = append(args, extra...)
	command := exec.CommandContext(t.Context(), name, args...)
	command.Env = append(os.Environ(), "PGPASSWORD="+config.Password, "PGSSLMODE=disable")
	command.Stdout, command.Stderr = io.Discard, io.Discard
	require.NoError(t, command.Run(), "%s failed; verify installed clients match the disposable fixture PostgreSQL version (subprocess output suppressed)", name)
}

func fileDigest(t *testing.T, path string) ([sha256.Size]byte, int64) {
	t.Helper()
	file, err := os.Open(path)
	require.NoError(t, err)
	defer func() { require.NoError(t, file.Close()) }()
	hash := sha256.New()
	length, err := io.Copy(hash, file)
	require.NoError(t, err)
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, length
}

func ciphertextDigest(t *testing.T, pool *pgxpool.Pool) [sha256.Size]byte {
	t.Helper()
	records, err := credentialrekey.New().List(t.Context(), pool, credentialrekey.Cursor{}, 100)
	require.NoError(t, err)
	require.Len(t, records, 8)
	encoded, err := json.Marshal(records)
	require.NoError(t, err)
	defer clear(encoded)
	return sha256.Sum256(encoded)
}

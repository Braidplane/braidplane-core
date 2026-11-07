package postgres

import (
	"cmp"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"path"
	"slices"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// EmbeddedMigrationCatalogIdentity identifies the catalog compiled into this binary.
func EmbeddedMigrationCatalogIdentity() (uint, string, error) {
	return MigrationCatalogIdentity(embeddedMigrationFiles, "migrations")
}

// MigrationCatalogIdentity matches the raw-byte catalog identity used by release manifests.
func MigrationCatalogIdentity(files fs.FS, directory string) (uint, string, error) {
	if files == nil {
		return 0, "", xerror.Unexpected().Detail("migration catalog filesystem is required")
	}
	names, err := fs.Glob(files, path.Join(directory, "*.up.sql"))
	if err != nil {
		return 0, "", xerror.Wrapf("list migration catalog: %w", err)
	}
	if len(names) == 0 {
		return 0, "", xerror.Unexpected().Detail("at least one up migration is required")
	}
	type entry struct {
		version uint64
		name    string
	}
	entries := make([]entry, 0, len(names))
	versions := make(map[uint64]bool, len(names))
	for _, name := range names {
		version, err := migrationVersion(name)
		if err != nil {
			return 0, "", err
		}
		if version == 0 || versions[version] {
			return 0, "", xerror.Unexpected().Detail("migration catalog versions must be positive and unique")
		}
		versions[version] = true
		entries = append(entries, entry{version: version, name: name})
	}
	slices.SortFunc(entries, func(a, b entry) int { return cmp.Compare(a.version, b.version) })
	var catalog []byte
	for _, entry := range entries {
		body, err := fs.ReadFile(files, entry.name)
		if err != nil {
			return 0, "", xerror.Wrapf("read migration catalog file: %w", err)
		}
		catalog = fmt.Appendf(catalog, "%s\t%x\n", path.Base(entry.name), sha256.Sum256(body))
	}
	return uint(entries[len(entries)-1].version), fmt.Sprintf("%x", sha256.Sum256(catalog)), nil
}

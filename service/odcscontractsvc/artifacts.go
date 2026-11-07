package odcscontractsvc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
	"github.com/Braidplane/braidplane-core/postgres/odcscontracts"
	"github.com/Braidplane/braidplane-core/postgres/projects"
	"github.com/Braidplane/braidplane-core/service/pipelinegit"
)

var (
	ErrArtifact       = xerror.New(xerror.KindInvalidInput, "ODCS_ARTIFACT_INVALID", "invalid ODCS artifact import")
	ErrImportConflict = xerror.New(xerror.KindAlreadyExists, "ODCS_IMPORT_CONFLICT", "ODCS import conflicts with catalog or repository state")
	fullCommit        = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// ArtifactImporter reads only the configured project Git backend; callers enforce project permissions.
type ArtifactImporter struct {
	txm      postgres.TransactionManager
	repo     odcscontracts.Repository
	projects projects.Repository
	source   pipelinegit.SourceResolver
}

func NewArtifactImporter(txm postgres.TransactionManager, repo odcscontracts.Repository, projects projects.Repository, source pipelinegit.SourceResolver) *ArtifactImporter {
	if txm == nil || repo == nil || projects == nil || source == nil {
		panic("ODCS artifact importer requires all dependencies")
	}
	return &ArtifactImporter{txm: txm, repo: repo, projects: projects, source: source}
}

type admittedArtifact struct {
	artifact model.ODCSArtifact
	id       string
	name     string
	revision *model.ODCSRevision
}

func digestBytes(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

// Bound the indexed text values below PostgreSQL's B-tree key-size limit.
const maxArtifactLocationBytes = 1024

func validateArtifactKey(repo, commit, filePath string) error {
	if len(repo) == 0 || len(repo) > maxArtifactLocationBytes || !utf8.ValidString(repo) || strings.ContainsRune(repo, 0) {
		return ErrArtifact.WithPublicDetail("source repository must be valid UTF-8 text of 1 to 1024 bytes without NUL")
	}
	if !fullCommit.MatchString(commit) {
		return ErrArtifact.WithPublicDetail("artifact commit must be a full lowercase SHA-1 hash")
	}
	if len(filePath) > maxArtifactLocationBytes || !utf8.ValidString(filePath) || strings.ContainsRune(filePath, 0) || path.Clean(filePath) != filePath || !strings.HasPrefix(filePath, "contracts/") || strings.Contains(filePath, "\\") {
		return ErrArtifact.WithPublicDetail("artifact path must be valid repository-relative text inside contracts/, at most 1024 bytes, without NUL or backslashes")
	}
	return nil
}

func (s *ArtifactImporter) Import(ctx context.Context, org, project slug.Slug, apply bool, expectedCommit string) (*model.ODCSArtifactImport, error) {
	if apply && !fullCommit.MatchString(expectedCommit) {
		return nil, ErrArtifact.WithPublicDetail("apply requires the exact full commit from preview")
	}
	source, err := s.source.Resolve(ctx, org, pipelinegit.SourceRequest{ProjectSlug: project})
	if err != nil {
		return nil, err
	}
	if source.Cleanup != nil {
		defer source.Cleanup()
	}
	if source.SourceRepo == nil || source.LastAppliedCommit == nil || !fullCommit.MatchString(*source.LastAppliedCommit) {
		return nil, ErrArtifact.WithPublicDetail("configured Git source must supply immutable commit provenance")
	}
	commit := *source.LastAppliedCommit
	if expectedCommit != "" && expectedCommit != commit {
		return nil, ErrImportConflict.WithPublicDetail("Git commit changed; preview the current commit before applying")
	}
	entries, err := source.RepoStore.ListFiles(ctx, "contracts")
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	admitted := make([]admittedArtifact, 0)
	ids := map[string]bool{}
	total := 0
	for _, entry := range entries {
		ext := strings.ToLower(path.Ext(entry.Path))
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			continue
		}
		if err := validateArtifactKey(*source.SourceRepo, commit, entry.Path); err != nil {
			return nil, err
		}
		if len(admitted) >= 128 {
			return nil, ErrArtifact.WithPublicDetail("import exceeds 128 artifacts")
		}
		raw, err := source.RepoStore.ReadFile(ctx, entry.Path, odcs.MaxDocumentBytes)
		if err != nil {
			return nil, err
		}
		total += len(raw)
		if total > 8<<20 {
			return nil, ErrArtifact.WithPublicDetail("import exceeds 8 MiB")
		}
		format := "yaml"
		if ext == ".json" {
			format = "json"
		}
		document, err := odcs.ParseArtifact(raw, format)
		if err != nil {
			return nil, err
		}
		var identity struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(document, &identity); err != nil {
			return nil, ErrArtifact.WithPublicDetail("artifact must be an ODCS object with a string id")
		}
		if strings.TrimSpace(identity.ID) == "" || len(identity.ID) > 255 {
			return nil, ErrArtifact.WithPublicDetail("artifact id must contain 1 to 255 bytes")
		}
		revision, err := odcs.Record(document, identity.ID)
		if err != nil {
			return nil, err
		}
		if ids[identity.ID] {
			return nil, ErrImportConflict.WithPublicDetail("multiple artifacts declare the same ODCS id; retain one file per identity")
		}
		ids[identity.ID] = true
		name := identity.Name
		if name == "" {
			name = identity.ID
		}
		if len(name) > 255 {
			return nil, ErrArtifact.WithPublicDetail("contract name exceeds 255 bytes")
		}
		artifact := model.ODCSArtifact{ContractSlug: slug.Slug("odcs-" + digestBytes([]byte(identity.ID))), RevisionSlug: slug.Slug("sha256-" + strings.TrimPrefix(revision.Digest, "sha256:")), Path: entry.Path, Format: format, Digest: revision.Digest, ArtifactDigest: "sha256:" + digestBytes(raw), Original: raw, SourceRepo: *source.SourceRepo, Commit: commit}
		admitted = append(admitted, admittedArtifact{artifact: artifact, id: identity.ID, name: name, revision: revision})
	}
	result := &model.ODCSArtifactImport{Commit: commit, Applied: apply, Artifacts: make([]model.ODCSArtifact, 0, len(admitted))}
	run := s.txm.RunRead
	if apply {
		run = s.txm.RunInTx
	}
	err = run(ctx, func(ctx context.Context, db postgres.DBTX) error {
		// Serialize catalog creation and verify the backend has not changed while Git was read.
		var p *projects.Project
		var err error
		if apply {
			p, err = s.projects.FindBySlugForUpdate(ctx, db, org, project)
		} else {
			p, err = s.projects.FindBySlug(ctx, db, org, project)
		}
		if err != nil {
			return err
		}
		if p.PipelineBackendTrackedBranch == nil || source.SourceRef == nil || *source.SourceRef != "refs/heads/"+*p.PipelineBackendTrackedBranch {
			return ErrImportConflict.WithPublicDetail("project Git branch changed; preview again")
		}
		if p.PipelineBackendRepo == nil || *p.PipelineBackendRepo != *source.SourceRepo || p.PipelineBackendDataSourceSlug == nil || *p.PipelineBackendDataSourceSlug != source.DataSourceSlug {
			return ErrImportConflict.WithPublicDetail("project Git backend changed; preview again")
		}
		for _, item := range admitted {
			a := item.artifact
			contract, err := s.repo.FindContractByIdentity(ctx, db, org, project, item.id)
			if xerror.Is(err, odcscontracts.ErrNotFound) {
				occupied, slotErr := s.repo.GetContract(ctx, db, org, project, a.ContractSlug)
				if slotErr != nil && !xerror.Is(slotErr, odcscontracts.ErrNotFound) {
					return slotErr
				}
				if occupied != nil {
					return ErrImportConflict.With(err).WithPublicDetail("deterministic contract slug is occupied by another identity")
				}
				if apply {
					contract, err = s.repo.CreateContract(ctx, db, org, project, model.NewODCSContract{Slug: a.ContractSlug, Name: item.name, ODCSID: item.id})
				} else {
					contract = &model.ODCSContract{Slug: a.ContractSlug, Status: "active"}
					err = nil
				}
			}
			if err != nil {
				return err
			}
			if apply {
				contract, err = s.repo.LockContract(ctx, db, org, project, contract.Slug)
				if err != nil {
					return err
				}
			}
			if contract.Status == "archived" {
				return ErrArchived.WithPublicDetail("reactivate the contract explicitly before importing")
			}
			a.ContractSlug = contract.Slug
			revision, err := s.repo.FindRevisionByDigest(ctx, db, org, project, contract.Slug, a.Digest)
			if xerror.Is(err, odcscontracts.ErrNotFound) {
				item.revision.Slug = a.RevisionSlug
				if apply {
					revision, err = s.repo.CreateRevision(ctx, db, org, project, contract.Slug, *item.revision)
				} else {
					revision = item.revision
					err = nil
				}
			}
			if err != nil {
				return err
			}
			a.RevisionSlug = revision.Slug

			previous, err := s.repo.FindArtifact(ctx, db, org, project, a)
			if err != nil && !xerror.Is(err, odcscontracts.ErrNotFound) {
				return err
			}
			if previous != nil && (previous.RevisionSlug != a.RevisionSlug || previous.ArtifactDigest != a.ArtifactDigest || !bytes.Equal(previous.Original, a.Original)) {
				return ErrImportConflict.WithPublicDetail("stored artifact disagrees with Git commit; existing provenance is immutable")
			}
			if apply && previous == nil {
				if err := s.repo.CreateArtifact(ctx, db, org, project, a); err != nil {
					return err
				}
			}
			result.Artifacts = append(result.Artifacts, a)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *ArtifactImporter) Get(ctx context.Context, org, project, contract, revisionSlug slug.Slug, repo, commit, filePath string) (*model.ODCSArtifact, error) {
	if err := validateArtifactKey(repo, commit, filePath); err != nil {
		return nil, err
	}
	var result *model.ODCSArtifact
	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		revision, err := s.repo.GetRevision(ctx, db, org, project, contract, revisionSlug)
		if err != nil {
			return err
		}
		result, err = s.repo.FindArtifact(ctx, db, org, project, model.ODCSArtifact{SourceRepo: repo, Commit: commit, Path: filePath})
		if err != nil {
			return err
		}
		if result.ContractSlug != contract || result.RevisionSlug != revisionSlug {
			return odcscontracts.ErrNotFound
		}
		result.Digest = revision.Digest
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

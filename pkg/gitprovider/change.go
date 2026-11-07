package gitprovider

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	maxChangeIDLength     = 255
	maxChangeRefLength    = 255
	maxChangePathLength   = 1024
	MaxChangeWebhookBytes = 2 * 1024 * 1024
)

var (
	ErrChangeAuthentication  = xerror.New(xerror.KindUnauthorized, "GIT_CHANGE_AUTHENTICATION_FAILED", "git change authentication failed")
	ErrChangeActionIgnored   = xerror.New(xerror.KindInvalidInput, "GIT_CHANGE_ACTION_IGNORED", "git change action is not handled")
	ErrChangePayloadTooLarge = xerror.New(xerror.KindInvalidInput, "GIT_CHANGE_PAYLOAD_TOO_LARGE", "git change payload is too large")
	ErrChangeClosed          = xerror.New(xerror.KindInvalidInput, "GIT_CHANGE_CLOSED", "git change is closed")
	ErrChangeStale           = xerror.New(xerror.KindInvalidInput, "GIT_CHANGE_STALE_HEAD", "git change head is stale")
	ErrChangePermissionLost  = xerror.New(xerror.KindForbidden, "GIT_CHANGE_PERMISSION_LOST", "git change permission was lost")
	ErrChangeRefDeleted      = xerror.New(xerror.KindNotFound, "GIT_CHANGE_REF_DELETED", "git change ref was deleted")
	ErrChangePathMissing     = xerror.New(xerror.KindNotFound, "GIT_CHANGE_PATH_MISSING", "git change pipeline path is missing")
	ErrChangeUnavailable     = xerror.New(xerror.KindUnavailable, "GIT_CHANGE_PROVIDER_UNAVAILABLE", "git change provider unavailable")
	ErrChangeUnreachable     = xerror.New(xerror.KindNotFound, "GIT_CHANGE_COMMIT_UNREACHABLE", "git change commit is unreachable")
)

//go:generate -command enumer go tool -modfile=../../tools/go.mod enumer
//go:generate enumer -type=ChangeAction -json -text -linecomment -output=change_action_enumer.go
type ChangeAction int

const (
	ChangeActionUnknown ChangeAction = iota // unknown
	ChangeActionUpsert                      // upsert
	ChangeActionClose                       // close
)

// VerifiedChangeEvent is the authenticated provider signal persisted for
// background processing. GitLab carries its binding organization; pipeline
// paths and GitHub organization ownership are resolved later.
type VerifiedChangeEvent struct {
	DeliveryID           string
	BodySHA256           string
	Action               ChangeAction
	Provider             repostore.Provider
	OrganizationSlug     slug.Slug
	ProviderConnectionID string
	RepositoryID         string
	ChangeRequestID      string
	HeadSHA              string
	SourceRef            string
	TargetRef            string
	Rerequest            bool
}

func (e VerifiedChangeEvent) Change(pipelinePath string) VerifiedChange {
	return VerifiedChange{
		Provider: e.Provider, ProviderConnectionID: e.ProviderConnectionID,
		RepositoryID: e.RepositoryID, ChangeRequestID: e.ChangeRequestID,
		HeadSHA: e.HeadSHA, SourceRef: e.SourceRef, TargetRef: e.TargetRef,
		PipelinePath: strings.TrimSpace(pipelinePath), Rerequest: e.Rerequest,
	}
}

func (e VerifiedChangeEvent) Validate() error {
	if !validDeliveryID(e.DeliveryID) {
		return xerror.InvalidInput(xerror.RepoFile).Detail("git change delivery identity is invalid")
	}
	if len(e.BodySHA256) != sha256.Size*2 || e.BodySHA256 != strings.ToLower(e.BodySHA256) {
		return xerror.InvalidInput(xerror.RepoFile).Detail("git change payload digest is invalid")
	}
	if _, err := hex.DecodeString(e.BodySHA256); err != nil {
		return xerror.InvalidInput(xerror.RepoFile).Detail("git change payload digest is invalid")
	}
	if !e.Action.IsAChangeAction() || e.Action == ChangeActionUnknown {
		return xerror.InvalidInput(xerror.RepoFile).Detail("git change action is invalid")
	}
	return validateChangeIdentity(
		e.Provider, e.ProviderConnectionID, e.RepositoryID, e.ChangeRequestID,
		e.HeadSHA, e.SourceRef, e.TargetRef,
	)
}

func validDeliveryID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxChangeIDLength {
		return false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

// VerifiedChange is provider-neutral identity emitted only after an adapter has
// authenticated its webhook, CI identity, or CLI caller.
type VerifiedChange struct {
	Provider             repostore.Provider
	ProviderConnectionID string
	RepositoryID         string
	ChangeRequestID      string
	HeadSHA              string
	SourceRef            string
	TargetRef            string
	PipelinePath         string
	Rerequest            bool
}

// Validate rejects incomplete or mutable Git identity before target lookup.
func (c VerifiedChange) Validate() error {
	if err := validateChangeIdentity(
		c.Provider, c.ProviderConnectionID, c.RepositoryID, c.ChangeRequestID,
		c.HeadSHA, c.SourceRef, c.TargetRef,
	); err != nil {
		return err
	}
	if strings.TrimSpace(c.PipelinePath) == "" || len(c.PipelinePath) > maxChangePathLength {
		return xerror.InvalidInput(xerror.RepoFile).Detail("git change pipeline path is invalid")
	}
	cleanPath := path.Clean(strings.TrimSpace(c.PipelinePath))
	if cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, "../") || path.IsAbs(cleanPath) || cleanPath != c.PipelinePath {
		return xerror.InvalidInput(xerror.RepoFile).Detail("git change pipeline path must be repository-relative and normalized")
	}
	return nil
}

func validateChangeIdentity(provider repostore.Provider, connectionID, repositoryID, changeID, headSHA, sourceRef, targetRef string) error {
	if provider != repostore.ProviderGitHub && provider != repostore.ProviderGitLab {
		return xerror.InvalidInput(xerror.RepoFile).Detail("git change provider is unsupported")
	}
	for _, value := range []string{connectionID, repositoryID, changeID, sourceRef, targetRef} {
		if strings.TrimSpace(value) == "" {
			return xerror.InvalidInput(xerror.RepoFile).Detail("git change identity is incomplete")
		}
	}
	if len(connectionID) > maxChangeIDLength || len(repositoryID) > maxChangeIDLength ||
		len(changeID) > maxChangeIDLength || len(sourceRef) > maxChangeRefLength || len(targetRef) > maxChangeRefLength {
		return xerror.InvalidInput(xerror.RepoFile).Detail("git change identity exceeds its allowed length")
	}
	if len(headSHA) != 40 && len(headSHA) != 64 {
		return xerror.InvalidInput(xerror.RepoFile).Detail("git change head SHA is invalid")
	}
	if _, err := hex.DecodeString(headSHA); err != nil || headSHA != strings.ToLower(headSHA) {
		return xerror.InvalidInput(xerror.RepoFile).Detail("git change head SHA is invalid")
	}
	for _, ref := range []string{sourceRef, targetRef} {
		branch, ok := strings.CutPrefix(ref, "refs/heads/")
		if !ok || branch == "" || branch != NormalizeBranchName(branch) {
			return xerror.InvalidInput(xerror.RepoFile).Detail("git change refs must be canonical branch refs")
		}
	}
	return nil
}

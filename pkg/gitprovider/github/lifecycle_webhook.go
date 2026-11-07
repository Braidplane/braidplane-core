package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const maxInstallationLifecycleWebhookBytes = 2 * 1024 * 1024

var (
	ErrInstallationLifecycleAuthentication = xerror.New(xerror.KindUnauthorized, "GITHUB_APP_LIFECYCLE_AUTHENTICATION_FAILED", "GitHub App lifecycle authentication failed")
	ErrInstallationLifecycleIgnored        = xerror.New(xerror.KindInvalidInput, "GITHUB_APP_LIFECYCLE_IGNORED", "GitHub App lifecycle event is not handled")
	ErrInstallationLifecycleTooLarge       = xerror.New(xerror.KindInvalidInput, "GITHUB_APP_LIFECYCLE_TOO_LARGE", "GitHub App lifecycle payload is too large")
)

type InstallationRepository struct {
	ID         int64
	FullName   string
	OwnerLogin string
	Name       string
}

type InstallationLifecycleEvent struct {
	DeliveryID     string
	Action         string
	InstallationID int64
	Added          []InstallationRepository
	Removed        []int64
}

func VerifyInstallationLifecycleWebhook(secret []byte, signature, event, deliveryID string, body []byte) (InstallationLifecycleEvent, error) {
	if len(body) > maxInstallationLifecycleWebhookBytes {
		return InstallationLifecycleEvent{}, ErrInstallationLifecycleTooLarge
	}
	if len(secret) == 0 || !validLifecycleSignature(secret, signature, body) {
		return InstallationLifecycleEvent{}, ErrInstallationLifecycleAuthentication
	}
	if event != "installation" && event != "installation_repositories" {
		return InstallationLifecycleEvent{}, ErrInstallationLifecycleIgnored
	}
	var payload struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		RepositoriesAdded []struct {
			ID       int64  `json:"id"`
			FullName string `json:"full_name"`
			Name     string `json:"name"`
			Owner    struct {
				Login string `json:"login"`
			} `json:"owner"`
		} `json:"repositories_added"`
		RepositoriesRemoved []struct {
			ID int64 `json:"id"`
		} `json:"repositories_removed"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return InstallationLifecycleEvent{}, xerror.InvalidInput(xerror.JSON).With(err)
	}
	result := InstallationLifecycleEvent{DeliveryID: strings.TrimSpace(deliveryID), Action: payload.Action, InstallationID: payload.Installation.ID}
	if !validLifecycleDeliveryID(result.DeliveryID) || result.InstallationID <= 0 {
		return InstallationLifecycleEvent{}, xerror.InvalidInput(xerror.Organization).Detail("GitHub App lifecycle identity is invalid")
	}
	switch event + ":" + payload.Action {
	case "installation:suspend", "installation:unsuspend", "installation:deleted":
	case "installation_repositories:added", "installation_repositories:removed":
	default:
		return InstallationLifecycleEvent{}, ErrInstallationLifecycleIgnored
	}
	for _, repo := range payload.RepositoriesAdded {
		if repo.ID <= 0 || strings.TrimSpace(repo.FullName) == "" || strings.TrimSpace(repo.Name) == "" || strings.TrimSpace(repo.Owner.Login) == "" {
			return InstallationLifecycleEvent{}, xerror.InvalidInput(xerror.Organization).Detail("GitHub App repository identity is invalid")
		}
		result.Added = append(result.Added, InstallationRepository{ID: repo.ID, FullName: repo.FullName, OwnerLogin: repo.Owner.Login, Name: repo.Name})
	}
	for _, repo := range payload.RepositoriesRemoved {
		if repo.ID <= 0 {
			return InstallationLifecycleEvent{}, xerror.InvalidInput(xerror.Organization).Detail("GitHub App repository identity is invalid")
		}
		result.Removed = append(result.Removed, repo.ID)
	}
	return result, nil
}

func validLifecycleDeliveryID(deliveryID string) bool {
	if deliveryID == "" || len(deliveryID) > 255 {
		return false
	}
	for _, char := range deliveryID {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '-' {
			return false
		}
	}
	return true
}

func validLifecycleSignature(secret []byte, signature string, body []byte) bool {
	encoded, ok := strings.CutPrefix(strings.TrimSpace(signature), "sha256=")
	if !ok {
		return false
	}
	want, err := hex.DecodeString(encoded)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(want, mac.Sum(nil))
}

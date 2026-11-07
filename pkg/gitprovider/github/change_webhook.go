package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const closedState = "closed"

// VerifyChangeWebhook authenticates and normalizes a GitHub pull-request event.
// The webhook is only a low-latency signal; upsert processing re-reads the pull
// request from GitHub before trusting its current head or state. Pipeline paths
// are resolved later from Braidplane-owned mappings.
func VerifyChangeWebhook(secret []byte, signature, event, deliveryID string, body []byte) (gitprovider.VerifiedChangeEvent, error) {
	if len(body) > gitprovider.MaxChangeWebhookBytes {
		return gitprovider.VerifiedChangeEvent{}, gitprovider.ErrChangePayloadTooLarge
	}
	if len(secret) == 0 || !validSignature(secret, signature, body) {
		return gitprovider.VerifiedChangeEvent{}, gitprovider.ErrChangeAuthentication
	}
	var payload struct {
		Action       string `json:"action"`
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
		Repository struct {
			ID int64 `json:"id"`
		} `json:"repository"`
		PullRequest struct {
			Number int    `json:"number"`
			State  string `json:"state"`
			Head   struct {
				SHA string `json:"sha"`
				Ref string `json:"ref"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		} `json:"pull_request"`
		RequestedAction struct {
			Identifier string `json:"identifier"`
		} `json:"requested_action"`
		CheckRun struct {
			HeadSHA      string `json:"head_sha"`
			PullRequests []struct {
				Number int `json:"number"`
				Head   struct {
					Ref string `json:"ref"`
				} `json:"head"`
				Base struct {
					Ref string `json:"ref"`
				} `json:"base"`
			} `json:"pull_requests"`
		} `json:"check_run"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return gitprovider.VerifiedChangeEvent{}, xerror.InvalidInput(xerror.JSON).With(err)
	}
	action := gitprovider.ChangeActionUpsert
	if strings.TrimSpace(event) == "check_run" {
		requested := payload.Action == "rerequested" || (payload.Action == "requested_action" && payload.RequestedAction.Identifier == "rerequest")
		if !requested || len(payload.CheckRun.PullRequests) != 1 {
			return gitprovider.VerifiedChangeEvent{}, gitprovider.ErrChangeActionIgnored
		}
		payload.PullRequest.Number = payload.CheckRun.PullRequests[0].Number
		payload.PullRequest.State = "open"
		payload.PullRequest.Head.SHA = payload.CheckRun.HeadSHA
		payload.PullRequest.Head.Ref = payload.CheckRun.PullRequests[0].Head.Ref
		payload.PullRequest.Base.Ref = payload.CheckRun.PullRequests[0].Base.Ref
	} else {
		if strings.TrimSpace(event) != "pull_request" {
			return gitprovider.VerifiedChangeEvent{}, xerror.InvalidInput(xerror.RepoFile).Detail("unsupported GitHub change event")
		}
		switch payload.Action {
		case "opened", "reopened", "synchronize", "edited":
		case closedState:
			action = gitprovider.ChangeActionClose
		default:
			return gitprovider.VerifiedChangeEvent{}, gitprovider.ErrChangeActionIgnored
		}
	}
	if payload.Installation.ID <= 0 || payload.Repository.ID <= 0 || payload.PullRequest.Number <= 0 {
		return gitprovider.VerifiedChangeEvent{}, xerror.InvalidInput(xerror.RepoFile).Detail("GitHub change identity is invalid")
	}
	if action != gitprovider.ChangeActionClose && payload.PullRequest.State == closedState {
		return gitprovider.VerifiedChangeEvent{}, gitprovider.ErrChangeClosed
	}
	digest := sha256.Sum256(body)
	change := gitprovider.VerifiedChangeEvent{
		DeliveryID:           strings.TrimSpace(deliveryID),
		BodySHA256:           hex.EncodeToString(digest[:]),
		Action:               action,
		Provider:             repostore.ProviderGitHub,
		ProviderConnectionID: strconv.FormatInt(payload.Installation.ID, 10),
		RepositoryID:         strconv.FormatInt(payload.Repository.ID, 10),
		ChangeRequestID:      strconv.Itoa(payload.PullRequest.Number),
		HeadSHA:              strings.ToLower(payload.PullRequest.Head.SHA),
		SourceRef:            "refs/heads/" + gitprovider.NormalizeBranchName(payload.PullRequest.Head.Ref),
		TargetRef:            "refs/heads/" + gitprovider.NormalizeBranchName(payload.PullRequest.Base.Ref),
		Rerequest:            strings.TrimSpace(event) == "check_run",
	}
	return change, change.Validate()
}

func validSignature(secret []byte, signature string, body []byte) bool {
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

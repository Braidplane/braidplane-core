package gitlab

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	"github.com/Braidplane/braidplane-core/pkg/repostore"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const maxWebhookAge = 5 * time.Minute

type WebhookAuthority struct {
	ProviderConnectionID string
	RepositoryID         string
}

// VerifyChangeWebhook authenticates a GitLab Standard Webhooks delivery before
// parsing it and applies only Braidplane-owned connection and repository IDs.
func VerifyChangeWebhook(signingToken, deliveryID, timestamp, signature, event string, body []byte, now time.Time, authority WebhookAuthority) (gitprovider.VerifiedChangeEvent, error) {
	if len(body) > gitprovider.MaxChangeWebhookBytes {
		return gitprovider.VerifiedChangeEvent{}, gitprovider.ErrChangePayloadTooLarge
	}
	if !validStandardWebhookSignature(signingToken, deliveryID, timestamp, signature, body, now) {
		return gitprovider.VerifiedChangeEvent{}, gitprovider.ErrChangeAuthentication
	}
	if strings.TrimSpace(event) != "Merge Request Hook" {
		return gitprovider.VerifiedChangeEvent{}, xerror.InvalidInput(xerror.RepoFile).Detail("unsupported GitLab change event")
	}
	// Decode one value before typed parsing so duplicate objects cannot merge
	// fields from a representation different from the one used for replay identity.
	var value any
	if err := jsonvalue.Unmarshal(body, &value); err != nil {
		return gitprovider.VerifiedChangeEvent{}, xerror.InvalidInput(xerror.JSON).With(err)
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return gitprovider.VerifiedChangeEvent{}, xerror.InvalidInput(xerror.JSON).With(err)
	}
	var payload struct {
		ObjectKind string `json:"object_kind"`
		EventType  string `json:"event_type"`
		Project    struct {
			ID int64 `json:"id"`
		} `json:"project"`
		ObjectAttributes struct {
			IID          int64  `json:"iid"`
			State        string `json:"state"`
			Action       string `json:"action"`
			SourceBranch string `json:"source_branch"`
			TargetBranch string `json:"target_branch"`
			LastCommit   struct {
				ID string `json:"id"`
			} `json:"last_commit"`
		} `json:"object_attributes"`
	}
	if err := json.Unmarshal(canonical, &payload); err != nil {
		return gitprovider.VerifiedChangeEvent{}, xerror.InvalidInput(xerror.JSON).With(err)
	}
	// GitLab redacts emails in saved deliveries before resending them. Only
	// fields consumed by intake bind replay identity; raw bytes remain authenticated.
	canonical, err = json.Marshal(payload)
	if err != nil {
		return gitprovider.VerifiedChangeEvent{}, xerror.InvalidInput(xerror.JSON).With(err)
	}
	if payload.ObjectKind != "merge_request" || payload.EventType != "merge_request" {
		return gitprovider.VerifiedChangeEvent{}, xerror.InvalidInput(xerror.RepoFile).Detail("unsupported GitLab change payload")
	}
	if strconv.FormatInt(payload.Project.ID, 10) != authority.RepositoryID {
		return gitprovider.VerifiedChangeEvent{}, gitprovider.ErrChangeAuthentication
	}
	action := gitprovider.ChangeActionUpsert
	switch strings.ToLower(strings.TrimSpace(payload.ObjectAttributes.Action)) {
	case "open", "reopen", "update", "approved", "unapproved", "approval", "unapproval":
	case "close", "merge":
		action = gitprovider.ChangeActionClose
	default:
		return gitprovider.VerifiedChangeEvent{}, gitprovider.ErrChangeActionIgnored
	}
	state := strings.ToLower(strings.TrimSpace(payload.ObjectAttributes.State))
	if action == gitprovider.ChangeActionUpsert && (state == "closed" || state == "merged") {
		return gitprovider.VerifiedChangeEvent{}, gitprovider.ErrChangeClosed
	}
	change := gitprovider.VerifiedChangeEvent{
		DeliveryID:           strings.TrimSpace(deliveryID),
		BodySHA256:           payloadDigest(canonical),
		Action:               action,
		Provider:             repostore.ProviderGitLab,
		ProviderConnectionID: authority.ProviderConnectionID,
		RepositoryID:         authority.RepositoryID,
		ChangeRequestID:      strconv.FormatInt(payload.ObjectAttributes.IID, 10),
		HeadSHA:              strings.ToLower(payload.ObjectAttributes.LastCommit.ID),
		SourceRef:            "refs/heads/" + gitprovider.NormalizeBranchName(payload.ObjectAttributes.SourceBranch),
		TargetRef:            "refs/heads/" + gitprovider.NormalizeBranchName(payload.ObjectAttributes.TargetBranch),
	}
	return change, change.Validate()
}

func payloadDigest(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}

func validStandardWebhookSignature(signingToken, deliveryID, timestamp, signatures string, body []byte, now time.Time) bool {
	key, ok := standardWebhookSigningKey(signingToken)
	if !ok {
		return false
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil || strings.TrimSpace(deliveryID) == "" {
		return false
	}
	delta := now.Sub(time.Unix(seconds, 0))
	if delta < -maxWebhookAge || delta > maxWebhookAge {
		return false
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(deliveryID + "." + timestamp + "."))
	_, _ = mac.Write(body)
	want := "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
	for signature := range strings.FieldsSeq(signatures) {
		if hmac.Equal([]byte(want), []byte(signature)) {
			return true
		}
	}
	return false
}

func standardWebhookSigningKey(signingToken string) ([]byte, bool) {
	rawToken, ok := strings.CutPrefix(signingToken, "whsec_")
	if !ok || signingToken != strings.TrimSpace(signingToken) {
		return nil, false
	}
	key, err := base64.StdEncoding.Strict().DecodeString(rawToken)
	return key, err == nil && len(key) == 32
}

package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	gogitlab "gitlab.com/gitlab-org/api/client-go/v2"

	"github.com/Braidplane/braidplane-core/pkg/gitprovider"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// PublishProofCheck uses an MR note because GitLab commit statuses create CI
// pipelines and cannot represent an informational, neutral check.
func (c *Client) PublishProofCheck(ctx context.Context, check gitprovider.ProofCheck) error {
	iid, err := strconv.ParseInt(check.ChangeRequestID, 10, 64)
	if err != nil || iid <= 0 || check.RepositoryID == "" || check.HeadSHA == "" || check.ExternalID == "" || check.Conclusion != "neutral" {
		return xerror.InvalidInput(xerror.RepoFile).Detail("GitLab Proof requires an exact candidate and neutral conclusion")
	}
	user, _, err := c.client.Users.CurrentUser(gogitlab.WithContext(ctx))
	if err != nil {
		return mapProviderError(err, c.token)
	}
	marker := fmt.Sprintf("<!-- braidplane:%s:%s -->", check.ExternalID, check.HeadSHA)
	body := fmt.Sprintf("%s\n### Braidplane Proof\n\nHead: `%s`\n\n%s\n\n[View protected Proof receipt](%s)", marker, check.HeadSHA, check.Summary, check.DetailsURL)
	var existing *gogitlab.Note
	opts := &gogitlab.ListMergeRequestNotesOptions{}
	for {
		notes, response, err := c.client.Notes.ListMergeRequestNotes(check.RepositoryID, iid, opts, gogitlab.WithContext(ctx))
		if err != nil {
			return mapProviderError(err, c.token)
		}
		for _, note := range notes {
			if !note.System && note.Author.ID == user.ID && strings.HasPrefix(note.Body, marker+"\n") {
				existing = note
				break
			}
		}
		if existing != nil || response == nil || response.NextPage == 0 {
			break
		}
		opts.Page = response.NextPage
	}
	// Recheck after pagination, immediately before writing. Numeric project
	// identity prevents a renamed/replaced repository from receiving the result.
	proposal, err := c.GetProposal(ctx, gitprovider.ProposalGetRequest{ProposalID: check.ChangeRequestID})
	if err != nil {
		return err
	}
	if proposal.RepositoryID != check.RepositoryID {
		return gitprovider.ErrChangeStale
	}
	obsolete := proposal.State != gitprovider.ProposalStateOpen || proposal.Closed || proposal.Merged || !strings.EqualFold(proposal.HeadSHA, check.HeadSHA)
	if obsolete && !check.ExistingOnly {
		return gitprovider.ErrChangeStale
	}
	if existing != nil {
		if existing.Body == body {
			return nil
		}
		_, _, err = c.client.Notes.UpdateMergeRequestNote(check.RepositoryID, iid, existing.ID, &gogitlab.UpdateMergeRequestNoteOptions{Body: &body}, gogitlab.WithContext(ctx))
	} else if !check.ExistingOnly {
		// A failed POST may already have created the note. Let the worker reread it
		// before retrying instead of letting the SDK repeat an ambiguous mutation.
		_, _, err = c.client.Notes.CreateMergeRequestNote(check.RepositoryID, iid, &gogitlab.CreateMergeRequestNoteOptions{Body: &body}, gogitlab.WithContext(ctx),
			gogitlab.WithRequestRetry(func(context.Context, *http.Response, error) (bool, error) { return false, nil }))
	}
	if err != nil {
		return mapProviderError(err, c.token)
	}
	return nil
}

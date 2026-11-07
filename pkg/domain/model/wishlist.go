package model

//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=WishlistFocus -transform=snake -json -text -linecomment -output=wishlist_focus_enumer.go

const (
	WishlistWorkflowToTestFirstMaxLength                    = 500
	WishlistCommunicationRequestVersionPreAlphaInvitationV1 = "pre_alpha_invitation_v1"
	WishlistCommunicationRequestVersionPreAlphaInvitationV2 = "pre_alpha_invitation_v2"
	WishlistCommunicationRequestVersion                     = WishlistCommunicationRequestVersionPreAlphaInvitationV2
	WishlistCommunicationRequestText                        = "By joining, you ask Braidplane to email you one pre-alpha access invitation if selected."
)

type WishlistFocus int

const (
	WishlistFocusUnknown                       WishlistFocus = iota // unknown
	WishlistFocusCLIFirstETLRuns                                    // cli_first_etl_runs
	WishlistFocusGuidedETLDemoWalkthrough                           // guided_etl_demo_walkthrough
	WishlistFocusGitIntegration                                     // git_integration
	WishlistFocusContractsAndValidation                             // contracts_and_validation
	WishlistFocusScheduledReproducibleRuns                          // scheduled_reproducible_runs
	WishlistFocusSourceCheckpointsIncrementals                      // source_checkpoints_incrementals
)

type NewWishlistRegistration struct {
	WorkEmail                   string        `validate:"required,email,max=254"`
	CareMostAbout               WishlistFocus `validate:"wishlistfocus"`
	CommunicationRequestVersion string        `validate:"required,wishlistcommunicationrequestversion"`
	WorkflowToTestFirst         string        `validate:"omitempty,max=500"`
}

type WishlistRegistration struct {
	WorkEmail                   string
	CareMostAbout               WishlistFocus
	CommunicationRequestVersion string
	WorkflowToTestFirst         string
	Timestamps
}

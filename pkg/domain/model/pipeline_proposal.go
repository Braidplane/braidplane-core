package model

import (
	"crypto/sha256"
	"fmt"
	"net/mail"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	PipelineProposalModeRepositoryCredential = "repository_credential"
	PipelineProposalModeDelegatedUser        = "delegated_user"
	PipelineProposalModeOrganizationService  = "organization_service"
)

// PipelineProposalAuthority is the credential-safe provider identity bound to one publish attempt.
type PipelineProposalAuthority struct {
	ID                        int64  `json:"id"`
	PublicationMode           string `json:"publication_mode"`
	ProviderHost              string `json:"provider_host"`
	ProviderAccountID         string `json:"provider_account_id"`
	ProviderRepositoryID      string `json:"provider_repository_id"`
	ProviderCredentialID      string `json:"provider_credential_id"`
	ExecutingPrincipalID      string `json:"executing_principal_id"`
	ExecutingPrincipalDisplay string `json:"executing_principal_display"`
	AuthoritySource           string `json:"authority_source"`
	GitConfigurationHash      string `json:"git_configuration_hash,omitempty"`
	GitRepository             string `json:"git_repository,omitempty"`
	GitBaseRef                string `json:"git_base_ref,omitempty"`
	GitDataSourceSlug         string `json:"git_data_source_slug,omitempty"`
	GitCredentialSlug         string `json:"git_credential_slug,omitempty"`
	GitCredentialRevision     string `json:"git_credential_revision,omitempty"`
}

// PipelineProposalAuthorityPayload is the credential-free authority identity carried by publish jobs.
type PipelineProposalAuthorityPayload struct {
	ID                        int64  `json:"id"`
	PublicationMode           string `json:"publication_mode"`
	ProviderHost              string `json:"provider_host"`
	ProviderAccountID         string `json:"provider_account_id"`
	ProviderRepositoryID      string `json:"provider_repository_id"`
	ExecutingPrincipalID      string `json:"executing_principal_id"`
	ExecutingPrincipalDisplay string `json:"executing_principal_display"`
	AuthoritySource           string `json:"authority_source"`
	GitConfigurationHash      string `json:"git_configuration_hash,omitempty"`
	GitRepository             string `json:"git_repository,omitempty"`
	GitBaseRef                string `json:"git_base_ref,omitempty"`
	GitDataSourceSlug         string `json:"git_data_source_slug,omitempty"`
	GitCredentialSlug         string `json:"git_credential_slug,omitempty"`
	GitCredentialRevision     string `json:"git_credential_revision,omitempty"`
}

func (a PipelineProposalAuthority) Payload() PipelineProposalAuthorityPayload {
	return PipelineProposalAuthorityPayload{
		ID:                        a.ID,
		PublicationMode:           a.PublicationMode,
		ProviderHost:              a.ProviderHost,
		ProviderAccountID:         a.ProviderAccountID,
		ProviderRepositoryID:      a.ProviderRepositoryID,
		ExecutingPrincipalID:      a.ExecutingPrincipalID,
		ExecutingPrincipalDisplay: a.ExecutingPrincipalDisplay,
		AuthoritySource:           a.AuthoritySource,
		GitConfigurationHash:      a.GitConfigurationHash,
		GitRepository:             a.GitRepository,
		GitBaseRef:                a.GitBaseRef,
		GitDataSourceSlug:         a.GitDataSourceSlug,
		GitCredentialSlug:         a.GitCredentialSlug,
		GitCredentialRevision:     a.GitCredentialRevision,
	}
}

// ValidateForPersistence rejects incomplete identities and email-address disclosure.
func (a PipelineProposalAuthority) ValidateForPersistence() error {
	if a.PublicationMode == PipelineProposalModeRepositoryCredential && a.ProviderCredentialID != "" {
		return xerror.InvalidInput(xerror.Pipeline).Detail("plain Git authority cannot claim a provider credential")
	}
	return a.Payload().ValidateForPersistence()
}

func (a PipelineProposalAuthorityPayload) ValidateForPersistence() error {
	if a.PublicationMode == PipelineProposalModeRepositoryCredential {
		if a.AuthoritySource != "git_data_source_credential" || a.GitConfigurationHash == "" || a.GitRepository == "" || a.GitBaseRef == "" || a.GitDataSourceSlug == "" || a.GitCredentialSlug == "" || a.GitCredentialRevision == "" {
			return xerror.InvalidInput(xerror.Pipeline).Detail("Git repository credential binding is incomplete")
		}
		if a.ProviderHost != "" || a.ProviderAccountID != "" || a.ProviderRepositoryID != "" || a.ExecutingPrincipalID != "" {
			return xerror.InvalidInput(xerror.Pipeline).Detail("plain Git authority cannot claim a provider identity")
		}
	} else if strings.TrimSpace(a.ProviderAccountID) == "" {
		return xerror.InvalidInput(xerror.Pipeline).Detail("provider account ID is required")
	}
	display := strings.TrimSpace(a.ExecutingPrincipalDisplay)
	if display == "" {
		return xerror.InvalidInput(xerror.Pipeline).Detail("executing principal display is required")
	}
	if address, err := mail.ParseAddress(display); err == nil && strings.Contains(address.Address, "@") {
		return xerror.InvalidInput(xerror.Pipeline).Detail(
			"executing principal display must not be an email address",
		)
	}
	return nil
}

// GitConfigurationHash identifies the persisted non-secret connection configuration.
func GitConfigurationHash(config []byte) string { return fmt.Sprintf("%x", sha256.Sum256(config)) }

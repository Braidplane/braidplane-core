package model

import "github.com/Braidplane/braidplane-core/pkg/slug"

type MCPConsentPrompt struct {
	ID, ClientID, ClientName, RedirectOrigin, Resource string
	User, Organization                                 slug.Slug
	ReadOperations, ChangeOperations                   []string
}

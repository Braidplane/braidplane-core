package views

// OrganizationCreateFormData stores sticky values for organization creation forms.
type OrganizationCreateFormData struct {
	Slug        string
	Name        string
	Description string
}

func DefaultOrganizationCreateFormData() OrganizationCreateFormData {
	return OrganizationCreateFormData{}
}

package views

import (
	"context"
	"net/url"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
)

type EnvironmentBindingMatrixRow struct {
	RefSlug        string
	RefName        string
	PipelineSlug   string
	DataSourceType string
	Capabilities   []string
	Cells          []EnvironmentBindingMatrixCell
}

type EnvironmentBindingMatrixCell struct {
	EnvironmentSlug  string
	EnvironmentColor string
	DataSourceSlug   string
	DataSourceName   string
	DataSourceType   string
	IsBound          bool
	IsActive         bool
	IsReadOnly       bool
}

type EnvironmentCreateFormData struct {
	Name        string
	Slug        string
	Description string
	Color       string
	Protected   bool
}

func DefaultEnvironmentCreateFormData() EnvironmentCreateFormData {
	return EnvironmentCreateFormData{Color: string(api.EnvironmentColorGray)}
}

func (form EnvironmentCreateFormData) HasInput() bool {
	return strings.TrimSpace(form.Name) != "" ||
		strings.TrimSpace(form.Slug) != "" ||
		strings.TrimSpace(form.Description) != "" ||
		environmentColorValue(form.Color) != string(api.EnvironmentColorGray) ||
		form.Protected
}

// EnvironmentColorOption describes one selectable runtime environment palette color.
type EnvironmentColorOption struct {
	Value    string
	Label    string
	Class    string
	Selected bool
}

// EnvironmentColorOptions returns the controlled palette used by environment forms.
func EnvironmentColorOptions(selected string) []EnvironmentColorOption {
	selected = environmentColorValue(selected)
	colors := []struct {
		value string
		label string
	}{
		{string(api.EnvironmentColorBlue), "Blue"},
		{string(api.EnvironmentColorAmber), "Amber"},
		{string(api.EnvironmentColorRed), "Red"},
		{string(api.EnvironmentColorGreen), "Green"},
		{string(api.EnvironmentColorPurple), "Purple"},
		{string(api.EnvironmentColorGray), "Gray"},
	}
	options := make([]EnvironmentColorOption, 0, len(colors))
	for _, color := range colors {
		options = append(options, EnvironmentColorOption{
			Value:    color.value,
			Label:    color.label,
			Class:    EnvironmentColorBadgeClass(color.value),
			Selected: color.value == selected,
		})
	}
	return options
}

// EnvironmentColorFromForm parses a submitted palette value without accepting free-form colors.
func EnvironmentColorFromForm(raw string) (api.EnvironmentColor, bool) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	switch normalized {
	case string(api.EnvironmentColorBlue),
		string(api.EnvironmentColorAmber),
		string(api.EnvironmentColorRed),
		string(api.EnvironmentColorGreen),
		string(api.EnvironmentColorPurple),
		string(api.EnvironmentColorGray):
		return api.EnvironmentColor(normalized), true
	default:
		return "", false
	}
}

func EnvironmentDescriptionValue(description *api.EnvironmentDescription) string {
	if description == nil {
		return ""
	}
	return *description
}

func RuntimeVariableDescriptionValue(description *string) string {
	if description == nil {
		return ""
	}
	return *description
}

// EnvironmentSecretDescriptionValue returns an empty fallback for optional secret descriptions.
func EnvironmentSecretDescriptionValue(description *string) string {
	if description == nil {
		return ""
	}
	return *description
}

// EnvironmentColorBadgeClass maps a stored environment color to a compact badge treatment.
func EnvironmentColorBadgeClass(environmentColor string) string {
	return environmentColorSoftClass(environmentColor)
}

// EnvironmentRiskLabel describes the operator posture for a runtime environment.
func EnvironmentRiskLabel(environmentSlug string, protected bool) string {
	switch normalizedEnvironmentSlug(environmentSlug) {
	case environmentSlugProd, environmentSlugProduction:
		return "production"
	case environmentSlugStg, environmentSlugStage, environmentSlugStaging:
		return "staging"
	case environmentSlugDev, environmentSlugDevelopment:
		return "development"
	default:
		return "custom"
	}
}

// EnvironmentRiskSummary gives compact guidance for catalog and project environment cards.
func EnvironmentRiskSummary(environmentSlug string, protected bool) string {
	switch normalizedEnvironmentSlug(environmentSlug) {
	case environmentSlugProd, environmentSlugProduction:
		if protected {
			return "Protected production target. Treat mutations as production-impacting runtime changes."
		}
		return "Production target. Treat mutations as production-impacting runtime changes."
	case environmentSlugStg, environmentSlugStage, environmentSlugStaging:
		if protected {
			return "Protected staging target for release checks, drift review, and binding validation."
		}
		return "Staging target for release checks, drift review, and binding validation."
	case environmentSlugDev, environmentSlugDevelopment:
		if protected {
			return "Protected development target for local iteration and sandbox data."
		}
		return "Development target for local iteration and sandbox data."
	default:
		if protected {
			return "Custom protected runtime target."
		}
		return "Custom runtime target."
	}
}

// ProtectedEnvironmentCount counts catalog entries with protected mutation guards.
func ProtectedEnvironmentCount(environments []api.Environment) int {
	count := 0
	for _, environment := range environments {
		if environment.Protected {
			count++
		}
	}
	return count
}

// EnvironmentSelectionHref builds an organization catalog selection URL.
func EnvironmentSelectionHref(orgSlug, environmentSlug string) string {
	base := "/organizations/" + strings.TrimSpace(orgSlug) + "/environments"
	trimmedEnvironmentSlug := strings.TrimSpace(environmentSlug)
	if trimmedEnvironmentSlug == "" {
		return base
	}

	values := url.Values{}
	values.Set("environment_slug", trimmedEnvironmentSlug)
	return base + "?" + values.Encode()
}

// OrganizationEnvironmentDetailHref builds the full-page environment detail URL.
func OrganizationEnvironmentDetailHref(orgSlug, environmentSlug string) string {
	return "/organizations/" + strings.TrimSpace(orgSlug) + "/environments/" + strings.TrimSpace(environmentSlug)
}

// OrganizationEnvironmentHTMXTarget selects the partial root for embedded and full-page environment forms.
func OrganizationEnvironmentHTMXTarget(fullPage bool) string {
	if fullPage {
		return "#organization-environment-full-page"
	}
	return "#organization-environments-content"
}

// OrganizationEnvironmentHTMXSelect selects the partial root for embedded and full-page environment form responses.
func OrganizationEnvironmentHTMXSelect(fullPage bool) string {
	return OrganizationEnvironmentHTMXTarget(fullPage)
}

// ProjectEnvironmentSelectionHref builds a project catalog selection URL while preserving active runtime context.
func ProjectEnvironmentSelectionHref(orgSlug, projectSlug, activeEnvironmentSlug, selectedEnvironmentSlug string) string {
	base := "/orgs/" + strings.TrimSpace(orgSlug) + "/projects/" + strings.TrimSpace(projectSlug) + "/environments"
	values := url.Values{}
	if activeEnvironmentSlug = strings.TrimSpace(activeEnvironmentSlug); activeEnvironmentSlug != "" {
		values.Set("environment_slug", activeEnvironmentSlug)
	}
	if selectedEnvironmentSlug = strings.TrimSpace(selectedEnvironmentSlug); selectedEnvironmentSlug != "" {
		values.Set("selected", selectedEnvironmentSlug)
	}
	if encoded := values.Encode(); encoded != "" {
		return base + "?" + encoded
	}
	return base
}

// SelectedCatalogEnvironmentSlug validates the URL-selected catalog environment.
func SelectedCatalogEnvironmentSlug(catalog []api.Environment, requestedSlug string) string {
	requestedSlug = strings.TrimSpace(requestedSlug)
	if requestedSlug == "" {
		return ""
	}
	for _, environment := range catalog {
		if environment.Slug == requestedSlug {
			return requestedSlug
		}
	}
	return ""
}

// EnvironmentCatalogCardClass uses the shared inventory selection treatment;
// stored target colors stay on identity badges rather than implying health.
func EnvironmentCatalogCardClass(selected bool) string {
	if selected {
		return "border-primary/60 bg-primary/15 shadow-sm shadow-primary/10"
	}
	return "border-base-300 bg-base-100"
}

func ProjectEnvironmentCatalogCardClass(selected, enabled bool) string {
	if !selected && !enabled {
		return "border-base-300 bg-base-200/30"
	}
	return EnvironmentCatalogCardClass(selected)
}

// ProjectEnvironmentEnabled reports whether a catalog environment is enabled for the project.
func ProjectEnvironmentEnabled(environmentSlug string, environments []api.ProjectEnvironment) bool {
	environmentSlug = strings.TrimSpace(environmentSlug)
	for _, environment := range environments {
		if environment.EnvironmentSlug == environmentSlug {
			return true
		}
	}
	return false
}

// ProtectedProjectEnvironmentCount counts enabled environments that require confirmation.
func ProtectedProjectEnvironmentCount(environments []api.ProjectEnvironment) int {
	count := 0
	for _, environment := range environments {
		if environment.EnvironmentProtected {
			count++
		}
	}
	return count
}

// MissingBindingCount counts logical reference/environment cells without a binding.
func MissingBindingCount(
	refs []api.DataSourceRef,
	environments []api.ProjectEnvironment,
	bindings map[string][]api.DataSourceBinding,
) int {
	count := 0
	for _, row := range EnvironmentBindingMatrixRows(refs, environments, bindings) {
		for _, cell := range row.Cells {
			if !cell.IsBound {
				count++
			}
		}
	}
	return count
}

// normalizedEnvironmentSlug trims and lowercases runtime environment slugs before semantic matching.
func normalizedEnvironmentSlug(environmentSlug string) string {
	return strings.ToLower(strings.TrimSpace(environmentSlug))
}

// environmentColorValue normalizes display inputs and falls back to neutral gray.
func environmentColorValue(environmentColor string) string {
	color, ok := EnvironmentColorFromForm(environmentColor)
	if !ok {
		return string(api.EnvironmentColorGray)
	}
	return string(color)
}

// environmentColorOutlineClass maps stored palette values to outline badge classes.
func environmentColorOutlineClass(environmentColor string) string {
	switch environmentColorValue(environmentColor) {
	case string(api.EnvironmentColorBlue):
		return badgeInfoOutline
	case string(api.EnvironmentColorAmber):
		return badgeWarningOutline
	case string(api.EnvironmentColorRed):
		return badgeErrorOutline
	case string(api.EnvironmentColorGreen):
		return badgeSuccessOutline
	case string(api.EnvironmentColorPurple):
		return badgePurpleOutline
	default:
		return badgeGhostOutline
	}
}

// environmentColorSoftClass maps stored palette values to soft badge classes.
func environmentColorSoftClass(environmentColor string) string {
	switch environmentColorValue(environmentColor) {
	case string(api.EnvironmentColorBlue):
		return badgeInfoSoft
	case string(api.EnvironmentColorAmber):
		return badgeWarningSoft
	case string(api.EnvironmentColorRed):
		return badgeErrorSoft
	case string(api.EnvironmentColorGreen):
		return badgeSuccessSoft
	case string(api.EnvironmentColorPurple):
		return badgePurpleSoft
	default:
		return badgeGhostSoft
	}
}

func AvailableCatalogEnvironments(
	catalog []api.Environment,
	environments []api.ProjectEnvironment,
) []api.Environment {
	enabled := make(map[string]struct{}, len(environments))
	for _, environment := range environments {
		enabled[environment.EnvironmentSlug] = struct{}{}
	}

	available := make([]api.Environment, 0, len(catalog))
	for _, environment := range catalog {
		if _, ok := enabled[environment.Slug]; ok {
			continue
		}
		available = append(available, environment)
	}
	return available
}

// AvailableCatalogEnvironmentsByProtection returns available catalog entries filtered by protected-state.
func AvailableCatalogEnvironmentsByProtection(
	catalog []api.Environment,
	environments []api.ProjectEnvironment,
	protected bool,
) []api.Environment {
	available := AvailableCatalogEnvironments(catalog, environments)
	filtered := make([]api.Environment, 0, len(available))
	for _, environment := range available {
		if environment.Protected != protected {
			continue
		}
		filtered = append(filtered, environment)
	}
	return filtered
}

func EnvironmentBindingMatrixRows(
	refs []api.DataSourceRef,
	environments []api.ProjectEnvironment,
	bindings map[string][]api.DataSourceBinding,
) []EnvironmentBindingMatrixRow {
	rows := make([]EnvironmentBindingMatrixRow, 0, len(refs))
	for _, ref := range refs {
		cells := make([]EnvironmentBindingMatrixCell, 0, len(environments))
		for _, environment := range environments {
			environmentSlug := environment.EnvironmentSlug
			cells = append(cells, environmentBindingMatrixCell(environmentSlug, string(environment.EnvironmentColor), ref, bindings[environmentSlug]))
		}

		rows = append(rows, EnvironmentBindingMatrixRow{
			RefSlug:        ref.Slug,
			RefName:        ref.Name,
			PipelineSlug:   ref.PipelineSlug,
			DataSourceType: dataSourceRefTypeString(ref.DataSourceType),
			Capabilities:   dataSourceRefCapabilities(ref.RequiredCapabilities),
			Cells:          cells,
		})
	}
	return rows
}

func environmentBindingMatrixCell(
	environmentSlug string,
	environmentColor string,
	ref api.DataSourceRef,
	bindings []api.DataSourceBinding,
) EnvironmentBindingMatrixCell {
	for _, binding := range bindings {
		if binding.PipelineSlug != ref.PipelineSlug || binding.DataSourceRefSlug != ref.Slug {
			continue
		}

		return EnvironmentBindingMatrixCell{
			EnvironmentSlug:  environmentSlug,
			EnvironmentColor: environmentColor,
			DataSourceSlug:   binding.DataSourceSlug,
			DataSourceName:   binding.DataSourceName,
			DataSourceType:   string(binding.DataSourceType),
			IsBound:          true,
			IsActive:         binding.DataSourceActive,
			IsReadOnly:       binding.DataSourceReadOnly,
		}
	}

	return EnvironmentBindingMatrixCell{EnvironmentSlug: environmentSlug, EnvironmentColor: environmentColor}
}

func dataSourceRefCapabilities(capabilities []api.DataSourceRefCapability) []string {
	result := make([]string, 0, len(capabilities))
	for _, capability := range capabilities {
		result = append(result, strings.ReplaceAll(string(capability), "_", " "))
	}
	return result
}

func projectEnvironmentConfiguration(ctx context.Context) bool {
	return entityPageURL(ctx).Query().Get("section") == "configuration"
}

func ProjectEnvironmentConfigurationHref(orgSlug, projectSlug, activeEnvironmentSlug, selectedEnvironmentSlug string) string {
	list := ProjectEnvironmentSelectionHref(orgSlug, projectSlug, activeEnvironmentSlug, selectedEnvironmentSlug)
	u, _ := url.Parse(list)
	values := u.Query()
	values.Set("section", "configuration")
	u.RawQuery = values.Encode()
	return entityDetailHref(u.String(), list)
}

func projectEnvironmentAction(ctx context.Context, action string) string {
	u, _ := url.Parse(action)
	values := u.Query()
	current := entityPageURL(ctx).Query()
	for _, key := range []string{"section", "selected", "environment_slug", "list_return"} {
		if value := current.Get(key); value != "" {
			values.Set(key, value)
		}
	}
	u.RawQuery = values.Encode()
	return u.String()
}

func inactiveEnvironmentBindingCount(refs []api.DataSourceRef, environment api.ProjectEnvironment, bindings map[string][]api.DataSourceBinding) int {
	count := 0
	for _, row := range EnvironmentBindingMatrixRows(refs, []api.ProjectEnvironment{environment}, bindings) {
		for _, cell := range row.Cells {
			if cell.IsBound && !cell.IsActive {
				count++
			}
		}
	}
	return count
}

// OrganizationEnvironmentSection defaults unknown sections to target settings.
func OrganizationEnvironmentSection(section string) string {
	switch section {
	case "variables", "secrets":
		return section
	default:
		return "settings"
	}
}

func organizationEnvironmentAction(ctx context.Context, action string, fullPage bool) string {
	if !fullPage {
		return action
	}
	current := entityPageURL(ctx)
	if current.Query().Get("section") == "" && current.Query().Get("list_return") == "" {
		return action
	}
	return entitySectionHref(ctx, action, OrganizationEnvironmentSection(current.Query().Get("section")))
}

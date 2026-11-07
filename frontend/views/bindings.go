package views

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
)

const (
	bindingCellStateBound       = "bound"
	bindingCellStateInvalid     = "invalid"
	bindingCellStateMissing     = "missing"
	bindingCellStateUnavailable = "unavailable"
)

// PipelineBindingContext carries pipeline-owned refs and project environment bindings for detail views.
type PipelineBindingContext struct {
	DataSourceRefs                 []api.DataSourceRef
	Environments                   []api.ProjectEnvironment
	Bindings                       map[string][]api.DataSourceBinding
	DataSources                    []api.DataSource
	BindingUnavailableEnvironments map[string]bool
	RefsUnavailable                bool
	EnvironmentsUnavailable        bool
	BindingsUnavailable            bool
	DataSourcesUnavailable         bool
}

// DataSourceBindingRow joins one logical ref requirement with every enabled project environment.
type DataSourceBindingRow struct {
	PipelineSlug         string
	RefSlug              string
	RefName              string
	DataSourceType       string
	OperationTypes       []string
	Capabilities         []string
	RequiredCapabilities []api.DataSourceRefCapability
	RefAvailable         bool
	CapabilityMismatch   bool
	Cells                []DataSourceBindingCell
}

// DataSourceBindingCell describes one logical ref/environment binding state and available actions.
type DataSourceBindingCell struct {
	EnvironmentSlug      string
	EnvironmentName      string
	EnvironmentColor     string
	EnvironmentProtected bool
	State                string
	StateLabel           string
	StateClass           string
	StateDescription     string
	HasBinding           bool
	Binding              api.DataSourceBinding
	InvalidReasons       []string
	Options              []DataSourceBindingOption
}

// DataSourceBindingOption is one concrete data-source candidate for binding selectors.
type DataSourceBindingOption struct {
	Slug       string
	Name       string
	Type       string
	Label      string
	IsActive   bool
	IsReadOnly bool
	Compatible bool
	Selected   bool
}

type dataSourceBindingRefKey struct {
	pipelineSlug string
	refSlug      string
}

// ProjectBindingRows returns editable binding rows for refs required by project pipeline specs.
func ProjectBindingRows(projectContext ProjectOperationalContext) []DataSourceBindingRow {
	requirements := ProjectBindingRequirements(projectContext)
	rows := make([]DataSourceBindingRow, 0, len(requirements))
	for _, requirement := range requirements {
		rows = append(rows, dataSourceBindingRowFromSeed(
			requirement,
			projectContext.Environments,
			projectContext.Bindings,
			projectContext.BindingUnavailableEnvironments,
			projectContext.DataSources,
		))
	}
	return rows
}

// ProjectBindingRequirements extracts distinct logical refs from every loaded project pipeline spec.
func ProjectBindingRequirements(projectContext ProjectOperationalContext) []DataSourceBindingRow {
	refsBySlug := make(map[dataSourceBindingRefKey]api.DataSourceRef, len(projectContext.DataSourceRefs))
	for _, ref := range projectContext.DataSourceRefs {
		refsBySlug[newDataSourceBindingRefKey(ref.PipelineSlug, ref.Slug)] = ref
	}

	requirements := []DataSourceBindingRow{}
	requirementsBySlug := map[dataSourceBindingRefKey]int{}
	for idx := range projectContext.PipelineDetails {
		pipelineSlug := projectContext.PipelineDetails[idx].Slug
		if strings.TrimSpace(pipelineSlug) == "" {
			pipelineSlug = projectContext.PipelineDetails[idx].Spec.Metadata.Slug
		}
		requirements, requirementsBySlug = appendPipelineSpecBindingRequirements(
			projectContext.PipelineDetails[idx].Spec,
			pipelineSlug,
			refsBySlug,
			requirements,
			requirementsBySlug,
		)
	}
	return requirements
}

// PipelineBindingRows derives pipeline-required logical refs from the canonical spec projection.
func PipelineBindingRows(pipeline *api.Pipeline, bindingContext PipelineBindingContext) []DataSourceBindingRow {
	requirements := PipelineBindingRequirements(pipeline, bindingContext.DataSourceRefs)
	rows := make([]DataSourceBindingRow, 0, len(requirements))
	for _, requirement := range requirements {
		rows = append(rows, dataSourceBindingRowFromSeed(
			requirement,
			bindingContext.Environments,
			bindingContext.Bindings,
			bindingContext.BindingUnavailableEnvironments,
			bindingContext.DataSources,
		))
	}
	return rows
}

// PipelineBindingRequirements extracts distinct query and sink logical refs in spec order.
func PipelineBindingRequirements(pipeline *api.Pipeline, projectRefs []api.DataSourceRef) []DataSourceBindingRow {
	if pipeline == nil {
		return nil
	}
	return pipelineSpecBindingRequirements(pipeline.Spec, projectRefs)
}

// pipelineSpecBindingRequirements extracts distinct logical refs from one canonical pipeline spec.
func pipelineSpecBindingRequirements(spec api.PipelineSpec, projectRefs []api.DataSourceRef) []DataSourceBindingRow {
	refsBySlug := make(map[dataSourceBindingRefKey]api.DataSourceRef, len(projectRefs))
	for _, ref := range projectRefs {
		refsBySlug[newDataSourceBindingRefKey(ref.PipelineSlug, ref.Slug)] = ref
	}

	requirements := []DataSourceBindingRow{}
	requirementsBySlug := map[dataSourceBindingRefKey]int{}
	requirements, _ = appendPipelineSpecBindingRequirements(spec, spec.Metadata.Slug, refsBySlug, requirements, requirementsBySlug)
	return requirements
}

// appendPipelineSpecBindingRequirements adds query and sink refs while preserving first-seen spec order.
func appendPipelineSpecBindingRequirements(
	spec api.PipelineSpec,
	pipelineSlug string,
	refsBySlug map[dataSourceBindingRefKey]api.DataSourceRef,
	requirements []DataSourceBindingRow,
	requirementsBySlug map[dataSourceBindingRefKey]int,
) ([]DataSourceBindingRow, map[dataSourceBindingRefKey]int) {
	for _, step := range spec.Steps {
		kind, err := step.Discriminator()
		if err != nil {
			continue
		}
		switch kind {
		case string(api.PipelineStepKindQuery):
			queryStep, err := step.AsPipelineQueryStep()
			if err != nil {
				continue
			}
			requirements, requirementsBySlug = appendPipelineBindingRequirement(
				requirements,
				requirementsBySlug,
				refsBySlug,
				pipelineSlug,
				queryStep.Query.DataSourceRef,
				string(queryStep.Query.OperationType),
				api.DataSourceRefCapabilityPipelineSource,
			)
		case string(api.PipelineStepKindSink):
			sinkStep, err := step.AsPipelineSinkStep()
			if err != nil {
				continue
			}
			requirements, requirementsBySlug = appendPipelineBindingRequirement(
				requirements,
				requirementsBySlug,
				refsBySlug,
				pipelineSlug,
				sinkStep.Sink.DataSourceRef,
				string(sinkStep.Sink.OperationType),
				api.DataSourceRefCapabilityPipelineSink,
			)
		}
	}
	return requirements, requirementsBySlug
}

// ProjectBindingCellTotalCount returns the total logical ref by environment cell count.
func ProjectBindingCellTotalCount(refs []api.DataSourceRef, environments []api.ProjectEnvironment) int {
	return len(refs) * len(environments)
}

// ProjectBindingAttentionCount counts cells that need operator action or could not be checked.
func ProjectBindingAttentionCount(rows []DataSourceBindingRow) int {
	count := 0
	for _, row := range rows {
		for _, cell := range row.Cells {
			if cell.State != bindingCellStateBound {
				count++
			}
		}
	}
	return count
}

// DataSourceBindingStateBadgeClass returns the badge class for a binding state.
func DataSourceBindingStateBadgeClass(state string) string {
	switch state {
	case bindingCellStateBound:
		return badgeSuccessSoft
	case bindingCellStateInvalid:
		return badgeErrorSoft
	case bindingCellStateMissing:
		return badgeWarningSoft
	default:
		return badgeGhostSoft
	}
}

// DataSourceBindingCanSubmit reports whether a selector has a usable target.
func DataSourceBindingCanSubmit(options []DataSourceBindingOption, dataSourcesUnavailable, canUpdate bool) bool {
	if dataSourcesUnavailable || !canUpdate {
		return false
	}
	for _, option := range options {
		if option.Compatible {
			return true
		}
	}
	return false
}

// DataSourceBindingRefDisplayName returns a ref label only when it adds information beyond the slug and binding target.
func DataSourceBindingRefDisplayName(row DataSourceBindingRow) string {
	name := strings.TrimSpace(row.RefName)
	if name == "" || strings.EqualFold(name, strings.TrimSpace(row.RefSlug)) {
		return ""
	}
	for _, cell := range row.Cells {
		if !cell.HasBinding {
			continue
		}
		if strings.EqualFold(name, strings.TrimSpace(cell.Binding.DataSourceName)) ||
			strings.EqualFold(name, strings.TrimSpace(cell.Binding.DataSourceSlug)) {
			return ""
		}
	}
	return name
}

// DataSourceBindingRefIdentityName returns the primary label for a binding ref identity row.
func DataSourceBindingRefIdentityName(row DataSourceBindingRow) string {
	if displayName := DataSourceBindingRefDisplayName(row); displayName != "" {
		return displayName
	}
	return row.RefSlug
}

// DataSourceBindingRefIdentitySlug returns the monospace slug when it would not duplicate the primary label.
func DataSourceBindingRefIdentitySlug(row DataSourceBindingRow) string {
	if DataSourceBindingRefDisplayName(row) != "" {
		return row.RefSlug
	}
	return ""
}

// DataSourceBindingFormID builds a stable id for binding forms and protected modals.
func DataSourceBindingFormID(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.Trim(dataSourceBindingIDPart(part), "-")
		if part == "" {
			continue
		}
		cleaned = append(cleaned, part)
	}
	if len(cleaned) == 0 {
		return "data-source-binding"
	}
	return strings.Join(cleaned, "-")
}

// ProjectDataSourceBindingUpsertAction builds the project overview binding upsert URL.
func ProjectDataSourceBindingUpsertAction(orgSlug, projectSlug, environmentSlug, refSlug string, fullPage bool) string {
	return projectDataSourceBindingAction(orgSlug, projectSlug, "", environmentSlug, refSlug, "upsert", fullPage)
}

// ProjectDataSourceBindingDeleteAction builds the project overview binding delete URL.
func ProjectDataSourceBindingDeleteAction(orgSlug, projectSlug, environmentSlug, refSlug string, fullPage bool) string {
	return projectDataSourceBindingAction(orgSlug, projectSlug, "", environmentSlug, refSlug, "delete", fullPage)
}

// PipelineDataSourceBindingUpsertAction builds the pipeline detail binding upsert URL.
func PipelineDataSourceBindingUpsertAction(orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, activeEnvironmentSlug string, embedded bool) string {
	return pipelineDataSourceBindingAction(orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, activeEnvironmentSlug, "upsert", embedded)
}

// PipelineDataSourceBindingDeleteAction builds the pipeline detail binding delete URL.
func PipelineDataSourceBindingDeleteAction(orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, activeEnvironmentSlug string, embedded bool) string {
	return pipelineDataSourceBindingAction(orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, activeEnvironmentSlug, "delete", embedded)
}

// ProjectEnvironmentsHref builds the project runtime target page URL.
func ProjectEnvironmentsHref(orgSlug, projectSlug string) string {
	return "/orgs/" + strings.TrimSpace(orgSlug) + "/projects/" + strings.TrimSpace(projectSlug) + "/environments"
}

// DataSourceBindingUpsertAction builds the correct project or pipeline binding upsert URL.
func DataSourceBindingUpsertAction(orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, activeEnvironmentSlug string, fullPage, embedded bool) string {
	if strings.TrimSpace(pipelineSlug) != "" {
		return PipelineDataSourceBindingUpsertAction(orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, activeEnvironmentSlug, embedded)
	}
	return ProjectDataSourceBindingUpsertAction(orgSlug, projectSlug, environmentSlug, refSlug, fullPage)
}

// DataSourceBindingUpsertActionForRow builds the correct upsert URL for one binding row.
func DataSourceBindingUpsertActionForRow(orgSlug, projectSlug, pipelineSlug, environmentSlug string, row DataSourceBindingRow, activeEnvironmentSlug string, fullPage, embedded bool) string {
	if strings.TrimSpace(pipelineSlug) != "" {
		return PipelineDataSourceBindingUpsertAction(orgSlug, projectSlug, pipelineSlug, environmentSlug, row.RefSlug, activeEnvironmentSlug, embedded)
	}
	return projectDataSourceBindingAction(orgSlug, projectSlug, row.PipelineSlug, environmentSlug, row.RefSlug, "upsert", fullPage)
}

// DataSourceBindingDeleteAction builds the correct project or pipeline binding delete URL.
func DataSourceBindingDeleteAction(orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, activeEnvironmentSlug string, fullPage, embedded bool) string {
	if strings.TrimSpace(pipelineSlug) != "" {
		return PipelineDataSourceBindingDeleteAction(orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, activeEnvironmentSlug, embedded)
	}
	return ProjectDataSourceBindingDeleteAction(orgSlug, projectSlug, environmentSlug, refSlug, fullPage)
}

// DataSourceBindingDeleteActionForRow builds the correct delete URL for one binding row.
func DataSourceBindingDeleteActionForRow(orgSlug, projectSlug, pipelineSlug, environmentSlug string, row DataSourceBindingRow, activeEnvironmentSlug string, fullPage, embedded bool) string {
	if strings.TrimSpace(pipelineSlug) != "" {
		return PipelineDataSourceBindingDeleteAction(orgSlug, projectSlug, pipelineSlug, environmentSlug, row.RefSlug, activeEnvironmentSlug, embedded)
	}
	return projectDataSourceBindingAction(orgSlug, projectSlug, row.PipelineSlug, environmentSlug, row.RefSlug, "delete", fullPage)
}

// DataSourceBindingScopeSlug returns the pipeline scope used for form identity.
func DataSourceBindingScopeSlug(pipelineSlug string, row DataSourceBindingRow) string {
	if strings.TrimSpace(pipelineSlug) != "" {
		return pipelineSlug
	}
	return row.PipelineSlug
}

// DataSourceBindingHTMXTarget selects the correct SSR replacement root for binding forms.
func DataSourceBindingHTMXTarget(pipelineSlug string, fullPage, embedded bool) string {
	if strings.TrimSpace(pipelineSlug) != "" {
		if embedded {
			return "#pipeline-catalog-workspace"
		}
		return "body"
	}
	return ProjectDetailHTMXTarget(fullPage)
}

// DataSourceBindingHTMXSelect selects the matching response fragment for binding forms.
func DataSourceBindingHTMXSelect(pipelineSlug string, fullPage, embedded bool) string {
	return DataSourceBindingHTMXTarget(pipelineSlug, fullPage, embedded)
}

type dataSourceBindingRequirement struct {
	ref                  api.DataSourceRef
	requiredCapabilities []api.DataSourceRefCapability
	refAvailable         bool
	capabilityMismatch   bool
}

// dataSourceBindingRowFromSeed refreshes cell state for a precomputed project or pipeline row.
func dataSourceBindingRowFromSeed(
	row DataSourceBindingRow,
	environments []api.ProjectEnvironment,
	bindings map[string][]api.DataSourceBinding,
	unavailableEnvironments map[string]bool,
	dataSources []api.DataSource,
) DataSourceBindingRow {
	row.Cells = make([]DataSourceBindingCell, 0, len(environments))
	if strings.TrimSpace(row.RefName) == "" {
		row.RefName = row.RefSlug
	}
	for _, environment := range environments {
		environmentSlug := strings.TrimSpace(environment.EnvironmentSlug)
		cell := dataSourceBindingCell(row, environment, bindings[environmentSlug], unavailableEnvironments[environmentSlug], dataSources)
		row.Cells = append(row.Cells, cell)
	}
	return row
}

// dataSourceBindingCell resolves binding status, validation, and selector options for one environment.
func dataSourceBindingCell(
	row DataSourceBindingRow,
	environment api.ProjectEnvironment,
	bindings []api.DataSourceBinding,
	unavailable bool,
	dataSources []api.DataSource,
) DataSourceBindingCell {
	cell := DataSourceBindingCell{
		EnvironmentSlug:      environment.EnvironmentSlug,
		EnvironmentName:      environment.EnvironmentName,
		EnvironmentColor:     string(environment.EnvironmentColor),
		EnvironmentProtected: environment.EnvironmentProtected,
		State:                bindingCellStateMissing,
		StateLabel:           "missing",
		StateClass:           DataSourceBindingStateBadgeClass(bindingCellStateMissing),
		StateDescription:     "Choose an organization data source for this logical reference.",
	}

	if unavailable {
		cell.State = bindingCellStateUnavailable
		cell.StateLabel = bindingCellStateUnavailable
		cell.StateClass = DataSourceBindingStateBadgeClass(bindingCellStateUnavailable)
		cell.StateDescription = "Binding state could not be loaded for this environment."
		return cell
	}
	if !row.RefAvailable {
		cell.State = bindingCellStateUnavailable
		cell.StateLabel = "ref unavailable"
		cell.StateClass = DataSourceBindingStateBadgeClass(bindingCellStateUnavailable)
		cell.StateDescription = "This logical ref is used by the pipeline but is missing from the aggregate project ref view."
		return cell
	}
	if row.CapabilityMismatch {
		cell.State = bindingCellStateUnavailable
		cell.StateLabel = "contract mismatch"
		cell.StateClass = DataSourceBindingStateBadgeClass(bindingCellStateUnavailable)
		cell.StateDescription = "The pipeline ref does not declare the capability used by this spec."
		return cell
	}

	for _, binding := range bindings {
		if strings.TrimSpace(binding.PipelineSlug) != strings.TrimSpace(row.PipelineSlug) {
			continue
		}
		if strings.TrimSpace(binding.DataSourceRefSlug) != row.RefSlug {
			continue
		}
		cell.HasBinding = true
		cell.Binding = binding
		cell.Options = dataSourceBindingOptions(row, dataSources, binding.DataSourceSlug)
		cell.InvalidReasons = dataSourceBindingInvalidReasons(row, binding)
		if len(cell.InvalidReasons) > 0 {
			cell.State = bindingCellStateInvalid
			cell.StateLabel = "invalid"
			cell.StateClass = DataSourceBindingStateBadgeClass(bindingCellStateInvalid)
			cell.StateDescription = strings.Join(cell.InvalidReasons, "; ")
			return cell
		}
		cell.State = bindingCellStateBound
		cell.StateLabel = "bound"
		cell.StateClass = DataSourceBindingStateBadgeClass(bindingCellStateBound)
		cell.StateDescription = "Environment resolves this logical ref to a concrete data source."
		return cell
	}

	cell.Options = dataSourceBindingOptions(row, dataSources, "")
	return cell
}

// dataSourceBindingOptions builds selector choices with only sources that can satisfy one logical ref.
func dataSourceBindingOptions(row DataSourceBindingRow, dataSources []api.DataSource, selectedSlug string) []DataSourceBindingOption {
	options := make([]DataSourceBindingOption, 0, len(dataSources))
	for _, dataSource := range dataSources {
		compatible, _ := dataSourceBindingOptionCompatibility(row, dataSource)
		if !compatible {
			continue
		}
		option := DataSourceBindingOption{
			Slug:       strings.TrimSpace(dataSource.Slug),
			Name:       dataSource.Name,
			Type:       string(dataSource.Type),
			IsActive:   dataSource.IsActive,
			IsReadOnly: dataSource.ReadOnly,
			Compatible: true,
			Selected:   strings.TrimSpace(dataSource.Slug) == strings.TrimSpace(selectedSlug),
		}
		option.Label = dataSourceBindingOptionLabel(option)
		options = append(options, option)
	}
	return options
}

// dataSourceBindingOptionCompatibility checks whether a concrete source can satisfy a logical ref.
func dataSourceBindingOptionCompatibility(row DataSourceBindingRow, dataSource api.DataSource) (bool, string) {
	if strings.TrimSpace(row.DataSourceType) != "" && dataSource.Type != api.DataSourceType(row.DataSourceType) {
		return false, "type mismatch"
	}
	if strings.TrimSpace(row.DataSourceType) == "" || len(row.OperationTypes) > 0 {
		if !row.RefAvailable || len(row.OperationTypes) == 0 {
			return false, "ref unavailable"
		}
		var descriptor pipelinespec.ConnectorCapability
		found := false
		for _, candidate := range pipelinespec.AllConnectorCapabilities() {
			if candidate.DataSourceType.String() == string(dataSource.Type) {
				descriptor, found = candidate, true
				break
			}
		}
		if !found {
			return false, "type mismatch"
		}
		for _, operationType := range row.OperationTypes {
			if descriptor.QueryOperationType != operationType {
				return false, "operation mismatch"
			}
		}
		for _, capability := range row.RequiredCapabilities {
			switch capability {
			case api.DataSourceRefCapabilityPipelineSource:
				if !descriptor.SupportsPipelineSource {
					return false, "source unsupported"
				}
			case api.DataSourceRefCapabilityPipelineSink:
				if !descriptor.SupportsPipelineSink {
					return false, "sink unsupported"
				}
			default:
				return false, "capability unsupported"
			}
		}
	}
	if !dataSource.IsActive {
		return false, "inactive"
	}
	if dataSource.ReadOnly && dataSourceBindingRequiresSink(row.RequiredCapabilities) {
		return false, "read-only sink"
	}
	return true, ""
}

// dataSourceBindingInvalidReasons explains why a stored binding no longer satisfies its ref.
func dataSourceBindingInvalidReasons(row DataSourceBindingRow, binding api.DataSourceBinding) []string {
	reasons := []string{}
	if (strings.TrimSpace(row.DataSourceType) == "" || binding.DataSourceType == api.DataSourceType(row.DataSourceType)) && binding.DataSourceActive &&
		(!binding.DataSourceReadOnly || !dataSourceBindingRequiresSink(row.RequiredCapabilities)) {
		if compatible, _ := dataSourceBindingOptionCompatibility(row, api.DataSource{
			Type: binding.DataSourceType, IsActive: binding.DataSourceActive, ReadOnly: binding.DataSourceReadOnly,
		}); !compatible {
			reasons = append(reasons, "bound data source does not support the pipeline operations")
		}
	}
	if !binding.DataSourceActive {
		reasons = append(reasons, "bound data source is inactive")
	}
	if strings.TrimSpace(row.DataSourceType) != "" && binding.DataSourceType != api.DataSourceType(row.DataSourceType) {
		reasons = append(reasons, "bound data source type no longer matches the logical ref")
	}
	if binding.DataSourceReadOnly && dataSourceBindingRequiresSink(row.RequiredCapabilities) {
		reasons = append(reasons, "read-only data source cannot satisfy a pipeline sink")
	}
	return reasons
}

// dataSourceBindingOptionLabel formats one selector option with state hints.
func dataSourceBindingOptionLabel(option DataSourceBindingOption) string {
	label := dataSourceBindingConnectionLabel(option.Name, option.Slug)
	if option.Type != "" {
		label += " - " + option.Type
	}
	if !option.IsActive {
		label += " - inactive"
	}
	if option.IsReadOnly {
		label += " - read only"
	}
	return label
}

// Always retain connection identity when names are duplicated or unusable.
func dataSourceBindingConnectionLabel(name, slug string) string {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > 64 || strings.EqualFold(name, slug) {
		return slug
	}
	return name + " (" + slug + ")"
}

// appendPipelineBindingRequirement records a pipeline ref requirement while preserving spec order.
func appendPipelineBindingRequirement(
	requirements []DataSourceBindingRow,
	requirementsBySlug map[dataSourceBindingRefKey]int,
	refsBySlug map[dataSourceBindingRefKey]api.DataSourceRef,
	pipelineSlug string,
	refSlug string,
	operationType string,
	capability api.DataSourceRefCapability,
) ([]DataSourceBindingRow, map[dataSourceBindingRefKey]int) {
	pipelineSlug = strings.TrimSpace(pipelineSlug)
	refSlug = strings.TrimSpace(refSlug)
	if refSlug == "" {
		return requirements, requirementsBySlug
	}
	key := newDataSourceBindingRefKey(pipelineSlug, refSlug)
	if idx, ok := requirementsBySlug[key]; ok {
		requirements[idx].OperationTypes = append(requirements[idx].OperationTypes, strings.TrimSpace(operationType))
		if !dataSourceBindingHasCapability(requirements[idx].RequiredCapabilities, capability) {
			requirements[idx].RequiredCapabilities = append(requirements[idx].RequiredCapabilities, capability)
			requirements[idx].Capabilities = dataSourceRefCapabilities(requirements[idx].RequiredCapabilities)
		}
		requirements[idx].CapabilityMismatch = dataSourceBindingRequirementCapabilityMismatch(
			requirements[idx].RequiredCapabilities,
			refsBySlug[key].RequiredCapabilities,
			requirements[idx].RefAvailable,
		)
		return requirements, requirementsBySlug
	}

	ref, ok := refsBySlug[key]
	if !ok {
		ref = api.DataSourceRef{PipelineSlug: pipelineSlug, Slug: refSlug, Name: refSlug}
	}
	requirement := dataSourceBindingRequirementFromRef(ref, []api.DataSourceRefCapability{capability}, ok)
	requirementsBySlug[key] = len(requirements)
	requirements = append(requirements, DataSourceBindingRow{
		PipelineSlug:         requirement.ref.PipelineSlug,
		RefSlug:              requirement.ref.Slug,
		RefName:              requirement.ref.Name,
		DataSourceType:       dataSourceRefTypeString(requirement.ref.DataSourceType),
		OperationTypes:       []string{strings.TrimSpace(operationType)},
		Capabilities:         dataSourceRefCapabilities(requirement.requiredCapabilities),
		RequiredCapabilities: requirement.requiredCapabilities,
		RefAvailable:         requirement.refAvailable,
		CapabilityMismatch:   requirement.capabilityMismatch,
	})
	return requirements, requirementsBySlug
}

// dataSourceRefTypeString returns the optional connector type for display and compatibility checks.
func dataSourceRefTypeString(dsType *api.DataSourceType) string {
	if dsType == nil {
		return ""
	}
	return string(*dsType)
}

// newDataSourceBindingRefKey scopes a logical ref slug by its owning pipeline.
func newDataSourceBindingRefKey(pipelineSlug, refSlug string) dataSourceBindingRefKey {
	return dataSourceBindingRefKey{
		pipelineSlug: strings.TrimSpace(pipelineSlug),
		refSlug:      strings.TrimSpace(refSlug),
	}
}

// dataSourceBindingRequirementFromRef packages a pipeline ref with the capabilities required by a UI surface.
func dataSourceBindingRequirementFromRef(ref api.DataSourceRef, required []api.DataSourceRefCapability, available bool) dataSourceBindingRequirement {
	return dataSourceBindingRequirement{
		ref:                  ref,
		requiredCapabilities: required,
		refAvailable:         available,
		capabilityMismatch:   dataSourceBindingRequirementCapabilityMismatch(required, ref.RequiredCapabilities, available),
	}
}

// dataSourceBindingRequirementCapabilityMismatch detects refs missing pipeline-required capabilities.
func dataSourceBindingRequirementCapabilityMismatch(required, declared []api.DataSourceRefCapability, available bool) bool {
	if !available {
		return false
	}
	for _, capability := range required {
		if !dataSourceBindingHasCapability(declared, capability) {
			return true
		}
	}
	return false
}

// dataSourceBindingHasCapability reports whether a ref capability set contains a capability.
func dataSourceBindingHasCapability(capabilities []api.DataSourceRefCapability, capability api.DataSourceRefCapability) bool {
	for _, candidate := range capabilities {
		if candidate == capability {
			return true
		}
	}
	return false
}

// dataSourceBindingRequiresSink reports whether the binding may write through a data source.
func dataSourceBindingRequiresSink(capabilities []api.DataSourceRefCapability) bool {
	return dataSourceBindingHasCapability(capabilities, api.DataSourceRefCapabilityPipelineSink)
}

// dataSourceBindingIDPart normalizes route data into DOM-safe id segments.
func dataSourceBindingIDPart(value string) string {
	var builder strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			builder.WriteRune(r)
			continue
		}
		builder.WriteRune('-')
	}
	return builder.String()
}

// projectDataSourceBindingAction builds project-surface binding action URLs with return context.
func projectDataSourceBindingAction(orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, action string, fullPage bool) string {
	base := "/organizations/" + strings.TrimSpace(orgSlug) +
		"/projects/" + strings.TrimSpace(projectSlug) +
		"/environments/" + strings.TrimSpace(environmentSlug) +
		"/data-source-bindings/" + strings.TrimSpace(refSlug) +
		"/" + strings.TrimSpace(action)
	values := url.Values{}
	if strings.TrimSpace(pipelineSlug) != "" {
		values.Set("pipeline_slug", strings.TrimSpace(pipelineSlug))
	}
	if fullPage {
		values.Set("return_to", "full")
	}
	if encoded := values.Encode(); encoded != "" {
		return base + "?" + encoded
	}
	return base
}

// pipelineDataSourceBindingAction builds pipeline-surface binding action URLs with return context.
func pipelineDataSourceBindingAction(orgSlug, projectSlug, pipelineSlug, environmentSlug, refSlug, activeEnvironmentSlug, action string, embedded bool) string {
	base := "/orgs/" + strings.TrimSpace(orgSlug) +
		"/projects/" + strings.TrimSpace(projectSlug) +
		"/pipelines/" + strings.TrimSpace(pipelineSlug) +
		"/environments/" + strings.TrimSpace(environmentSlug) +
		"/data-source-bindings/" + strings.TrimSpace(refSlug) +
		"/" + strings.TrimSpace(action)
	values := url.Values{}
	if strings.TrimSpace(activeEnvironmentSlug) != "" {
		values.Set("environment_slug", strings.TrimSpace(activeEnvironmentSlug))
	}
	if embedded {
		values.Set("return_to", "list")
	}
	if encoded := values.Encode(); encoded != "" {
		return base + "?" + encoded
	}
	return base
}

// PipelineBindingAttentionLabel reports the same unresolved states as the binding section.
func PipelineBindingAttentionLabel(pipeline *api.Pipeline, bindingContext PipelineBindingContext) string {
	if bindingContext.RefsUnavailable || bindingContext.EnvironmentsUnavailable || bindingContext.BindingsUnavailable || bindingContext.DataSourcesUnavailable {
		return "Binding data unavailable"
	}
	rows := PipelineBindingRows(pipeline, bindingContext)
	if len(rows) > 0 && len(bindingContext.Environments) == 0 {
		return "Runtime environment required for bindings"
	}
	if ProjectBindingAttentionCount(rows) > 0 {
		return "Missing, invalid or unavailable bindings"
	}
	return ""
}

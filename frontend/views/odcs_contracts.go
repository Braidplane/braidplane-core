package views

import (
	"encoding/json"
	"fmt"
	"net/url"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
)

// Keep full-document coverage out of the catalog's per-object view state.
type ODCSCatalogObject struct {
	SchemaObjectID string
	DocumentValid  bool
	Shape          *payload.Contract
	Diagnostics    []odcs.Diagnostic
}

type ODCSCatalogView struct {
	DefinitionReturn                                                 string
	Impact                                                           *api.ODCSRevisionImpact
	CompareRevision, FromPipeline, FromPlan, ToPipeline, ToPlan      string
	FromInput, ToInput                                               string
	ImpactError                                                      string
	Org, Project, Contract, Revision, Section, Error, ConsumersError string
	Object                                                           string
	Inspection                                                       *api.ODCSRevisionInspection
	Document                                                         string
	Coverage                                                         []api.ODCSDeclarationCoverage
	CoveragePage                                                     int
	CoverageMore                                                     bool
	Selector                                                         bool
	Contracts                                                        []api.ODCSContract
	Revisions                                                        []api.ODCSRevisionMetadata
	Metadata                                                         *api.ODCSRevisionMetadata
	Objects                                                          []ODCSCatalogObject
	Consumers                                                        []api.ODCSConsumerImpact
	NextContracts, NextRevisions                                     *string
}

func ODCSCatalogHref(org, project, contract, revision string, selector bool, cursor string) string {
	query := url.Values{}
	if contract != "" {
		query.Set("contract", contract)
	}
	if revision != "" {
		query.Set("revision", revision)
	}
	if selector {
		query.Set("selector", "1")
	}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	path := "/orgs/" + url.PathEscape(org) + "/projects/" + url.PathEscape(project) + "/contracts"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return path
}

func ODCSCatalogSectionHref(data ODCSCatalogView, section string) string {
	href, _ := url.Parse(ODCSCatalogHref(data.Org, data.Project, data.Contract, data.Revision, data.Selector, ""))
	query := href.Query()
	query.Set("section", section)
	if data.Object != "" {
		query.Set("object", data.Object)
	}
	if safe := DefinitionReturnHref(data.DefinitionReturn, data.Org, data.Project); safe != "" {
		query.Set("definition_return", safe)
	}
	href.RawQuery = query.Encode()
	return href.String()
}

func ODCSFieldValidations(field payload.ContractField) string {
	if field.Validations == nil {
		return "—"
	}
	raw, err := json.Marshal(field.Validations)
	if err != nil {
		return "Field validations unavailable"
	}
	return string(raw)
}

func ODCSCoverageHref(data ODCSCatalogView, page int) string {
	href, _ := url.Parse(ODCSCatalogSectionHref(data, "coverage"))
	query := href.Query()
	query.Set("object", data.Object)
	query.Set("page", fmt.Sprint(page))
	href.RawQuery = query.Encode()
	return href.String()
}

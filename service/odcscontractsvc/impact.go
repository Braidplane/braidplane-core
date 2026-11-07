package odcscontractsvc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	drift "github.com/Braidplane/braidplane-core/pkg/pipeline/drift"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	spec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
	"github.com/Braidplane/braidplane-core/postgres"
)

type RevisionImpact struct {
	FromPlan, ToPlan                 *ImpactPlanSelection
	ProjectSlug                      slug.Slug
	ContractSlug                     slug.Slug
	FromRevisionSlug, ToRevisionSlug slug.Slug
	FromDigest, ToDigest             string
	Objects                          []ObjectImpact
	Consumers                        []ConsumerImpact
}

type ImpactDiagnostic struct {
	PropertyID   string
	RevisionSlug slug.Slug
	odcs.Diagnostic
}

type ImpactChange struct {
	drift.Change
	BeforePropertyID, AfterPropertyID string
}

type ObjectImpact struct {
	Scope                 string
	SchemaObjectID        string
	Comparison            string
	Changes               []ImpactChange
	Diagnostics           []ImpactDiagnostic
	before, after         *payload.Contract
	richBefore, richAfter *payload.Structure
}

type ConsumerImpact struct {
	ProjectSlug, PipelineSlug, StepSlug, RevisionSlug               slug.Slug
	Origin, Environment, SpecHash, Port, SchemaObjectID, Comparison string
	Diagnostics                                                     []ImpactDiagnostic
	Impact                                                          *drift.Report
}

type ImpactPlanSelection struct {
	Pipeline        slug.Slug
	Digest          string
	ProjectionInput *int
}

type RevisionImpactRequest struct {
	From, To         slug.Slug
	FromPlan, ToPlan *ImpactPlanSelection
}

func (s *service) PreviewRevisionImpact(ctx context.Context, org, project, contract slug.Slug, request RevisionImpactRequest) (*RevisionImpact, error) {
	from, to := request.From, request.To
	if from == "latest" || to == "latest" || (request.FromPlan == nil) != (request.ToPlan == nil) {
		return nil, xerror.ValidationFailed()
	}
	result := &RevisionImpact{ProjectSlug: project, ContractSlug: contract, FromRevisionSlug: from, ToRevisionSlug: to, Objects: []ObjectImpact{}, Consumers: []ConsumerImpact{}}
	err := s.txm.RunRead(ctx, func(ctx context.Context, db postgres.DBTX) error {
		before, err := s.repo.GetRevision(ctx, db, org, project, contract, from)
		if err != nil {
			return err
		}
		after, err := s.repo.GetRevision(ctx, db, org, project, contract, to)
		if err != nil {
			return err
		}
		result.FromDigest, result.ToDigest = before.Digest, after.Digest
		if request.FromPlan != nil {
			if err := s.previewSelectedProjectionImpact(ctx, db, org, project, contract, before, after, request, result); err != nil {
				return err
			}
		} else {
			objects := map[string]bool{}
			for _, revision := range []*model.ODCSRevision{before, after} {
				for _, object := range revision.SchemaObjects {
					objects[object.ID] = true
				}
			}
			ids := make([]string, 0, len(objects))
			for id := range objects {
				ids = append(ids, id)
			}
			slices.Sort(ids)
			for _, id := range ids {
				comparison, err := compareObject(before, after, id)
				if err != nil {
					return err
				}
				result.Objects = append(result.Objects, comparison)
			}
		}
		snapshots, err := s.repo.ListConsumerSnapshots(ctx, db, org, project)
		if err != nil {
			return err
		}
		for _, snapshot := range snapshots {
			var pipeline spec.Pipeline
			if err := jsonvalue.Unmarshal(snapshot.Spec, &pipeline); err != nil {
				return xerror.Unexpected().With(err).Detail("decode ODCS consumer pipeline")
			}
			var pins []model.PinnedODCSPort
			if err := jsonvalue.Unmarshal(snapshot.Pins, &pins); err != nil {
				return xerror.Unexpected().With(err).Detail("decode ODCS consumer pins")
			}
			hash := snapshot.SpecHash
			if hash == "" {
				hash = fmt.Sprintf("%x", sha256.Sum256(snapshot.Spec))
			}
			for _, port := range consumerPorts(&pipeline) {
				ref := port.ref
				if ref == nil || ref.ContractSlug != contract {
					continue
				}
				consumer := ConsumerImpact{ProjectSlug: project, PipelineSlug: pipeline.Metadata.Slug, StepSlug: port.step, RevisionSlug: ref.RevisionSlug, Origin: snapshot.Origin, Environment: snapshot.Environment, SpecHash: hash, Port: port.name, SchemaObjectID: ref.SchemaObjectID, Comparison: "other_revision", Diagnostics: []ImpactDiagnostic{}}
				if ref.RevisionSlug == from || ref.RevisionSlug == to {
					index := slices.IndexFunc(result.Objects, func(o ObjectImpact) bool { return o.SchemaObjectID == ref.SchemaObjectID })
					if index < 0 {
						consumer.Comparison = "unsupported"
						consumer.Diagnostics = append(consumer.Diagnostics, ImpactDiagnostic{RevisionSlug: ref.RevisionSlug, Diagnostic: odcs.Diagnostic{Code: "object_unknown", Path: "$.schema", Message: "Select a schema object present in the compared revisions."}})
					} else {
						object := result.Objects[index]
						consumer.Comparison, consumer.Diagnostics = object.Comparison, object.Diagnostics
						oldShape, newShape := wireShape(object.before, ref), wireShape(object.after, ref)
						if snapshot.Origin == "deployed" {
							pinIndex := slices.IndexFunc(pins, func(p model.PinnedODCSPort) bool { return p.StepSlug == port.step && p.Port == port.name })
							if pinIndex < 0 {
								return xerror.Unexpected().Detail("deployed ODCS consumer is missing its immutable pin")
							}
							pin := pins[pinIndex]
							expected := before
							if ref.RevisionSlug == to {
								expected = after
							}
							if pin.ContractSlug != contract || pin.RevisionSlug != ref.RevisionSlug || pin.SchemaObjectID != ref.SchemaObjectID || pin.Digest != expected.Digest || pin.Shape == nil {
								return xerror.Unexpected().Detail("deployed ODCS consumer pin differs from its reference")
							}
							if ref.RevisionSlug == from {
								oldShape = pin.Shape
							} else {
								newShape = pin.Shape
							}
						}
						if object.Scope == "selected_projection" {
							if pipeline.Metadata.Slug != request.FromPlan.Pipeline && pipeline.Metadata.Slug != request.ToPlan.Pipeline {
								consumer.Comparison = "unsupported"
								consumer.Diagnostics = []ImpactDiagnostic{{RevisionSlug: ref.RevisionSlug, Diagnostic: odcs.Diagnostic{Code: "projection_not_selected", Path: "$", Message: "Select retained plans for this pipeline before comparing its executable meaning."}}}
							} else {
								consumer.Impact = selectedProjectionPortImpact(&pipeline, port, object)
								result.Consumers = append(result.Consumers, consumer)
								continue
							}
						}
						if consumer.Comparison == "supported" {
							consumer.Impact = drift.PortImpact(&pipeline, port.step, port.name, oldShape, newShape)
						}
					}
				}
				if consumer.Comparison == "unsupported" {
					consumer.Impact = &drift.Report{Type: drift.ReportTypeContractImpact, Status: drift.ReportStatusOpen, Severity: drift.ReportSeverityWarning, Summary: "Contract impact is unknown; resolve unsupported revision diagnostics before upgrading this port.", Changes: []drift.Change{}, Impacts: drift.PortImpacts(&pipeline, port.step, drift.ReportSeverityWarning)}
				}
				result.Consumers = append(result.Consumers, consumer)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func compareObject(before, after *model.ODCSRevision, id string) (ObjectImpact, error) {
	result := ObjectImpact{SchemaObjectID: id, Scope: "flat_revision", Comparison: "supported", Changes: []ImpactChange{}, Diagnostics: []ImpactDiagnostic{}}
	shapes := [2]*payload.Contract{}
	identities := [2]map[string]string{}
	for i, revision := range []*model.ODCSRevision{before, after} {
		identities[i] = map[string]string{}
		present := slices.ContainsFunc(revision.SchemaObjects, func(o model.ODCSSchemaObject) bool { return o.ID == id })
		conversion, err := odcs.Convert(revision.Document, id)
		if err != nil {
			return result, err
		}
		if conversion.Digest != revision.Digest || revision.ODCSVersion != odcs.PinnedVersion {
			return result, xerror.Unexpected().Detail("ODCS impact revision differs from its immutable document")
		}

		shapes[i] = conversion.Shape
		var document struct {
			Schema []struct {
				ID         string `json:"id"`
				Properties any    `json:"properties"`
			} `json:"schema"`
		}
		if err := json.Unmarshal(revision.Document, &document); err != nil {
			return result, xerror.Unexpected().With(err)
		}
		for _, object := range document.Schema {
			if object.ID == id {
				properties, _ := object.Properties.([]any)
				for _, value := range properties {
					property, _ := value.(map[string]any)
					name, _ := property["name"].(string)
					propertyID, _ := property["id"].(string)
					identities[i][name] = propertyID
				}
			}
		}
		for _, diagnostic := range conversion.Diagnostics {
			if !present && conversion.DocumentValid && diagnostic.Code == "object_not_found" {
				continue
			}
			item := ImpactDiagnostic{RevisionSlug: revision.Slug, Diagnostic: diagnostic}
			for objectIndex, object := range document.Schema {
				prefix := fmt.Sprintf("$.schema[%d].properties[", objectIndex)
				if object.ID != id || !strings.HasPrefix(diagnostic.Path, prefix) {
					continue
				}
				indexText, _, _ := strings.Cut(strings.TrimPrefix(diagnostic.Path, prefix), "]")
				index, err := strconv.Atoi(indexText)
				properties, _ := object.Properties.([]any)
				if err == nil && index >= 0 && index < len(properties) {
					property, _ := properties[index].(map[string]any)
					item.PropertyID, _ = property["id"].(string)
				}
			}
			result.Diagnostics = append(result.Diagnostics, item)
		}
	}
	result.before, result.after = shapes[0], shapes[1]
	if len(result.Diagnostics) > 0 {
		result.Comparison = "unsupported"
		return result, nil
	}
	pipeline := &spec.Pipeline{Steps: []spec.Step{{Slug: "object", Kind: "query", Query: &spec.QueryRef{}}}}
	report := drift.PortImpact(pipeline, "object", "output", shapes[0], shapes[1])
	if report != nil {
		for _, change := range report.Changes {
			path := strings.TrimPrefix(change.Path, "step[object].query.output_contract.")
			name, _, _ := strings.Cut(path, ".")
			change.Path = "schema_object[" + id + "]." + path
			result.Changes = append(result.Changes, ImpactChange{Change: change, BeforePropertyID: identities[0][name], AfterPropertyID: identities[1][name]})
		}
	}
	names := make([]string, 0, len(identities[0]))
	for name := range identities[0] {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		oldID, newID := identities[0][name], identities[1][name]
		if _, exists := identities[1][name]; exists && oldID != newID {
			result.Changes = append(result.Changes, ImpactChange{Change: drift.Change{Kind: "identity_change", Path: "schema_object[" + id + "]." + name + ".id", Before: oldID, After: newID, Severity: drift.ReportSeverityWarning, ReasonCode: "property_identity_changed"}, BeforePropertyID: oldID, AfterPropertyID: newID})
		}
	}
	return result, nil
}

func wireShape(shape *payload.Contract, ref *spec.ODCSContractRef) *payload.Contract {
	if shape == nil {
		return nil
	}
	wire := *shape
	wire.Representation, wire.RootShape = ref.Representation, ref.RootShape
	return &wire
}

type consumerPort struct {
	step slug.Slug
	name string
	ref  *spec.ODCSContractRef
}

func consumerPorts(pipeline *spec.Pipeline) []consumerPort {
	var ports []consumerPort
	for _, step := range pipeline.Steps {
		if step.Input != nil {
			ports = append(ports, consumerPort{step.Slug, "output", step.Input.OutputContractRef})
		}
		if step.Query != nil {
			ports = append(ports, consumerPort{step.Slug, "output", step.Query.OutputContractRef})
		}
		if step.Transform != nil {
			ports = append(ports, consumerPort{step.Slug, "input", step.Transform.InputContractRef}, consumerPort{step.Slug, "output", step.Transform.OutputContractRef})
		}
		if step.Transform != nil {
			for name, attachment := range step.Transform.InputContracts {
				if attachment != nil {
					ports = append(ports, consumerPort{step.Slug, "input:" + name, attachment.ContractRef})
				}
			}
		}
		if step.Sink != nil {
			ports = append(ports, consumerPort{step.Slug, "input", step.Sink.InputContractRef})
		}
	}
	return ports
}

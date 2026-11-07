// Package deploymentplan holds immutable environment-specific executable plans.
// Credential values and runtime selector values never enter this envelope.
package deploymentplan

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/contractplan"
	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	pipelinevars "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const FormatVersion = 1

var ErrInvalid = xerror.New(xerror.KindInvalidInput, "DEPLOYMENT_RESOLUTION_INVALID", "deployment resolution is missing, invalid or no longer usable")

type Target struct {
	Step            slug.Slug `json:"step"`
	Role            string    `json:"role"`
	Reference       slug.Slug `json:"reference"`
	DataSource      slug.Slug `json:"data_source"`
	ConfigDigest    string    `json:"config_digest"`
	BindingRevision string    `json:"binding_revision"`
}

type Input struct {
	Key      string                         `json:"key"`
	Type     model.RuntimeVariableValueType `json:"type"`
	Value    string                         `json:"value"`
	Revision string                         `json:"revision"`
}

type Plan struct {
	TemplateDigest   string                          `json:"template_digest"`
	ResolutionInputs map[string]odcs.ResolutionValue `json:"resolution_inputs,omitempty"`
	RevalidatedFrom  string                          `json:"revalidated_from,omitempty"`
	Inputs           []Input                         `json:"inputs,omitempty"`
	LiteralValues    []pipelinevars.LiteralLocation  `json:"literal_values,omitempty"`
	FormatVersion    int                             `json:"format_version"`
	Organization     slug.Slug                       `json:"organization"`
	Project          slug.Slug                       `json:"project"`
	Environment      slug.Slug                       `json:"environment"`
	CompiledDigest   string                          `json:"compiled_digest"`
	CompiledPlan     json.RawMessage                 `json:"compiled_plan"`
	Pipeline         *pipelinespec.Pipeline          `json:"pipeline"`
	Targets          []Target                        `json:"targets"`
}

func Encode(plan Plan) ([]byte, string, error) {
	plan.FormatVersion = FormatVersion
	plan.Targets = slices.Clone(plan.Targets)
	plan.Inputs = slices.Clone(plan.Inputs)
	plan.LiteralValues = pipelinevars.LiteralLocations(plan.Pipeline)
	slices.SortFunc(plan.Inputs, func(a, b Input) int { return strings.Compare(a.Key, b.Key) })
	slices.SortFunc(plan.Targets, func(a, b Target) int {
		if a.Step != b.Step {
			return strings.Compare(a.Step.String(), b.Step.String())
		}
		return strings.Compare(a.Role, b.Role)
	})
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, "", ErrInvalid.With(err)
	}
	id := digest(raw)
	if _, err := Read(raw, id); err != nil {
		return nil, "", err
	}
	return raw, id, nil
}

// Read verifies only recorded bytes, without resolving bindings or operations.
func Read(raw []byte, expected string) (*Plan, error) {
	if len(raw) == 0 || len(raw) > 32<<20 || digest(raw) != expected {
		return nil, ErrInvalid
	}
	var plan Plan
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(&plan); err != nil || !validDigest(plan.TemplateDigest) || plan.RevalidatedFrom != "" && !validDigest(plan.RevalidatedFrom) || plan.FormatVersion != FormatVersion || plan.Pipeline == nil {
		return nil, ErrInvalid.With(err)
	}
	for _, id := range []slug.Slug{plan.Organization, plan.Project, plan.Environment, plan.Pipeline.Metadata.Slug} {
		if _, err := slug.New(id.String()); err != nil {
			return nil, ErrInvalid.With(err)
		}
	}
	compiled, err := contractplan.Read(plan.CompiledPlan, plan.CompiledDigest)
	if err != nil {
		return nil, ErrInvalid.With(err)
	}
	portable, err := compiled.RuntimePipeline()
	if err != nil || portable.Metadata.Slug != plan.Pipeline.Metadata.Slug || len(portable.Steps) != len(plan.Pipeline.Steps) {
		return nil, ErrInvalid.With(err)
	}
	keys := pipelinevars.EnvironmentKeys(portable)
	if len(keys) != len(plan.Inputs) {
		return nil, ErrInvalid
	}
	for i, input := range plan.Inputs {
		if input.Key != keys[i] || !input.Type.IsARuntimeVariableValueType() || input.Type == model.RuntimeVariableValueTypeUnknown || input.Revision == "" {
			return nil, ErrInvalid
		}
	}
	used := make(map[string]bool)
	for i, step := range plan.Pipeline.Steps {
		original := portable.Steps[i]
		if original.Slug != step.Slug || original.Kind != step.Kind || (original.Query == nil) != (step.Query == nil) || (original.Sink == nil) != (step.Sink == nil) {
			return nil, ErrInvalid
		}
		for _, role := range []string{"query", "sink"} {
			var source, reference slug.Slug
			if role == "query" && step.Query != nil {
				source, reference = step.Query.DataSourceSlug, original.Query.DataSourceSlug
			}
			if role == "sink" && step.Sink != nil {
				source, reference = step.Sink.DataSourceSlug, original.Sink.DataSourceSlug
			}
			if source == "" {
				continue
			}
			count := 0
			for _, target := range plan.Targets {
				if target.Step != step.Slug || target.Role != role {
					continue
				}
				if target.DataSource != source || target.Reference != reference || !validDigest(target.ConfigDigest) || target.BindingRevision == "" {
					return nil, ErrInvalid
				}
				if _, err := slug.New(source.String()); err != nil {
					return nil, ErrInvalid.With(err)
				}
				count++
			}
			if count != 1 {
				return nil, ErrInvalid
			}
			used[step.Slug.String()+"/"+role] = true
		}
	}
	if !pipelinevars.RestoreLiteralLocations(plan.Pipeline, plan.LiteralValues) {
		return nil, ErrInvalid
	}
	if len(used) != len(plan.Targets) {
		return nil, ErrInvalid
	}
	return &plan, nil
}

func (p *Plan) Matches(org, project, environment, pipeline slug.Slug) bool {
	return p != nil && p.Organization == org && p.Project == project && p.Environment == environment && p.Pipeline != nil && p.Pipeline.Metadata.Slug == pipeline
}

func digest(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }

func validDigest(s string) bool {
	if len(s) != 71 || !strings.HasPrefix(s, "sha256:") {
		return false
	}
	for _, c := range s[7:] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Package contractplan owns portable immutable compilation snapshots. It has no
// catalog, Git, environment, credential, or execution capability.
package contractplan

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/domain/model"
	"github.com/Braidplane/braidplane-core/pkg/jsonvalue"
	"github.com/Braidplane/braidplane-core/pkg/odcs"
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	pipelineruntime "github.com/Braidplane/braidplane-core/pkg/pipeline/runtime"
	pipelinespec "github.com/Braidplane/braidplane-core/pkg/pipeline/spec"
	pipelinevalidate "github.com/Braidplane/braidplane-core/pkg/pipeline/validate"
	"github.com/Braidplane/braidplane-core/pkg/resourcescope"
	"github.com/Braidplane/braidplane-core/pkg/slug"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

const (
	FormatVersion      = 4
	CompilerID         = "braidplane/contract-plan/v4"
	StructuralPolicyID = "braidplane/structural-policy/v1"
)

var ErrInvalid = xerror.New(xerror.KindInvalidInput, "COMPILED_PLAN_INVALID", "compiled contract plan is incomplete, corrupt, or unsupported")

// Operation retains the complete saved definition, including parameter defaults
// and input requirements. Revision is the authoring revision, never a live alias.
type Operation struct {
	DataSourceType model.DataSourceType `json:"data_source_type,omitempty"`
	Step           slug.Slug            `json:"step"`
	Role           string               `json:"role"`
	DataSource     slug.Slug            `json:"data_source"`
	Slug           slug.Slug            `json:"slug"`
	Revision       string               `json:"revision"`
	Content        json.RawMessage      `json:"content"`
	Digest         string               `json:"digest"`
}

type OperationContent struct {
	ResourceScope            *resourcescope.Scope          `json:"resource_scope,omitempty"`
	Data                     json.RawMessage               `json:"data"`
	ExpectedInputContract    json.RawMessage               `json:"expected_input_contract,omitempty"`
	ExpectedInputContractRef *pipelinespec.ODCSContractRef `json:"expected_input_contract_ref,omitempty"`
	OutputContract           json.RawMessage               `json:"output_contract,omitempty"`
	OutputContractRef        *pipelinespec.ODCSContractRef `json:"output_contract_ref,omitempty"`
	ContractPins             []model.PinnedODCSPort        `json:"contract_pins,omitempty"`
	Parameters               []OperationParameter          `json:"parameters"`
}

type OperationParameter struct {
	Slug     slug.Slug `json:"slug"`
	Name     string    `json:"name"`
	DataType string    `json:"data_type"`
	Required bool      `json:"required"`
	Default  *string   `json:"default,omitempty"`
}

// Input contains only already-authorized, environment-independent material.
// Rich selections retain structural checks independently of catalog access.
type Input struct {
	Source     Source
	Authored   json.RawMessage
	Operations []Operation
	Pins       []model.PinnedODCSPort
	Projection *odcs.ProjectionRequest
}

type Source struct {
	Commit string `json:"commit"`
	Path   string `json:"path"`
}

type Plan struct {
	Source             Source                  `json:"source"`
	FormatVersion      int                     `json:"format_version"`
	CompilerID         string                  `json:"compiler_id"`
	StructuralPolicyID string                  `json:"structural_policy_id"`
	Authored           json.RawMessage         `json:"authored"`
	AuthoredSpecHash   string                  `json:"authored_spec_hash"`
	Operations         []Operation             `json:"operations"`
	Pins               []model.PinnedODCSPort  `json:"pins"`
	Projection         *odcs.ProjectionPlan    `json:"projection,omitempty"`
	ProjectionDigest   string                  `json:"projection_digest,omitempty"`
	ProjectionInputs   *odcs.ProjectionRequest `json:"projection_inputs,omitempty"`
}

// Compile freezes every input before computing identity. Authored identity is
// deliberately independent of saved-operation, projection and compiler changes.
func Compile(input Input) ([]byte, string, error) {
	authored, err := canonical(input.Authored)
	if err != nil {
		return nil, "", ErrInvalid.With(err)
	}
	pipeline, authoredHash, err := authoredIdentity(authored)
	if err != nil {
		return nil, "", err
	}
	if err := pipelineruntime.ValidatePipelineExecution(pipeline); err != nil {
		return nil, "", err
	}
	p := Plan{
		Source: input.Source, FormatVersion: FormatVersion, CompilerID: CompilerID, StructuralPolicyID: StructuralPolicyID,
		Authored: authored, AuthoredSpecHash: authoredHash, Operations: slices.Clone(input.Operations), Pins: slices.Clone(input.Pins),
	}
	for i := range p.Operations {
		content, err := canonical(p.Operations[i].Content)
		if err != nil {
			return nil, "", ErrInvalid.With(err)
		}
		var operation OperationContent
		if err := json.Unmarshal(content, &operation); err != nil {
			return nil, "", ErrInvalid.With(err)
		}
		var statement struct {
			Statement string `json:"statement"`
		}
		if err := json.Unmarshal(operation.Data, &statement); err != nil {
			return nil, "", ErrInvalid.With(err)
		}
		incremental := false
		for _, step := range pipeline.Steps {
			if step.Slug == p.Operations[i].Step && step.Query != nil {
				incremental = step.Query.Incremental != nil
			}
		}
		if err := pipelineruntime.ValidateOperationReferences(statement.Statement, p.Operations[i].Role == "sink", incremental); err != nil {
			return nil, "", xerror.Wrapf("compile step %s operation: %w", p.Operations[i].Step, err)
		}
		p.Operations[i].Content, p.Operations[i].Digest = content, digest(content)
	}
	slices.SortFunc(p.Operations, func(a, b Operation) int {
		if a.Step != b.Step {
			return strings.Compare(a.Step.String(), b.Step.String())
		}
		return strings.Compare(a.Role, b.Role)
	})
	slices.SortFunc(p.Pins, func(a, b model.PinnedODCSPort) int {
		if a.StepSlug != b.StepSlug {
			return strings.Compare(a.StepSlug.String(), b.StepSlug.String())
		}
		return strings.Compare(a.Port, b.Port)
	})
	if input.Projection != nil {
		projection, err := odcs.CompileProjection(*input.Projection)
		if err != nil {
			return nil, "", err
		}
		if projection.Plan == nil || len(projection.Diagnostics) != 0 {
			return nil, "", ErrInvalid
		}
		p.Projection, p.ProjectionDigest = projection.Plan, projection.Digest
		p.ProjectionInputs = input.Projection
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, "", ErrInvalid.With(err)
	}
	raw, err = canonical(raw)
	if err != nil {
		return nil, "", ErrInvalid.With(err)
	}
	id := digest(raw)
	if _, err = Read(raw, id); err != nil {
		return nil, "", err
	}
	return raw, id, nil
}

// Read uses only stored material. In particular, it never reruns the compiler,
// so a future compiler cannot reinterpret a stored snapshot.
func Read(raw []byte, expectedDigest string) (*Plan, error) {
	if len(raw) > odcs.MaxProjectionBytes {
		return nil, ErrInvalid
	}
	encoded, err := canonical(raw)
	if err != nil || digest(encoded) != expectedDigest {
		return nil, ErrInvalid.With(err)
	}
	var p Plan
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return nil, ErrInvalid.With(err)
	}
	supportedCompiler := p.supportsOperations() || p.FormatVersion == 1 && p.CompilerID == "braidplane/contract-plan/v1"
	if !supportedCompiler || p.StructuralPolicyID != StructuralPolicyID {
		return nil, ErrInvalid
	}
	if !validSource(p.Source) {
		return nil, ErrInvalid
	}
	pipeline, authoredHash, err := authoredIdentity(p.Authored)
	if err != nil || authoredHash != p.AuthoredSpecHash {
		return nil, ErrInvalid.With(err)
	}
	if err := validateOperations(pipeline, p.Operations); err != nil {
		return nil, err
	}
	if err := p.validateResourceScopes(); err != nil {
		return nil, err
	}

	if p.supportsOperations() {
		if err := resolvePins(pipeline, p.Pins); err != nil {
			return nil, err
		}
		if err := ApplyOperationDefaults(pipeline, p.Operations); err != nil {
			return nil, err
		}
		if err := pipelinevalidate.ValidateArtifactStructure(pipeline); err != nil {
			return nil, err
		}
		if err := pipelinevalidate.ValidateStepInputCompatibility(pipeline); err != nil {
			return nil, err
		}
	} else if err := validatePins(pipeline, p.Pins); err != nil {
		return nil, err
	}

	if p.Projection == nil {
		if p.ProjectionDigest != "" || p.ProjectionInputs != nil {
			return nil, ErrInvalid
		}
	} else {
		// ODCS projection digests use the typed compiler encoding, whose order
		// differs from canonical storage encoding.
		projection, err := json.Marshal(p.Projection)
		if err != nil || digest(projection) != p.ProjectionDigest || p.Projection.CompilerID != odcs.ProjectionCompilerID || len(p.Projection.Inputs) == 0 || p.ProjectionInputs == nil || len(p.ProjectionInputs.Inputs) != len(p.Projection.Inputs) {
			return nil, ErrInvalid.With(err)
		}
		for i, input := range p.Projection.Inputs {
			source := p.ProjectionInputs.Inputs[i]
			if source.Contract != input.Contract || source.Revision != input.Revision || source.ObjectID != input.ObjectID || source.Digest != input.Digest {
				return nil, ErrInvalid
			}
			if input.Contract == "" || input.Revision == "" || input.ObjectID == "" || input.Mapping.ID == "" || !validDigest(input.Digest) || !validDigest(input.ResolvedDigest) || payload.ValidateStructure(input.Structure, payload.DefaultStructuralLimits()) != nil {
				return nil, ErrInvalid
			}
		}
	}
	if err := p.validateExecutablePins(); err != nil {
		return nil, err
	}
	return &p, nil
}

func (p *Plan) supportsOperations() bool {
	return p.FormatVersion == FormatVersion && p.CompilerID == CompilerID || p.FormatVersion == 3 && p.CompilerID == "braidplane/contract-plan/v3" || p.FormatVersion == 2 && p.CompilerID == "braidplane/contract-plan/v2"
}

func canonical(raw []byte) ([]byte, error) {
	if len(raw) == 0 || len(raw) > odcs.MaxProjectionBytes {
		return nil, ErrInvalid
	}
	var value any
	if err := jsonvalue.Unmarshal(raw, &value); err != nil {
		return nil, ErrInvalid.With(err)
	}
	return json.Marshal(value)
}

func digest(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }

func authoredIdentity(raw []byte) (*pipelinespec.Pipeline, string, error) {
	var pipeline pipelinespec.Pipeline
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pipeline); err != nil {
		return nil, "", ErrInvalid.With(err)
	}
	encoded, err := json.Marshal(pipeline)
	if err != nil {
		return nil, "", ErrInvalid.With(err)
	}
	// Match the canonical author's existing spec hash, including its encoding.
	return &pipeline, fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}

func validDigest(value string) bool {
	if len(value) != 71 || value[:7] != "sha256:" {
		return false
	}
	return strings.Trim(value[7:], "0123456789abcdef") == ""
}

func validSource(source Source) bool {
	if len(source.Commit) != 40 && len(source.Commit) != 64 {
		return false
	}
	if strings.Trim(source.Commit, "0123456789abcdef") != "" {
		return false
	}
	return source.Path != "" && source.Path != "." && source.Path != ".." && !strings.HasPrefix(source.Path, "../") && !path.IsAbs(source.Path) && path.Clean(source.Path) == source.Path && !strings.ContainsAny(source.Path, "\\\x00")
}

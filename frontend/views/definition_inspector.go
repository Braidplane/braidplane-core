package views

import (
	"bytes"
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	api "github.com/Braidplane/braidplane-core/api-schema/gen/client"
	"github.com/Braidplane/braidplane-core/pkg/slug"
)

const DefinitionContentLimit = 1024 * 1024

type DefinitionInspection struct {
	Authority, Hash, Message, Notice, Commit, Created, SelectedStep string
	Spec                                                            *api.PipelineSpec
}

type definitionInspectionKey struct{}

func WithDefinitionInspection(ctx context.Context, inspection DefinitionInspection) context.Context {
	return context.WithValue(ctx, definitionInspectionKey{}, inspection)
}

func definitionInspection(ctx context.Context) DefinitionInspection {
	inspection, _ := ctx.Value(definitionInspectionKey{}).(DefinitionInspection)
	return inspection
}

type definitionView struct {
	DefinitionInspection
	Steps                                                 []PipelineSpecStepProjection
	Selected                                              *PipelineSpecStepProjection
	Step, Input, Port, Serialized, View, SelectionMessage string
}

func definitionInspectorView(ctx context.Context) definitionView {
	d := definitionView{DefinitionInspection: definitionInspection(ctx)}
	q := entityPageURL(ctx).Query()
	d.Step, d.Input, d.Port, d.View = q.Get("lineage_step"), q.Get("definition_input"), q.Get("definition_port"), q.Get("definition_view")
	if d.Step == "" && q.Get("definition_scope") != "whole" {
		d.Step = d.SelectedStep
	}
	if d.Spec == nil {
		return d
	}
	raw, err := json.Marshal(d.Spec)
	if err != nil {
		d.Message = "Definition content could not be decoded."
		d.Spec = nil
		return d
	}
	if len(raw) > DefinitionContentLimit {
		d.Message = "Definition content exceeds the 1 MiB inspection limit."
		d.Spec = nil
		return d
	}
	projection := PipelineSpecProjectionFromPipeline(&api.Pipeline{Spec: *d.Spec})
	d.Steps = append(d.Steps, projection.SourceSteps...)
	d.Steps = append(d.Steps, projection.TransformSteps...)
	d.Steps = append(d.Steps, projection.SinkSteps...)
	sort.Slice(d.Steps, func(i, j int) bool { return d.Steps[i].SpecPosition < d.Steps[j].SpecPosition })
	if d.Step != "" {
		for i := range d.Steps {
			if d.Steps[i].Slug == d.Step {
				d.Selected = &d.Steps[i]
				raw, err = json.Marshal(d.Spec.Steps[d.Selected.SpecPosition-1])
				break
			}
		}
		if d.Selected == nil {
			d.SelectionMessage = "Step unavailable in this version."
			return d
		}
		if d.Input != "" {
			found := false
			for _, input := range d.Selected.Inputs {
				if input.Name == d.Input {
					found = true
				}
			}
			if !found {
				d.SelectionMessage = "Input unavailable in this step."
			}
		}
		if d.Port != "" {
			found := false
			for _, slot := range d.Selected.ContractSlots {
				if (d.Port == "input" && strings.HasPrefix(slot.Label, "Input")) || (d.Port == "output" && strings.HasPrefix(slot.Label, "Output")) {
					found = true
				}
			}
			if !found {
				d.SelectionMessage = "Contract port unavailable in this step."
			}
		}
	}
	if err == nil {
		var pretty bytes.Buffer
		if json.Indent(&pretty, raw, "", "  ") == nil {
			d.Serialized = pretty.String()
		}
	}
	return d
}

// Keep navigation to identifiers and presentation state; never carry arbitrary query values.
func DefinitionHref(rawURL, step, input, port, view string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	values := url.Values{}
	for _, key := range []string{"environment_slug", "definition", "spec_hash", "list_return"} {
		if value := u.Query().Get(key); value != "" {
			values.Set(key, value)
		}
	}
	if strings.Contains(u.Path, "/runs/") {
		values.Set("section", "definition")
	} else {
		values.Set("section", "workflow")
	}
	if step != "" {
		values.Set("lineage_step", step)
	}
	if input != "" {
		values.Set("definition_input", input)
	}
	if port != "" {
		values.Set("definition_port", port)
	}
	if view == "serialized" {
		values.Set("definition_view", view)
	}
	if step == "" {
		values.Set("definition_scope", "whole")
	}
	u.RawQuery, u.Fragment = values.Encode(), "definition-inspector"
	return u.String()
}

func definitionHref(ctx context.Context, step, input, port, view string) string {
	d := definitionInspection(ctx)
	u := entityPageURL(ctx)
	q := u.Query()
	if d.Hash != "" {
		q.Set("spec_hash", d.Hash)
	}
	u.RawQuery = q.Encode()
	return DefinitionHref(u.String(), step, input, port, view)
}

func definitionContractHref(ctx context.Context, org, project, step string, slot PipelineSpecContractSlot) string {
	if slot.Ref == nil {
		return ""
	}
	u, _ := url.Parse(ODCSCatalogHref(org, project, slot.Ref.ContractSlug, slot.Ref.RevisionSlug, false, ""))
	q := u.Query()
	q.Set("object", slot.Ref.SchemaObjectId)
	input, port := "", "output"
	if strings.HasPrefix(slot.Label, "Input") {
		port = "input"
	}
	if strings.HasPrefix(slot.Label, "Input requirement: ") {
		input = strings.TrimPrefix(slot.Label, "Input requirement: ")
	}
	q.Set("definition_return", definitionHref(ctx, step, input, port, entityPageURL(ctx).Query().Get("definition_view")))
	u.RawQuery = q.Encode()
	return u.String()
}

func definitionAuthorityHref(rawURL, authority, hash string) string {
	u, _ := url.Parse(DefinitionHref(rawURL, "", "", "", "structured"))
	q := u.Query()
	q.Set("definition", authority)
	q.Del("spec_hash")
	if hash != "" {
		q.Set("spec_hash", hash)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// Return only to this project's definition, proposal or run; strip non-inspection query values.
func DefinitionReturnHref(raw, org, project string) string {
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || u.User != nil || strings.ContainsAny(raw, "\\\r\n") {
		return ""
	}
	prefix := "/orgs/" + org + "/projects/" + project + "/"
	if !strings.HasPrefix(u.Path, prefix) {
		return ""
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, prefix), "/")
	if len(parts) != 2 || (parts[0] != "pipelines" && parts[0] != "runs" && parts[0] != "pipeline-creations") {
		return ""
	}
	if _, err := slug.New(parts[1]); err != nil {
		return ""
	}
	q := u.Query()
	return DefinitionHref(u.String(), q.Get("lineage_step"), q.Get("definition_input"), q.Get("definition_port"), q.Get("definition_view"))
}

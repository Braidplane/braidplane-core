package odcs

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
)

// RelationshipSelection explicitly maps one declaration to an input containing
// an exact, caller-authorized project-owned revision. URLs never select inputs.
type RelationshipSelection struct {
	Input, Target int
	Path          string
}

type ProjectedKey struct {
	Path payload.StructuralPath
	IDs  []string
}

type ProjectedKeyPair struct{ From, To ProjectedKey }

type ProjectedRelationship struct {
	Input, Target int
	Path          string
	Pairs         []ProjectedKeyPair
}

type projectionRelationship struct {
	declaration map[string]any
	implicit    payload.StructuralPath
}

func (c *projectionCompiler) collectRelationships(p map[string]any, path string, dataPath payload.StructuralPath) {
	if p == nil {
		return
	}
	entries, _ := p["relationships"].([]any)
	for i, entry := range entries {
		locator := fmt.Sprintf("%s.relationships[%d]", path, i)
		relation := entry.(map[string]any)
		c.relations[locator] = projectionRelationship{declaration: relation, implicit: dataPath}
		if c.selectedRelationships[locator] {
			for _, key := range []string{"from", "to", "type"} {
				if value, exists := relation[key]; exists {
					relation[key] = c.resolve(value, locator+"."+key)
				}
			}
		}
	}
	children, _ := p["properties"].([]any)
	for i, child := range children {
		property := child.(map[string]any)
		name, _ := property["name"].(string)
		next := append(slices.Clone(dataPath), payload.StructuralSegment{Kind: payload.StructuralPropertySegment, Name: name})
		c.collectRelationships(property, fmt.Sprintf("%s.properties[%d]", path, i), next)
	}
	if items, ok := p["items"].(map[string]any); ok {
		c.collectRelationships(items, path+".items", append(slices.Clone(dataPath), payload.StructuralSegment{Kind: payload.StructuralItemSegment}))
	}
	if mapping, ok := p["map"].(map[string]any); ok {
		for _, side := range []struct {
			key  string
			kind payload.StructuralSegmentKind
		}{{"key", payload.StructuralKeySegment}, {"value", payload.StructuralValueSegment}} {
			if child, ok := mapping[side.key].(map[string]any); ok {
				c.collectRelationships(child, path+".map."+side.key, append(slices.Clone(dataPath), payload.StructuralSegment{Kind: side.kind}))
			}
		}
	}
}

func compileRelationships(selections []RelationshipSelection, compilers []*projectionCompiler, plan *ProjectionPlan) {
	seen := map[RelationshipSelection]bool{}
	declarations := map[string]bool{}
	for _, selection := range selections {
		if selection.Input < 0 || selection.Input >= len(compilers) || selection.Target < 0 || selection.Target >= len(compilers) {
			compilers[0].fail("relationship_target_invalid", "$.schema", "relationship")
			continue
		}
		c, target := compilers[selection.Input], compilers[selection.Target]
		declarationKey := fmt.Sprintf("%d:%s", selection.Input, selection.Path)
		if seen[selection] || declarations[declarationKey] {
			c.fail("relationship_ambiguous", "$.schema", "relationship")
			continue
		}
		seen[selection], declarations[declarationKey] = true, true
		relation, exists := c.relations[selection.Path]
		if !exists {
			// Do not echo an arbitrary caller-supplied path in diagnostics.
			c.fail("relationship_not_found", "$.schema", "relationship")
			continue
		}
		if c.output.Structure == nil || target.output.Structure == nil {
			c.fail("relationship_target_invalid", selection.Path, "relationship")
			continue
		}
		from, to := relationshipReferences(relation.declaration["from"]), relationshipReferences(relation.declaration["to"])
		if relation.implicit != nil {
			from = []string{""}
		}
		if len(from) == 0 || len(from) != len(to) {
			c.fail("relationship_pairing_invalid", selection.Path, "relationship")
			continue
		}
		projected := ProjectedRelationship{Input: selection.Input, Target: selection.Target, Path: selection.Path}
		pairs := map[string]bool{}
		for i := range from {
			left, lt := c.relationshipKey(from[i], relation.implicit, selection.Path)
			right, rt := target.relationshipKey(to[i], nil, "$.schema")
			if lt == nil || rt == nil || !relationshipTypesCompatible(c.output.Structure, lt, rt) {
				c.fail("relationship_key_invalid", selection.Path, "relationship")
				continue
			}
			pair := ProjectedKeyPair{From: left, To: right}
			raw, err := json.Marshal(pair)
			if err != nil {
				c.fail("relationship_key_invalid", selection.Path, "relationship")
				continue
			}
			if pairs[string(raw)] {
				c.fail("relationship_ambiguous", selection.Path, "relationship")
			}
			pairs[string(raw)] = true
			projected.Pairs = append(projected.Pairs, pair)
		}
		if _, exists := relation.declaration["type"]; !exists {
			c.output.Defaults = append(c.output.Defaults, ProjectionDefault{Path: selection.Path + ".type", Source: "ODCS/v3.2.0", Value: "foreignKey"})
		}
		c.mapped[selection.Path] = true
		for _, key := range []string{"from", "to", "type"} {
			c.mapped[selection.Path+"."+key] = true
		}
		plan.Relationships = append(plan.Relationships, projected)
	}
	// Graph references are sorted; pair order remains authored. No recursion or I/O.
	slices.SortFunc(plan.Relationships, func(a, b ProjectedRelationship) int {
		if a.Input != b.Input {
			return a.Input - b.Input
		}
		return strings.Compare(a.Path, b.Path)
	})
}

func relationshipReferences(value any) []string {
	if text, ok := value.(string); ok {
		return []string{text}
	}
	var refs []string
	if values, ok := value.([]any); ok {
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				return nil
			}
			refs = append(refs, text)
		}
	}
	return refs
}

func (c *projectionCompiler) relationshipKey(reference string, implicit payload.StructuralPath, locator string) (ProjectedKey, *payload.StructuralType) {
	s, objectName := c.output.Structure, c.output.ObjectName
	var key ProjectedKey
	t := s.Root
	if implicit != nil {
		for _, segment := range implicit {
			if t == nil {
				return key, nil
			}
			if segment.Kind != payload.StructuralPropertySegment {
				return key, nil
			}
			var found *payload.StructuralType
			for _, p := range t.Properties {
				if !c.spend(1, locator) {
					return key, nil
				}
				if p.Name == segment.Name {
					key.Path, key.IDs, found = append(key.Path, segment), append(key.IDs, p.ID), p.Type
				}
			}
			if found == nil {
				return key, nil
			}
			t = found
		}
		return key, t
	}
	// An explicit mapping authorizes the local target, never a fetch of this prefix.
	if _, fragment, qualified := strings.Cut(reference, "#"); qualified {
		reference = fragment
	}
	parts := strings.Split(strings.TrimPrefix(reference, "/"), "/")
	byID := len(parts) >= 4 && parts[0] == "schema" && parts[1] == s.Object && len(parts)%2 == 0
	var selectors []string
	if byID {
		for i := 2; i < len(parts); i += 2 {
			if parts[i] != "properties" {
				return key, nil
			}
			selectors = append(selectors, parts[i+1])
		}
	} else {
		parts = strings.Split(reference, ".")
		if len(parts) < 2 || parts[0] != objectName {
			return key, nil
		}
		selectors = parts[1:]
	}
	for _, selector := range selectors {
		if t == nil || t.Kind != payload.StructuralObject {
			return key, nil
		}
		var found *payload.StructuralType
		for _, p := range t.Properties {
			if !c.spend(1, locator) {
				return key, nil
			}
			if byID && p.ID != "" && p.ID == selector || !byID && p.Name == selector {
				if found != nil {
					return key, nil
				}
				key.Path = append(key.Path, payload.StructuralSegment{Kind: payload.StructuralPropertySegment, Name: p.Name})
				key.IDs, found = append(key.IDs, p.ID), p.Type
			}
		}
		if found == nil {
			return key, nil
		}
		t = found
	}
	return key, t
}

func relationshipTypesCompatible(s *payload.Structure, left, right *payload.StructuralType) bool {
	if left.Kind == payload.StructuralObject || left.Kind == payload.StructuralArray || left.Kind == payload.StructuralMap || left.Kind == payload.StructuralVector {
		return false
	}
	a, b := *s, *s
	lt, rt := *left, *right
	lt.Nullable, rt.Nullable = false, false
	a.Root, b.Root = &lt, &rt
	return payload.CheckStructuralCompatibility(&a, &b, payload.DefaultStructuralLimits()) == nil
}

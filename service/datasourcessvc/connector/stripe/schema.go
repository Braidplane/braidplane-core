// Package stripe implements bounded revenue reads from the Stripe API.
package stripe

import (
	"context"
	"slices"

	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
)

type SchemaIntrospector interface {
	IntrospectSchema(context.Context) (*Schema, error)
}

type Schema struct {
	Objects []ObjectSchema `json:"objects"`
}

type ObjectSchema struct {
	Name       string           `json:"name"`
	Properties []PropertySchema `json:"properties"`
}

type PropertySchema struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type resource struct {
	name, path, prefix string
	fields             []PropertySchema
}

var resources = []resource{
	{"customers", "/v1/customers", "cus_", []PropertySchema{{"name", "string"}, {"email", "string"}, {"currency", "string"}}},
	{"subscriptions", "/v1/subscriptions", "sub_", []PropertySchema{{"customer", "string"}, {"status", "string"}, {"currency", "string"}, {"cancel_at_period_end", "boolean"}}},
	{"invoices", "/v1/invoices", "in_", []PropertySchema{{"customer", "string"}, {"status", "string"}, {"currency", "string"}, {"amount_due", "integer"}, {"amount_paid", "integer"}, {"amount_remaining", "integer"}}},
	{"payments", "/v1/payment_intents", "pi_", []PropertySchema{{"customer", "string"}, {"status", "string"}, {"currency", "string"}, {"amount", "integer"}, {"amount_received", "integer"}}},
}

func (r resource) properties() []PropertySchema {
	return slices.Concat([]PropertySchema{{"id", "string"}, {"created", "integer"}, {"livemode", "boolean"}}, r.fields)
}

func (c *Connector) IntrospectSchema(ctx context.Context) (*Schema, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	schema := &Schema{}
	for _, r := range resources {
		fields := r.properties()
		size := len(r.name)
		for _, f := range fields {
			size += len(f.Name) + len(f.Type)
		}
		if err := schemaquota.Charge(ctx, 1, size); err != nil {
			return nil, err
		}
		schema.Objects = append(schema.Objects, ObjectSchema{r.name, fields})
	}
	return schema, nil
}

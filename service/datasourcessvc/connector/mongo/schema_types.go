package mongo

import (
	"context"
)

// SchemaIntrospector retrieves MongoDB schema metadata.
type SchemaIntrospector interface {
	// IntrospectSchema retrieves the MongoDB schema (collections, fields, types).
	// Samples documents to infer field types since MongoDB is schemaless.
	IntrospectSchema(ctx context.Context) (*Schema, error)
}

// Schema represents the MongoDB schema of a data source.
type Schema struct {
	// Collections is a list of collections in the data source.
	Collections []Collection
}

// Collection represents a single collection in the MongoDB database.
type Collection struct {
	// Name is the name of the collection.
	Name string

	// Fields is a list of fields discovered by sampling documents in the collection.
	Fields []Field

	// DocumentCount is the total number of documents in the collection (optional).
	DocumentCount int64
}

// Field represents a field discovered in sampled documents.
type Field struct {
	// Name is the field name (key in the document).
	Name string

	// Type is the inferred BSON type.
	Type FieldType

	// Nullable indicates whether the field was missing in some sampled documents.
	Nullable bool
}

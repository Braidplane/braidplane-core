package mongo

//go:generate -command enumer go tool -modfile=../../../../tools/go.mod enumer
//go:generate enumer -type=FieldType -transform=snake -output=field_type_string.go -text -linecomment

// FieldType represents the inferred type of a MongoDB field from BSON data.
type FieldType int

const (
	FieldTypeUnknown   FieldType = iota // unknown
	FieldTypeStr                        // string
	FieldTypeInt                        // int
	FieldTypeDouble                     // double
	FieldTypeBoolean                    // boolean
	FieldTypeObjectID                   // objectId
	FieldTypeDate                       // date
	FieldTypeTimestamp                  // timestamp
	FieldTypeObject                     // object
	FieldTypeArray                      // array
	FieldTypeBinary                     // binary
	FieldTypeDecimal                    // decimal
	FieldTypeRegex                      // regex
	FieldTypeNull                       // null
	FieldTypeMixed                      // mixed
)

package filedata

import (
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/nativetype"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// InferCSVFields infers field names from CSV content column headers.
// All inferred CSV fields have type string; headless CSVs get an additional csv_row array field.
func InferCSVFields(content []byte) ([]Field, error) {
	r, err := ParseCSV(content, ParseCSVOptions{})
	if err != nil {
		return nil, err
	}

	fields := make([]Field, 0, len(r.ColumnNames))
	for _, column := range r.ColumnNames {
		if strings.TrimSpace(column) == "" {
			continue
		}
		fields = append(fields, Field{Name: column, Type: nativetype.TypeString})
	}

	if r.IsHeadlessCSV {
		fields = append(fields, Field{Name: "csv_row", Type: nativetype.TypeArray})
	}

	return fields, nil
}

// InferJSONFields infers field paths and types from JSON content.
// Nested paths use dot notation; array fields carry a [] suffix.
func InferJSONFields(content []byte) ([]Field, error) {
	var raw any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, xerror.Unexpected().With(err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, xerror.Unexpected().Detail("JSON content must contain exactly one value").With(err)
	}

	fieldTypes := map[string]string{}
	collectJSONFieldTypes("", raw, fieldTypes)

	if len(fieldTypes) == 0 {
		return []Field{}, nil
	}

	names := make([]string, 0, len(fieldTypes))
	for name := range fieldTypes {
		names = append(names, name)
	}
	sort.Strings(names)

	fields := make([]Field, 0, len(names))
	for _, name := range names {
		fields = append(fields, Field{Name: name, Type: fieldTypes[name]})
	}

	return fields, nil
}

func collectJSONFieldTypes(prefix string, value any, fieldTypes map[string]string) {
	switch typed := value.(type) {
	case map[string]any:
		if prefix != "" && !strings.HasSuffix(prefix, "[]") {
			setFieldType(fieldTypes, prefix, nativetype.TypeObject)
		}
		for key, nested := range typed {
			next := key
			if prefix != "" {
				next = prefix + "." + key
			}
			collectJSONFieldTypes(next, nested, fieldTypes)
		}
	case []any:
		if prefix == "" {
			for _, item := range typed {
				collectJSONFieldTypes("", item, fieldTypes)
			}
			return
		}
		arrayPrefix := prefix + "[]"
		setFieldType(fieldTypes, arrayPrefix, nativetype.TypeArray)
		for _, item := range typed {
			collectJSONFieldTypes(arrayPrefix, item, fieldTypes)
		}
	case string:
		if strings.HasSuffix(prefix, "[]") {
			return
		}
		setFieldType(fieldTypes, prefix, nativetype.TypeString)
	case bool:
		if strings.HasSuffix(prefix, "[]") {
			return
		}
		setFieldType(fieldTypes, prefix, nativetype.TypeBoolean)
	case json.Number:
		if strings.HasSuffix(prefix, "[]") {
			return
		}
		if _, err := typed.Int64(); err == nil {
			setFieldType(fieldTypes, prefix, nativetype.TypeInteger)
		} else {
			setFieldType(fieldTypes, prefix, nativetype.TypeNumber)
		}
	case float64:
		if strings.HasSuffix(prefix, "[]") {
			return
		}
		if typed == float64(int64(typed)) {
			setFieldType(fieldTypes, prefix, nativetype.TypeInteger)
			return
		}
		setFieldType(fieldTypes, prefix, nativetype.TypeNumber)
	case nil:
		if strings.HasSuffix(prefix, "[]") {
			return
		}
		setFieldType(fieldTypes, prefix, nativetype.TypeNull)
	default:
		if strings.HasSuffix(prefix, "[]") {
			return
		}
		setFieldType(fieldTypes, prefix, inferPrimitiveType(typed))
	}
}

func inferPrimitiveType(value any) string {
	switch value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return nativetype.TypeInteger
	case float32, float64:
		return nativetype.TypeNumber
	case bool:
		return nativetype.TypeBoolean
	case string:
		return nativetype.TypeString
	default:
		return nativetype.TypeString
	}
}

// setFieldType sets or merges a field type in fieldTypes.
// Null is promoted to the actual type; integer+number collapses to number;
// all other conflicts default to string.
func setFieldType(fieldTypes map[string]string, name, typ string) {
	if strings.TrimSpace(name) == "" {
		return
	}

	existing, ok := fieldTypes[name]
	if !ok {
		fieldTypes[name] = typ
		return
	}

	fieldTypes[name] = nativetype.MergeJSONLike(existing, typ)
}

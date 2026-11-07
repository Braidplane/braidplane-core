package remotefile

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/nativetype"
	"github.com/Braidplane/braidplane-core/service/datasourcessvc/connector/filedata"
)

const schemaDiagnosticSeverityWarning = "warning"

// SchemaProvider lists and samples candidate files through a provider-owned transport.
type SchemaProvider interface {
	ListFiles(context.Context) ([]SchemaFile, error)
	ReadFile(context.Context, string, int64) (*ReadResult, error)
}

// SchemaFile names one provider file candidate and the target used to read it.
type SchemaFile struct {
	Name      string
	Target    string
	SizeBytes *int64
}

// SchemaOptions controls schema sampling guardrails and compatibility behavior.
type SchemaOptions struct {
	MaxBytes int64
}

// IntrospectSchema samples provider-listed files and maps them into normalized schema metadata.
func (adapter Adapter) IntrospectSchema(
	ctx context.Context,
	provider SchemaProvider,
	opts SchemaOptions,
) (*filedata.Schema, error) {
	candidates, err := provider.ListFiles(ctx)
	if err != nil {
		return nil, err
	}

	files := make([]filedata.File, 0, len(candidates))
	diagnostics := make([]filedata.SchemaDiagnostic, 0)
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file, diagnostic, ok := adapter.introspectFile(ctx, provider, candidate, opts)
		if !ok {
			file = filedata.File{Name: candidate.Name, Fields: []filedata.Field{}}
		}
		file.SizeBytes = candidate.SizeBytes
		file.MIMEType = filedata.MIMETypeForPath(candidate.Target, "application/octet-stream")
		if diagnostic.Code != "" {
			diagnostics = append(diagnostics, diagnostic)
		}
		files = append(files, file)
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Name < files[j].Name
	})

	return &filedata.Schema{Files: files, Diagnostics: diagnostics}, nil
}

// introspectFile samples one provider file and returns a warning when the sample is skipped.
func (adapter Adapter) introspectFile(
	ctx context.Context,
	provider SchemaProvider,
	candidate SchemaFile,
	opts SchemaOptions,
) (filedata.File, filedata.SchemaDiagnostic, bool) {
	ext := strings.ToLower(filepath.Ext(candidate.Target))
	if strings.HasSuffix(candidate.Target, "/") ||
		(ext != "" && ext != ".csv" && ext != ".json" && !filedata.IsSpreadsheetExtension(candidate.Target)) {
		return filedata.File{Name: candidate.Name, Fields: []filedata.Field{}}, filedata.SchemaDiagnostic{}, true
	}
	readLimit := opts.MaxBytes
	if schemaquota.Attached(ctx) {
		remaining := schemaquota.RemainingBytes(ctx, int(opts.MaxBytes))
		if remaining <= 0 {
			return filedata.File{}, schemaDiagnostic(candidate, "budget_exhausted", "Fields were not inferred because the schema byte budget was exhausted."), false
		}
		readLimit = int64(remaining)
	}
	content, err := provider.ReadFile(ctx, candidate.Target, readLimit)
	if err != nil {
		return filedata.File{}, schemaDiagnostic(
			candidate,
			"read_failed",
			"Fields were not inferred because the sample could not be read.",
		), false
	}
	if opts.MaxBytes > 0 && int64(len(content.Content)) > opts.MaxBytes {
		return filedata.File{}, schemaDiagnostic(
			candidate,
			"sample_too_large",
			fmt.Sprintf("Fields were not inferred because the sample exceeded the %d byte limit.", opts.MaxBytes),
		), false
	}
	if err := schemaquota.Charge(ctx, 0, len(content.Content)); err != nil {
		return filedata.File{}, schemaDiagnostic(candidate, "budget_exhausted", "Fields were not inferred because the schema byte budget was exhausted."), false
	}

	if ext == "" && (!utf8.Valid(content.Content) ||
		!strings.HasPrefix(http.DetectContentType(content.Content), "text/plain")) {
		return filedata.File{Name: candidate.Name, Fields: []filedata.Field{}}, filedata.SchemaDiagnostic{}, true
	}

	fields, ok, err := adapter.inferSchemaFields(candidate.Target, content.Content, readLimit)
	if err != nil {
		return filedata.File{}, schemaDiagnostic(
			candidate,
			"parse_failed",
			"Fields were not inferred because the sample could not be parsed.",
		), false
	}
	if !ok {
		diagnostic := schemaDiagnostic(
			candidate,
			"unsupported_format",
			"File sample did not match a supported schema inference format.",
		)
		return filedata.File{}, diagnostic, false
	}

	return filedata.File{Name: candidate.Name, Fields: fields}, filedata.SchemaDiagnostic{}, true
}

// inferSchemaFields detects a sample format and infers file fields for schema views.
func (adapter Adapter) inferSchemaFields(
	target string,
	content []byte,
	maxBytes int64,
) ([]filedata.Field, bool, error) {
	format, ok := detectSchemaFormat(target, content)
	if !ok {
		return nil, false, nil
	}

	if adapter.SchemaParsedColumns {
		return adapter.inferParsedColumnFields(target, content, format, maxBytes)
	}

	switch format {
	case filedata.QueryFormatCSV:
		fields, err := filedata.InferCSVFields(content)
		return fields, true, err
	case filedata.QueryFormatJSON:
		fields, err := filedata.InferJSONFields(content)
		return fields, true, err
	case filedata.QueryFormatSpreadsheet:
		fields, err := filedata.InferSpreadsheetFields(
			content,
			filedata.SpreadsheetOptions{FilePath: adapter.spreadsheetPath(target), MaxBytes: maxBytes},
		)
		return fields, true, err
	case filedata.QueryFormatFile:
		return nil, false, nil
	default:
		return nil, false, nil
	}
}

// detectSchemaFormat reports whether a file sample has a supported detectable format.
func detectSchemaFormat(target string, content []byte) (filedata.QueryFormat, bool) {
	format, err := filedata.DetectFormat(filedata.QueryFormatAuto, target, content)
	return format, err == nil
}

// inferParsedColumnFields preserves connectors whose schema fields come from normalized result columns.
func (adapter Adapter) inferParsedColumnFields(
	target string,
	content []byte,
	format filedata.QueryFormat,
	maxBytes int64,
) ([]filedata.Field, bool, error) {
	var fileResult *result.FileResult
	var err error
	switch format {
	case filedata.QueryFormatCSV:
		fileResult, err = filedata.ParseCSV(content, filedata.ParseCSVOptions{})
	case filedata.QueryFormatJSON:
		fileResult, err = filedata.ParseJSON(content)
	case filedata.QueryFormatSpreadsheet:
		fileResult, err = filedata.ParseSpreadsheet(
			content,
			filedata.SpreadsheetOptions{FilePath: adapter.spreadsheetPath(target), MaxBytes: maxBytes},
		)
	case filedata.QueryFormatFile:
		return nil, false, nil
	default:
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}

	fields := make([]filedata.Field, 0, len(fileResult.ColumnNames))
	for _, name := range fileResult.ColumnNames {
		fields = append(fields, filedata.Field{Name: name, Type: nativetype.TypeString})
	}

	return fields, true, nil
}

// schemaDiagnostic builds a safe non-fatal schema sampling warning for one candidate.
func schemaDiagnostic(candidate SchemaFile, code, message string) filedata.SchemaDiagnostic {
	return filedata.SchemaDiagnostic{
		Name:     candidate.Name,
		Target:   candidate.Target,
		Severity: schemaDiagnosticSeverityWarning,
		Code:     code,
		Message:  message,
	}
}

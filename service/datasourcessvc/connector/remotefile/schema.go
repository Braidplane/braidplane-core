package remotefile

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/Braidplane/braidplane-core/pkg/connector/result"
	"github.com/Braidplane/braidplane-core/pkg/connector/schemaquota"
	"github.com/Braidplane/braidplane-core/pkg/nativetype"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
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
	format, err := filedata.DetectFormat(filedata.QueryFormatAuto, candidate.Target)
	if err != nil || strings.HasSuffix(candidate.Target, "/") {
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

	fields, err := adapter.inferSchemaFields(candidate.Target, content.Content, format, readLimit)
	if err != nil {
		return filedata.File{}, schemaDiagnostic(
			candidate,
			"parse_failed",
			"Fields were not inferred because the sample could not be parsed.",
		), false
	}
	return filedata.File{Name: candidate.Name, Fields: fields}, filedata.SchemaDiagnostic{}, true
}

// inferSchemaFields parses a sample using the format selected from its extension.
func (adapter Adapter) inferSchemaFields(
	target string,
	content []byte,
	format filedata.QueryFormat,
	maxBytes int64,
) ([]filedata.Field, error) {
	if adapter.SchemaParsedColumns {
		return adapter.inferParsedColumnFields(target, content, format, maxBytes)
	}

	switch format {
	case filedata.QueryFormatCSV:
		return filedata.InferCSVFields(content)
	case filedata.QueryFormatJSON:
		return filedata.InferJSONFields(content)
	case filedata.QueryFormatSpreadsheet:
		return filedata.InferSpreadsheetFields(
			content,
			filedata.SpreadsheetOptions{FilePath: target, MaxBytes: maxBytes},
		)
	default:
		return nil, xerror.Unexpected().Detail("unsupported schema sample format")
	}
}

// inferParsedColumnFields preserves connectors whose schema fields come from normalized result columns.
func (adapter Adapter) inferParsedColumnFields(
	target string,
	content []byte,
	format filedata.QueryFormat,
	maxBytes int64,
) ([]filedata.Field, error) {
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
			filedata.SpreadsheetOptions{FilePath: target, MaxBytes: maxBytes},
		)
	default:
		return nil, xerror.Unexpected().Detail("unsupported schema sample format")
	}
	if err != nil {
		return nil, err
	}

	fields := make([]filedata.Field, 0, len(fileResult.ColumnNames))
	for _, name := range fileResult.ColumnNames {
		fields = append(fields, filedata.Field{Name: name, Type: nativetype.TypeString})
	}

	return fields, nil
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

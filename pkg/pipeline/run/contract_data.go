package run

import (
	"github.com/Braidplane/braidplane-core/pkg/pipeline/payload"
	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

func ContractData(source *StepResult) ([]string, []map[string]any, error) {
	if source == nil {
		return nil, nil, nil
	}

	if runtimePayload, ok := source.Payload(); ok {
		return payload.ContractData(runtimePayload)
	}

	if httpResult, ok := source.HTTP(); ok && len(httpResult.Body) > 0 {
		mediaType := detectHTTPMediaType(httpResult.Headers)
		switch mediaType {
		case httpMediaTypeJSON, "":
			rows, columnNames, err := payload.ParseJSONBodyToRowsPreserveNumbers(httpResult.Body)
			return columnNames, rows, err
		case httpMediaTypeCSV, httpMediaTypeCSV2:
			rows, columnNames, err := payload.ParseCSVBodyToRows(httpResult.Body)
			return columnNames, rows, err
		default:
			return nil, nil, xerror.UnsupportedHTTPContentType().Detailf(
				"unsupported HTTP Content-Type: %s; supported types are application/json and text/csv",
				mediaType,
			)
		}
	}

	return nil, nil, nil
}

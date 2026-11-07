//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=ReportType -json -text -linecomment -output=report_type_enumer.go
//go:generate enumer -type=ReportStatus -json -text -linecomment -output=report_status_enumer.go
//go:generate enumer -type=ReportSeverity -json -text -linecomment -output=report_severity_enumer.go
package drift

// ReportType identifies the producer of a drift or impact report.
type ReportType int

const (
	ReportTypeUnknown        ReportType = iota // unknown
	ReportTypeContractImpact                   // contract_impact
	ReportTypeSourceSchema                     // source_schema
)

// ReportStatus identifies whether a report is still current.
type ReportStatus int

const (
	ReportStatusUnknown    ReportStatus = iota // unknown
	ReportStatusOpen                           // open
	ReportStatusSuperseded                     // superseded
)

// ReportSeverity identifies the strongest compatibility effect in a report.
type ReportSeverity int

const (
	ReportSeverityUnknown  ReportSeverity = iota // unknown
	ReportSeveritySafe                           // safe
	ReportSeverityWarning                        // warning
	ReportSeverityBreaking                       // breaking
)

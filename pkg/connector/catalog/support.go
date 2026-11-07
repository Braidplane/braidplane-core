package catalog

// SupportLevel qualifies the documented operation scope, not every provider feature.
// Criteria and evidence live in the public connector support guide.
//
//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=SupportLevel -json -text -linecomment -output=support_level_enumer.go
type SupportLevel int

const (
	SupportLevelUnknown         SupportLevel = iota // unclassified
	SupportLevelExperimental                        // experimental
	SupportLevelPreview                             // preview
	SupportLevelSupported                           // supported
	SupportLevelProductionReady                     // production_ready
	SupportLevelDeprecated                          // deprecated
)

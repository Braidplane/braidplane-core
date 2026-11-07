package model

// ProofMaintenanceHealth is a deployment aggregate, never a tenant job inventory.
type ProofMaintenanceHealth struct {
	LastSuccessTimestamp float64
	RecentFailures       int64
	CancellationDue      int64
	ExpiryDue            int64
	EvidenceDue          int64
	MetadataDue          int64
}

package run

//go:generate -command enumer go tool -modfile=../../../tools/go.mod enumer
//go:generate enumer -type=WorkerLiveness -json -text -linecomment -output=worker_liveness_enumer.go

type WorkerLiveness int

const (
	WorkerLivenessUnavailable WorkerLiveness = iota // unavailable
	WorkerLivenessRecent                            // recent
	WorkerLivenessStale                             // stale
	WorkerLivenessTerminal                          // terminal
)

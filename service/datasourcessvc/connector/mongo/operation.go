//go:generate -command enumer go tool -modfile=../../../../tools/go.mod enumer
//go:generate enumer -type=Operation -transform=snake -output=operation_string.go -text -linecomment
package mongo

// Operation represents a MongoDB operation type.
type Operation int

const (
	OperationUnknown Operation = iota // unknown

	// Read operations
	OperationFind           // find
	OperationAggregate      // aggregate
	OperationCountDocuments // count_documents

	// Write operations
	OperationInsertOne  // insert_one
	OperationInsertMany // insert_many
	OperationUpdateOne  // update_one
	OperationDeleteOne  // delete_one
	OperationUpdateMany // update_many
	OperationDeleteMany // delete_many
)

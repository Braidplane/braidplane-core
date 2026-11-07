package executiondelivery

import "github.com/Braidplane/braidplane-core/pkg/xerror"

type notExecutedError struct{ cause error }

func (e *notExecutedError) Error() string { return e.cause.Error() }
func (e *notExecutedError) Unwrap() error { return e.cause }

// MarkNotExecuted marks an admission failure that must not change connector health.
func MarkNotExecuted(err error) error {
	if err == nil {
		return nil
	}
	return &notExecutedError{cause: err}
}

// IsNotExecuted reports whether admission prevented connector execution.
func IsNotExecuted(err error) bool {
	var marked *notExecutedError
	return xerror.As(err, &marked)
}

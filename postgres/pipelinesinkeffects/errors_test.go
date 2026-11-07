package pipelinesinkeffects

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestErrPipelineSinkEffectNotFound_Code identifies a missing ledger effect without mislabeling it as a run.
func TestErrPipelineSinkEffectNotFound_Code(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "PIPELINE_SINK_EFFECT_NOT_FOUND", ErrPipelineSinkEffectNotFound.Code())
}

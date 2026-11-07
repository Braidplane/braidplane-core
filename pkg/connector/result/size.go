package result

import (
	"encoding/json"

	"github.com/Braidplane/braidplane-core/pkg/xerror"
)

// ResponseBudget bounds retained connector output before appending another value.
type ResponseBudget struct {
	limit int64
	used  int64
}

func NewResponseBudget(limit int64) *ResponseBudget { return &ResponseBudget{limit: limit} }

func (b *ResponseBudget) Check(size int64) error {
	if b.limit > 0 && size > b.limit-b.used {
		return xerror.ExecutionResponseSizeExceeded().Detailf("execution response exceeds %d byte limit", b.limit)
	}
	return nil
}

func (b *ResponseBudget) Charge(size int64) error {
	if err := b.Check(size); err != nil {
		return err
	}
	b.used += size
	return nil
}

func (b *ResponseBudget) ChargeJSON(value any) error {
	if b.limit <= 0 {
		return nil
	}
	if text, ok := value.(string); ok {
		if err := b.Check(int64(len(text))); err != nil {
			return err
		}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	return b.Charge(int64(len(data)))
}

// CheckResponseSize counts row payloads individually so the final check never
// allocates a second complete SQL or MongoDB result.
func (r *ExecutionResult) CheckResponseSize(limit int64) error {
	if r == nil || limit <= 0 {
		return nil
	}
	metadata := *r
	var rows []map[string]any
	switch {
	case r.sql != nil:
		payload := *r.sql
		rows, payload.Rows = payload.Rows, nil
		metadata.sql = &payload
	case r.mongo != nil:
		payload := *r.mongo
		rows, payload.Documents = payload.Documents, nil
		metadata.mongo = &payload
	}
	budget := NewResponseBudget(limit)
	encoded, err := json.Marshal(&metadata)
	if err != nil {
		return xerror.Unexpected().With(err)
	}
	size := int64(len(encoded))
	if rows != nil {
		// Replace null with the array brackets.
		size -= 2
	}
	if err := budget.Charge(size); err != nil {
		return err
	}
	for i, row := range rows {
		if i > 0 {
			if err := budget.Charge(1); err != nil {
				return err
			}
		}
		if err := budget.ChargeJSON(row); err != nil {
			return err
		}
	}
	return nil
}

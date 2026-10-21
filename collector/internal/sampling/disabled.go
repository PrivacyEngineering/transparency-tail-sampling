package sampling

import (
	"context"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

// disabledPolicy always returns NotSampled. Used as "check-off" replacement of transparency_attribute
// in overhead measurements (transparency_attribute.disabled: true).
type disabledPolicy struct{}

// NewDisabledPolicy returns a policy that never samples.
func NewDisabledPolicy() PolicyEvaluator { return disabledPolicy{} }

func (disabledPolicy) Evaluate(context.Context, pcommon.TraceID, *TraceData) (Decision, error) {
	return NotSampled, nil
}

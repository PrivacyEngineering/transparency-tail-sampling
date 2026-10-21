// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package sampling // import "github.com/open-telemetry/opentelemetry-collector-contrib/processor/tailsamplingprocessor/internal/sampling"

import (
	"context"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.uber.org/zap"
)

type subpolicy struct {
	// the subpolicy evaluator
	evaluator PolicyEvaluator

	// spans per second allocated to each subpolicy
	allocatedSPS int64

	// spans per second that each subpolicy sampled in this period
	sampledSPS int64

	// H-7d: traces this subpolicy votes for bypass the rate limit ("unbudgeted evidence")
	unbudgeted bool
}

// CompositeOptions are hardening switches (all false = upstream/thesis behaviour). See FIXES.md H-7.
type CompositeOptions struct {
	// FallThrough: a sub-policy that votes Sampled but is over budget does not end the evaluation;
	// the next sub-policy is evaluated (H-7a).
	FallThrough bool
	// EnforceTotalBudget: the sum of spans sampled by all sub-policies in a second is capped by
	// max_total_spans_per_second (H-7c).
	EnforceTotalBudget bool
	// Unbudgeted marks sub-policies (by index) whose Sampled votes bypass the rate limit (H-7d).
	Unbudgeted map[int]bool
}

// Composite evaluator and its internal data
type Composite struct {
	// the subpolicy evaluators
	subpolicies []*subpolicy

	// maximum total spans per second that must be sampled
	maxTotalSPS int64

	// current unix timestamp second
	currentSecond int64

	// The time provider (can be different from clock for testing purposes)
	timeProvider TimeProvider

	logger *zap.Logger

	opts     CompositeOptions
	totalSPS int64
}

var _ PolicyEvaluator = (*Composite)(nil)

// SubPolicyEvalParams defines the evaluator and max rate for a sub-policy
type SubPolicyEvalParams struct {
	Evaluator         PolicyEvaluator
	MaxSpansPerSecond int64
}

// NewComposite creates a policy evaluator that samples all subpolicies.
func NewComposite(
	logger *zap.Logger,
	maxTotalSpansPerSecond int64,
	subPolicyParams []SubPolicyEvalParams,
	timeProvider TimeProvider,
) PolicyEvaluator {

	var subpolicies []*subpolicy

	for i := 0; i < len(subPolicyParams); i++ {
		sub := &subpolicy{}
		sub.evaluator = subPolicyParams[i].Evaluator
		sub.allocatedSPS = subPolicyParams[i].MaxSpansPerSecond

		// We are just starting, so there is no previous input, set it to 0
		sub.sampledSPS = 0

		subpolicies = append(subpolicies, sub)
	}

	return &Composite{
		maxTotalSPS:  maxTotalSpansPerSecond,
		subpolicies:  subpolicies,
		timeProvider: timeProvider,
		logger:       logger,
	}
}

// NewCompositeWithOptions creates a composite evaluator with hardening options (see CompositeOptions).
func NewCompositeWithOptions(logger *zap.Logger, maxTotalSpansPerSecond int64, subPolicyParams []SubPolicyEvalParams,
	timeProvider TimeProvider, opts CompositeOptions) PolicyEvaluator {
	c := NewComposite(logger, maxTotalSpansPerSecond, subPolicyParams, timeProvider).(*Composite)
	c.opts = opts
	for i, sub := range c.subpolicies {
		sub.unbudgeted = opts.Unbudgeted[i]
	}
	return c
}

// Children returns the sub-policy evaluators (used to find LateSpanCheckers).
func (c *Composite) Children() []PolicyEvaluator {
	out := make([]PolicyEvaluator, 0, len(c.subpolicies))
	for _, s := range c.subpolicies {
		out = append(out, s.evaluator)
	}
	return out
}

// Evaluate looks at the trace data and returns a corresponding SamplingDecision.
func (c *Composite) Evaluate(ctx context.Context, traceID pcommon.TraceID, trace *TraceData) (Decision, error) {
	// Rate limiting works by counting spans that are sampled during each 1 second
	// time period. Until the total number of spans during a particular second
	// exceeds the allocated number of spans-per-second the traces are sampled,
	// once the limit is exceeded the traces are no longer sampled. The counter
	// restarts at the beginning of each second.
	// Current counters and rate limits are kept separately for each subpolicy.

	currSecond := c.timeProvider.getCurSecond()
	if c.currentSecond != currSecond {
		// This is a new second
		c.currentSecond = currSecond
		// Reset counters
		for i := range c.subpolicies {
			c.subpolicies[i].sampledSPS = 0
		}
		c.totalSPS = 0
	}

	for _, sub := range c.subpolicies {
		decision, err := sub.evaluator.Evaluate(ctx, traceID, trace)
		if err != nil {
			return Unspecified, err
		}

		if decision == Sampled || decision == InvertSampled {
			// The subpolicy made a decision to Sample. Now we need to make our decision.
			n := trace.SpanCount.Load()

			// H-7d: unbudgeted evidence is always kept (but still counted)
			if sub.unbudgeted {
				sub.sampledSPS += n
				c.totalSPS += n
				return Sampled, nil
			}

			// Calculate resulting SPS counter if we decide to sample this trace
			spansInSecondIfSampled := sub.sampledSPS + n
			withinTotal := !c.opts.EnforceTotalBudget || c.totalSPS+n <= c.maxTotalSPS

			// Check if the rate will be within the allocated bandwidth.
			if spansInSecondIfSampled <= sub.allocatedSPS && spansInSecondIfSampled <= c.maxTotalSPS && withinTotal {
				sub.sampledSPS = spansInSecondIfSampled
				c.totalSPS += n

				// Let the sampling happen
				return Sampled, nil
			}

			if c.opts.FallThrough {
				continue // H-7a: give the next sub-policy a chance
			}

			// We exceeded the rate limit. Don't sample this trace.
			// Note that we will continue evaluating new incoming traces against
			// allocated SPS, we do not update sub.sampledSPS here in order to give
			// chance to another smaller trace to be accepted later.
			return NotSampled, nil
		}
	}

	return NotSampled, nil
}

// OnDroppedSpans is called when the trace needs to be dropped, due to memory
// pressure, before the decision_wait time has been reached.
func (c *Composite) OnDroppedSpans(pcommon.TraceID, *TraceData) (Decision, error) {
	// Here we have a number of possible solutions:
	// 1. Random sample traces based on maxTotalSPS.
	// 2. Perform full composite sampling logic by calling Composite.Evaluate(), essentially
	//    using partial trace data for sampling.
	// 3. Sample everything.
	//
	// It seems that #2 may be the best choice from end user perspective, but
	// it is not certain and it is also additional performance penalty when we are
	// already under a memory (and possibly CPU) pressure situation.
	//
	// For now we are playing safe and go with #3. Investigating alternate options
	// should be a future task.
	return Sampled, nil
}

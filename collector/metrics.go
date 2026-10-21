// Parts of this file were developed with the assistance of Claude (Anthropic) and reviewed by the authors.

// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package transparencyprocessor // import "github.com/open-telemetry/opentelemetry-collector-contrib/processor/tailsamplingprocessor"

import (
	"go.opencensus.io/stats"
	"go.opencensus.io/stats/view"
	"go.opencensus.io/tag"
	"go.opentelemetry.io/collector/config/configtelemetry"
	"go.opentelemetry.io/collector/obsreport"
)

// Variables related to metrics specific to tail sampling.
var (
	tagPolicyKey, _    = tag.NewKey("policy")
	tagSampledKey, _   = tag.NewKey("sampled")
	tagSourceFormat, _ = tag.NewKey("source_format")

	statDecisionLatencyMicroSec  = stats.Int64("sampling_decision_latency", "Latency (in microseconds) of a given sampling policy", "µs")
	statOverallDecisionLatencyUs = stats.Int64("sampling_decision_timer_latency", "Latency (in microseconds) of each run of the sampling decision timer", "µs")

	statTraceRemovalAgeSec           = stats.Int64("sampling_trace_removal_age", "Time (in seconds) from arrival of a new trace until its removal from memory", "s")
	statLateSpanArrivalAfterDecision = stats.Int64("sampling_late_span_age", "Time (in seconds) from the sampling decision was taken and the arrival of a late span", "s")

	statPolicyEvaluationErrorCount = stats.Int64("sampling_policy_evaluation_error", "Count of sampling policy evaluation errors", stats.UnitDimensionless)

	statCountTracesSampled = stats.Int64("count_traces_sampled", "Count of traces that were sampled or not", stats.UnitDimensionless)

	statDroppedTooEarlyCount    = stats.Int64("sampling_trace_dropped_too_early", "Count of traces that needed to be dropped the configured wait time", stats.UnitDimensionless)
	statNewTraceIDReceivedCount = stats.Int64("new_trace_id_received", "Counts the arrival of new traces", stats.UnitDimensionless)
	statTracesOnMemoryGauge     = stats.Int64("sampling_traces_on_memory", "Tracks the number of traces current on memory", stats.UnitDimensionless)

	// Added for the reproducibility artifact (names prefixed "tp_" so they are not shared with the
	// identically named OpenCensus measures of the contrib tail_sampling processor in the same binary).
	statDecisionLatencyNs  = stats.Int64("tp_sampling_decision_latency_ns", "Latency (ns) of one policy evaluation for one trace", "ns")
	statLateSpansForwarded = stats.Int64("tp_late_spans_forwarded", "Spans arriving after the decision that were forwarded", stats.UnitDimensionless)
	statLateSpansDropped   = stats.Int64("tp_late_spans_dropped", "Spans arriving after a NotSampled decision that were dropped", stats.UnitDimensionless)
	statEvictedUndecided   = stats.Int64("tp_traces_evicted_undecided", "Traces evicted (num_traces exceeded) before a decision", stats.UnitDimensionless)
)

// SamplingProcessorMetricViews return the metrics views according to given telemetry level.
func SamplingProcessorMetricViews(level configtelemetry.Level) []*view.View {
	if level == configtelemetry.LevelNone {
		return nil
	}

	policyTagKeys := []tag.Key{tagPolicyKey}

	latencyDistributionAggregation := view.Distribution(1, 2, 5, 10, 25, 50, 75, 100, 150, 200, 300, 400, 500, 750, 1000, 2000, 3000, 4000, 5000, 10000, 20000, 30000, 50000)
	ageDistributionAggregation := view.Distribution(1, 2, 5, 10, 20, 30, 40, 50, 60, 90, 120, 180, 300, 600, 1800, 3600, 7200)

	decisionLatencyView := &view.View{
		Name:        obsreport.BuildProcessorCustomMetricName(Type, statDecisionLatencyMicroSec.Name()),
		Measure:     statDecisionLatencyMicroSec,
		Description: statDecisionLatencyMicroSec.Description(),
		TagKeys:     policyTagKeys,
		Aggregation: latencyDistributionAggregation,
	}
	overallDecisionLatencyView := &view.View{
		Name:        obsreport.BuildProcessorCustomMetricName(Type, statOverallDecisionLatencyUs.Name()),
		Measure:     statOverallDecisionLatencyUs,
		Description: statOverallDecisionLatencyUs.Description(),
		Aggregation: latencyDistributionAggregation,
	}

	traceRemovalAgeView := &view.View{
		Name:        obsreport.BuildProcessorCustomMetricName(Type, statTraceRemovalAgeSec.Name()),
		Measure:     statTraceRemovalAgeSec,
		Description: statTraceRemovalAgeSec.Description(),
		Aggregation: ageDistributionAggregation,
	}
	lateSpanArrivalView := &view.View{
		Name:        obsreport.BuildProcessorCustomMetricName(Type, statLateSpanArrivalAfterDecision.Name()),
		Measure:     statLateSpanArrivalAfterDecision,
		Description: statLateSpanArrivalAfterDecision.Description(),
		Aggregation: ageDistributionAggregation,
	}

	countPolicyEvaluationErrorView := &view.View{
		Name:        obsreport.BuildProcessorCustomMetricName(Type, statPolicyEvaluationErrorCount.Name()),
		Measure:     statPolicyEvaluationErrorCount,
		Description: statPolicyEvaluationErrorCount.Description(),
		Aggregation: view.Sum(),
	}

	sampledTagKeys := []tag.Key{tagPolicyKey, tagSampledKey}
	countTracesSampledView := &view.View{
		Name:        obsreport.BuildProcessorCustomMetricName(Type, statCountTracesSampled.Name()),
		Measure:     statCountTracesSampled,
		Description: statCountTracesSampled.Description(),
		TagKeys:     sampledTagKeys,
		Aggregation: view.Sum(),
	}

	countTraceDroppedTooEarlyView := &view.View{
		Name:        obsreport.BuildProcessorCustomMetricName(Type, statDroppedTooEarlyCount.Name()),
		Measure:     statDroppedTooEarlyCount,
		Description: statDroppedTooEarlyCount.Description(),
		Aggregation: view.Sum(),
	}
	countTraceIDArrivalView := &view.View{
		Name:        obsreport.BuildProcessorCustomMetricName(Type, statNewTraceIDReceivedCount.Name()),
		Measure:     statNewTraceIDReceivedCount,
		Description: statNewTraceIDReceivedCount.Description(),
		Aggregation: view.Sum(),
	}
	trackTracesOnMemorylView := &view.View{
		Name:        obsreport.BuildProcessorCustomMetricName(Type, statTracesOnMemoryGauge.Name()),
		Measure:     statTracesOnMemoryGauge,
		Description: statTracesOnMemoryGauge.Description(),
		Aggregation: view.LastValue(),
	}

	nsDist := view.Distribution(100, 250, 500, 1000, 2500, 5000, 10000, 25000, 50000, 100000, 250000, 500000, 1e6, 2.5e6, 1e7, 1e8)
	extra := []*view.View{
		{Name: obsreport.BuildProcessorCustomMetricName(Type, statDecisionLatencyNs.Name()), Measure: statDecisionLatencyNs,
			Description: statDecisionLatencyNs.Description(), TagKeys: policyTagKeys, Aggregation: nsDist},
		{Name: obsreport.BuildProcessorCustomMetricName(Type, statLateSpansForwarded.Name()), Measure: statLateSpansForwarded,
			Description: statLateSpansForwarded.Description(), Aggregation: view.Sum()},
		{Name: obsreport.BuildProcessorCustomMetricName(Type, statLateSpansDropped.Name()), Measure: statLateSpansDropped,
			Description: statLateSpansDropped.Description(), Aggregation: view.Sum()},
		{Name: obsreport.BuildProcessorCustomMetricName(Type, statEvictedUndecided.Name()), Measure: statEvictedUndecided,
			Description: statEvictedUndecided.Description(), Aggregation: view.Sum()},
	}

	return append(extra,
		decisionLatencyView,
		overallDecisionLatencyView,

		traceRemovalAgeView,
		lateSpanArrivalView,

		countPolicyEvaluationErrorView,

		countTracesSampledView,

		countTraceDroppedTooEarlyView,
		countTraceIDArrivalView,
		trackTracesOnMemorylView,
	)
}

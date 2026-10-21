package transparencyprocessor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akkbng/otel-transparency-collector/internal/sampling"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const tiltJSON = `{"dataDisclosed":[{"_id":"paymentservice","category":"Credit Card; transaction id"}]}`

func oneSpan(id byte, svc, cat string) ptrace.Traces {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	rs.Resource().Attributes().PutStr("service.name", svc)
	s := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
	s.SetTraceID(pcommon.TraceID([16]byte{id}))
	s.SetSpanID(pcommon.SpanID([8]byte{id, 1}))
	if cat != "" {
		s.Attributes().PutStr("tilt.dataDisclosed.category", cat)
	}
	return td
}

func newTestProc(t *testing.T, numTraces uint64, harden bool) (*transparencyProcessor, *consumertest.TracesSink) {
	p := filepath.Join(t.TempDir(), "tilt.json")
	require.NoError(t, os.WriteFile(p, []byte(tiltJSON), 0o600))
	cfg := Config{DecisionWait: time.Second, NumTraces: numTraces, LateSpanCheck: harden, DecideOnEviction: harden,
		PolicyCfgs: []PolicyCfg{{sharedPolicyCfg: sharedPolicyCfg{Name: "tilt", Type: TiltConformity,
			TiltConformityCfg: TiltConformityCfg{TiltURL: "file://" + p}}}}}
	sink := new(consumertest.TracesSink)
	proc, err := newTransparencyProcessor(context.Background(), componenttest.NewNopTelemetrySettings(), sink, cfg)
	require.NoError(t, err)
	return proc.(*transparencyProcessor), sink
}

// H-8: a violating trace evicted before its decision is evaluated and forwarded instead of being lost.
func TestDecideOnEviction(t *testing.T) {
	for _, harden := range []bool{false, true} {
		tsp, sink := newTestProc(t, 1, harden)
		require.NoError(t, tsp.ConsumeTraces(context.Background(), oneSpan(1, "paymentservice", "health data")))
		require.NoError(t, tsp.ConsumeTraces(context.Background(), oneSpan(2, "frontend", ""))) // evicts trace 1
		if harden {
			assert.Equal(t, 1, sink.SpanCount(), "evicted violating trace forwarded")
		} else {
			assert.Equal(t, 0, sink.SpanCount(), "thesis behaviour: evicted trace silently dropped")
		}
	}
}

// H-6: a violating span arriving after a NotSampled decision is forwarded as evidence and flagged.
func TestLateSpanCheck(t *testing.T) {
	for _, harden := range []bool{false, true} {
		tsp, sink := newTestProc(t, 100, harden)
		require.NoError(t, tsp.ConsumeTraces(context.Background(), oneSpan(3, "paymentservice", "Credit Card")))
		// take the decision as samplingPolicyOnTick does (the id batcher is asynchronous)
		id := pcommon.TraceID([16]byte{3})
		d, ok := tsp.idToTrace.Load(id)
		require.True(t, ok)
		td := d.(*sampling.TraceData)
		dec, _ := tsp.makeDecision(id, td, &policyMetrics{})
		require.Equal(t, sampling.NotSampled, dec) // conforming
		td.Lock()
		td.FinalDecision = dec
		td.ReceivedBatches = ptrace.NewTraces()
		td.Unlock()
		assert.Equal(t, 0, sink.SpanCount())
		late := oneSpan(3, "paymentservice", "health data")
		late.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).SetSpanID(pcommon.SpanID([8]byte{3, 2}))
		require.NoError(t, tsp.ConsumeTraces(context.Background(), late))
		if harden {
			require.Equal(t, 1, sink.SpanCount())
			got := sink.AllTraces()[0].ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
			r, _ := got.Attributes().Get("tilt.check_reason")
			assert.Equal(t, "undeclared_category", r.Str())
		} else {
			assert.Equal(t, 0, sink.SpanCount(), "thesis behaviour: late violating span dropped")
		}
	}
}

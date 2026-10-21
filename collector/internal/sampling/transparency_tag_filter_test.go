package sampling

// Tests added for the reproducibility artifact (not part of the thesis code). They pin down the
// behaviour of the transparency_attribute policy exactly as implemented in transparency_tag_filter.go.

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
)

const testTilt = `{"dataDisclosed":[
 {"_id":"checkoutservice","category":"user id; order id"},
 {"_id":"cartservice","category":""},
 {"_id":"emailservice"},
 {"_id":"dupservice","category":"a"},
 {"_id":"dupservice","category":"b"}
],"automatedDecisionMaking":{"inUse":false}}`

type sp struct {
	service  string
	category *string
}

func str(s string) *string { return &s }

func newTestFilter(t *testing.T, ignore bool) PolicyEvaluator {
	p := filepath.Join(t.TempDir(), "tilt.json")
	require.NoError(t, os.WriteFile(p, []byte(testTilt), 0o600))
	f, err := NewTransparencyAttributeFilter(componenttest.NewNopTelemetrySettings(), "file://"+p, ignore)
	require.NoError(t, err)
	return f
}

func buildTrace(spans ...sp) (*TraceData, []ptrace.Span) {
	td := ptrace.NewTraces()
	var out []ptrace.Span
	for _, s := range spans {
		rs := td.ResourceSpans().AppendEmpty()
		rs.Resource().Attributes().PutStr("service.name", s.service)
		span := rs.ScopeSpans().AppendEmpty().Spans().AppendEmpty()
		span.SetTraceID(pcommon.TraceID([16]byte{1}))
		if s.category != nil {
			span.Attributes().PutStr("tilt.dataDisclosed.category", *s.category)
		}
	}
	for i := 0; i < td.ResourceSpans().Len(); i++ {
		out = append(out, td.ResourceSpans().At(i).ScopeSpans().At(0).Spans().At(0))
	}
	c := &atomic.Int64{}
	c.Store(int64(len(spans)))
	return &TraceData{ReceivedBatches: td, SpanCount: c}, out
}

func flag(s ptrace.Span) string {
	v, ok := s.Attributes().Get("tilt.check_flag")
	if !ok {
		return "unset"
	}
	if v.Bool() {
		return "true"
	}
	return "false"
}

func TestTransparencyPolicy(t *testing.T) {
	cases := []struct {
		name     string
		ignore   bool
		spans    []sp
		decision Decision
		flags    []string
	}{
		{"undeclared service is ignored", false, []sp{{"frontend", str("user id")}}, NotSampled, []string{"unset"}},
		{"declared category passes", false, []sp{{"checkoutservice", str("order id")}}, NotSampled, []string{"true"}},
		{"undeclared category fails", false, []sp{{"checkoutservice", str("health data")}}, Sampled, []string{"false"}},
		{"substring match passes", false, []sp{{"checkoutservice", str("id")}}, NotSampled, []string{"true"}},
		{"empty category attribute fails", false, []sp{{"checkoutservice", str("")}}, Sampled, []string{"false"}},
		{"missing attribute on declared service fails (thesis default)", false, []sp{{"checkoutservice", nil}}, Sampled, []string{"false"}},
		{"missing attribute ignored with EXT-1", true, []sp{{"checkoutservice", nil}}, NotSampled, []string{"unset"}},
		{"empty declared category fails", false, []sp{{"cartservice", str("user id")}}, Sampled, []string{"false"}},
		{"declared entry without category fails", false, []sp{{"emailservice", str("customer email")}}, Sampled, []string{"false"}},
		{"only first TILT entry per service is used", false, []sp{{"dupservice", str("b")}}, Sampled, []string{"false"}},
		{"evaluation stops at first failing span", false,
			[]sp{{"checkoutservice", str("order id")}, {"checkoutservice", str("x")}, {"checkoutservice", str("user id")}},
			Sampled, []string{"true", "false", "unset"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newTestFilter(t, c.ignore)
			trace, spans := buildTrace(c.spans...)
			d, err := f.Evaluate(context.Background(), pcommon.TraceID{}, trace)
			require.NoError(t, err)
			assert.Equal(t, c.decision, d)
			for i, s := range spans {
				assert.Equal(t, c.flags[i], flag(s), "span %d", i)
			}
		})
	}
}

func TestTransparencyPolicyTiltErrors(t *testing.T) {
	_, err := NewTransparencyAttributeFilter(componenttest.NewNopTelemetrySettings(), "file:///does/not/exist.json", false)
	assert.Error(t, err)
	p := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(p, []byte("<html>"), 0o600))
	_, err = NewTransparencyAttributeFilter(componenttest.NewNopTelemetrySettings(), "file://"+p, false)
	assert.Error(t, err)
}

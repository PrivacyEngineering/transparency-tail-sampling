// Parts of this file were developed with the assistance of Claude (Anthropic) and reviewed by the authors.

package sampling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
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

func writeTilt(t *testing.T, content string) (string, string) {
	p := filepath.Join(t.TempDir(), "tilt.json")
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	sum := sha256.Sum256([]byte(content))
	return "file://" + p, hex.EncodeToString(sum[:])
}

func reason(s ptrace.Span) string {
	v, ok := s.Attributes().Get("tilt.check_reason")
	if !ok {
		return ""
	}
	return v.Str()
}

func TestTiltConformity(t *testing.T) {
	url, sum := writeTilt(t, testTilt)
	cases := []struct {
		name     string
		sampleOn []string
		spans    []sp
		decision Decision
		flags    []string
		reasons  []string
	}{
		{"exact membership passes", nil, []sp{{"checkoutservice", str("order id")}}, NotSampled, []string{"true"}, []string{""}},
		{"substring no longer passes (H-1)", nil, []sp{{"checkoutservice", str("id")}}, Sampled, []string{"false"}, []string{ReasonUndeclaredCategory}},
		{"whitespace is trimmed", nil, []sp{{"checkoutservice", str(" user id ")}}, NotSampled, []string{"true"}, []string{""}},
		{"all TILT entries of a service are used (H-1)", nil, []sp{{"dupservice", str("b")}}, NotSampled, []string{"true"}, []string{""}},
		{"V3 unlisted service with annotation (H-2)", nil, []sp{{"frontend", str("user id")}}, Sampled, []string{"false"}, []string{ReasonUnlistedService}},
		{"unlisted unannotated span ignored", nil, []sp{{"frontend", nil}}, NotSampled, []string{"unset"}, []string{""}},
		{"V2 missing annotation flagged but not sampled by default", nil, []sp{{"checkoutservice", nil}}, NotSampled, []string{"false"}, []string{ReasonMissingAnnotation}},
		{"V2 sampled when configured (thesis semantics)", []string{ReasonMissingAnnotation}, []sp{{"checkoutservice", nil}}, Sampled, []string{"false"}, []string{ReasonMissingAnnotation}},
		{"empty declared category: annotated span is V1", nil, []sp{{"cartservice", str("user id")}}, Sampled, []string{"false"}, []string{ReasonUndeclaredCategory}},
		{"all spans are evaluated (H-4)", nil,
			[]sp{{"checkoutservice", str("x")}, {"checkoutservice", str("order id")}, {"frontend", str("y")}},
			Sampled, []string{"false", "true", "false"}, []string{ReasonUndeclaredCategory, "", ReasonUnlistedService}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := NewTiltConformity(componenttest.NewNopTelemetrySettings(), TiltConformityConfig{TiltURL: url, TiltSHA256: sum, SampleOn: c.sampleOn})
			defer p.(*tiltConformity).Shutdown()
			trace, spans := buildTrace(c.spans...)
			d, err := p.Evaluate(context.Background(), pcommon.TraceID{}, trace)
			require.NoError(t, err)
			assert.Equal(t, c.decision, d)
			for i, s := range spans {
				assert.Equal(t, c.flags[i], flag(s), "flag span %d", i)
				assert.Equal(t, c.reasons[i], reason(s), "reason span %d", i)
			}
		})
	}
}

func TestTiltConformityFailClosed(t *testing.T) {
	url, _ := writeTilt(t, testTilt)
	for name, cfg := range map[string]TiltConformityConfig{
		"sha256 mismatch": {TiltURL: url, TiltSHA256: "00"},
		"missing file":    {TiltURL: "file:///nonexistent.json"},
	} {
		t.Run(name, func(t *testing.T) {
			p := NewTiltConformity(componenttest.NewNopTelemetrySettings(), cfg) // must not panic
			defer p.(*tiltConformity).Shutdown()
			tr, spans := buildTrace(sp{"checkoutservice", str("order id")}, sp{"frontend", nil})
			d, err := p.Evaluate(context.Background(), pcommon.TraceID{}, tr)
			require.NoError(t, err)
			assert.Equal(t, Sampled, d)
			assert.Equal(t, ReasonTiltUnavailable, reason(spans[0]))
			assert.Equal(t, "unset", flag(spans[1]))
		})
	}
	t.Run("HTTP 404 and non-JSON", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/html" {
				_, _ = w.Write([]byte("<html>"))
				return
			}
			http.NotFound(w, r)
		}))
		defer srv.Close()
		for _, u := range []string{srv.URL + "/missing", srv.URL + "/html"} {
			p := NewTiltConformity(componenttest.NewNopTelemetrySettings(), TiltConformityConfig{TiltURL: u})
			tr, _ := buildTrace(sp{"checkoutservice", str("order id")})
			d, err := p.Evaluate(context.Background(), pcommon.TraceID{}, tr)
			require.NoError(t, err)
			assert.Equal(t, Sampled, d)
			p.(*tiltConformity).Shutdown()
		}
	})
	t.Run("served over HTTP with matching sha256", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(testTilt)) }))
		defer srv.Close()
		_, sum := writeTilt(t, testTilt)
		p := NewTiltConformity(componenttest.NewNopTelemetrySettings(), TiltConformityConfig{TiltURL: srv.URL, TiltSHA256: sum})
		defer p.(*tiltConformity).Shutdown()
		tr, _ := buildTrace(sp{"checkoutservice", str("order id")})
		d, _ := p.Evaluate(context.Background(), pcommon.TraceID{}, tr)
		assert.Equal(t, NotSampled, d)
	})
}

func TestTiltConformityDisabledAndLate(t *testing.T) {
	url, _ := writeTilt(t, testTilt)
	off := NewTiltConformity(componenttest.NewNopTelemetrySettings(), TiltConformityConfig{TiltURL: url, Disabled: true})
	tr, spans := buildTrace(sp{"checkoutservice", str("x")})
	d, _ := off.Evaluate(context.Background(), pcommon.TraceID{}, tr)
	assert.Equal(t, NotSampled, d)
	assert.Equal(t, "unset", flag(spans[0]))

	on := NewTiltConformity(componenttest.NewNopTelemetrySettings(), TiltConformityConfig{TiltURL: url})
	defer on.(*tiltConformity).Shutdown()
	tr2, spans2 := buildTrace(sp{"checkoutservice", str("x")})
	assert.True(t, on.(LateSpanChecker).CheckLateSpans(tr2.ReceivedBatches))
	assert.Equal(t, "false", flag(spans2[0]))
}

// ---- composite hardening (H-7) ----

type constPolicy struct{ d Decision }

func (c constPolicy) Evaluate(context.Context, pcommon.TraceID, *TraceData) (Decision, error) { return c.d, nil }

type fixedClock struct{}

func (fixedClock) getCurSecond() int64 { return 42 }

func traceWithSpans(n int64) *TraceData {
	c := &atomic.Int64{}
	c.Store(n)
	return &TraceData{SpanCount: c, ReceivedBatches: ptrace.NewTraces()}
}

func TestCompositeHardening(t *testing.T) {
	ctx := context.Background()
	subs := func() []SubPolicyEvalParams {
		return []SubPolicyEvalParams{{Evaluator: constPolicy{Sampled}, MaxSpansPerSecond: 10}, {Evaluator: constPolicy{Sampled}, MaxSpansPerSecond: 100}}
	}
	// upstream: first sub-policy over budget -> NotSampled even though the second has budget
	up := NewComposite(nil, 1000, subs(), fixedClock{})
	d, _ := up.Evaluate(ctx, pcommon.TraceID{}, traceWithSpans(8))
	assert.Equal(t, Sampled, d)
	d, _ = up.Evaluate(ctx, pcommon.TraceID{}, traceWithSpans(8))
	assert.Equal(t, NotSampled, d, "upstream has no fall-through")

	ft := NewCompositeWithOptions(nil, 1000, subs(), fixedClock{}, CompositeOptions{FallThrough: true})
	_, _ = ft.Evaluate(ctx, pcommon.TraceID{}, traceWithSpans(8))
	d, _ = ft.Evaluate(ctx, pcommon.TraceID{}, traceWithSpans(8))
	assert.Equal(t, Sampled, d, "fall-through to the second sub-policy")

	// global budget: upstream lets the sum of sub-policies exceed max_total_spans_per_second
	two := func() []SubPolicyEvalParams {
		return []SubPolicyEvalParams{{Evaluator: constPolicy{NotSampled}, MaxSpansPerSecond: 0}, {Evaluator: constPolicy{Sampled}, MaxSpansPerSecond: 15}}
	}
	g := NewCompositeWithOptions(nil, 10, []SubPolicyEvalParams{{Evaluator: constPolicy{Sampled}, MaxSpansPerSecond: 8}, two()[1]}, fixedClock{},
		CompositeOptions{FallThrough: true, EnforceTotalBudget: true})
	d1, _ := g.Evaluate(ctx, pcommon.TraceID{}, traceWithSpans(8))
	d2, _ := g.Evaluate(ctx, pcommon.TraceID{}, traceWithSpans(8))
	assert.Equal(t, []Decision{Sampled, NotSampled}, []Decision{d1, d2}, "total budget 10 enforced")

	// unbudgeted evidence
	ub := NewCompositeWithOptions(nil, 10, []SubPolicyEvalParams{{Evaluator: constPolicy{Sampled}, MaxSpansPerSecond: 0}}, fixedClock{},
		CompositeOptions{Unbudgeted: map[int]bool{0: true}})
	d, _ = ub.Evaluate(ctx, pcommon.TraceID{}, traceWithSpans(1000))
	assert.Equal(t, Sampled, d)
}

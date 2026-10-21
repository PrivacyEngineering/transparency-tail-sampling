package sampling

// tilt_conformity: HARDENED variant of the thesis transparency_attribute policy (see FIXES.md, H-1 .. H-7).
// The thesis policy (transparency_tag_filter.go) is left unchanged so that both can be compared with one binary.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"
)

// Reasons written to tilt.check_reason.
const (
	ReasonUndeclaredCategory = "undeclared_category" // V1: annotated category not declared for this service
	ReasonMissingAnnotation  = "missing_annotation"  // V2: span of a TILT-listed service without (or with empty) category
	ReasonUnlistedService    = "unlisted_service"    // V3: annotated span of a service that is absent from the TILT document
	ReasonTiltUnavailable    = "tilt_unavailable"    // TILT could not be loaded/verified: fail closed for annotated spans
	attrCheckReason          = "tilt.check_reason"
)

// TiltConformityConfig configures the hardened policy.
type TiltConformityConfig struct {
	TiltURL         string        // http(s):// or file://
	TiltSHA256      string        // optional hex sha256 of the TILT document (integrity pin)
	SampleOn        []string      // reasons that make the trace sampled; default undeclared_category, unlisted_service, tilt_unavailable
	RefreshInterval time.Duration // retry/refresh period for the TILT document (0 = 60 s)
	Disabled        bool          // check-off switch for overhead measurements: always NotSampled, no flags
}

type declaredSet map[string]map[string]struct{} // service -> set of declared categories

type tiltConformity struct {
	logger   *zap.Logger
	cfg      TiltConformityConfig
	sampleOn map[string]bool
	declared atomic.Pointer[declaredSet]
	stopOnce sync.Once
	stop     chan struct{}
}

var _ PolicyEvaluator = (*tiltConformity)(nil)

// LateSpanChecker is implemented by policies that can check spans arriving after the sampling decision.
type LateSpanChecker interface {
	// CheckLateSpans annotates the spans in td and reports whether any of them is a sampling-relevant violation.
	CheckLateSpans(td ptrace.Traces) bool
}

// NewTiltConformity creates the hardened policy. A failing TILT fetch is logged and retried, never fatal (H-3).
func NewTiltConformity(settings component.TelemetrySettings, cfg TiltConformityConfig) PolicyEvaluator {
	if len(cfg.SampleOn) == 0 {
		cfg.SampleOn = []string{ReasonUndeclaredCategory, ReasonUnlistedService, ReasonTiltUnavailable}
	}
	if cfg.RefreshInterval <= 0 {
		cfg.RefreshInterval = 60 * time.Second
	}
	p := &tiltConformity{logger: settings.Logger, cfg: cfg, sampleOn: map[string]bool{}, stop: make(chan struct{})}
	for _, r := range cfg.SampleOn {
		p.sampleOn[r] = true
	}
	if cfg.Disabled {
		return p
	}
	p.reload()
	go func() {
		t := time.NewTicker(cfg.RefreshInterval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				p.reload()
			case <-p.stop:
				return
			}
		}
	}()
	return p
}

func (p *tiltConformity) reload() {
	ds, err := loadDeclared(p.cfg.TiltURL, p.cfg.TiltSHA256)
	if err != nil {
		// keep the last verified document; with none, annotated spans are treated as tilt_unavailable
		p.logger.Error("TILT document not loaded; failing closed", zap.String("tilt_url", p.cfg.TiltURL), zap.Error(err))
		return
	}
	if old := p.declared.Swap(ds); old == nil {
		p.logger.Info("Loaded and verified TILT document", zap.String("tilt_url", p.cfg.TiltURL), zap.Int("services", len(*ds)))
	}
}

func loadDeclared(url, wantSHA string) (*declaredSet, error) {
	var raw []byte
	var err error
	if strings.HasPrefix(url, "file://") {
		raw, err = os.ReadFile(strings.TrimPrefix(url, "file://"))
	} else {
		client := &http.Client{Timeout: 10 * time.Second}
		var resp *http.Response
		resp, err = client.Get(url)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			raw, err = io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		}
	}
	if err != nil {
		return nil, err
	}
	if wantSHA != "" {
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, wantSHA) {
			return nil, fmt.Errorf("sha256 mismatch: got %s, want %s", got, wantSHA)
		}
	}
	var doc tiltSpec
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decoding TILT: %w", err)
	}
	if len(doc.DataDisclosed) == 0 {
		return nil, fmt.Errorf("TILT document has no dataDisclosed entries")
	}
	ds := declaredSet{}
	for _, e := range doc.DataDisclosed { // H-1: union over ALL entries of a service, exact set membership
		set, ok := ds[e.ServiceId]
		if !ok {
			set = map[string]struct{}{}
			ds[e.ServiceId] = set
		}
		for _, c := range strings.Split(e.Category, ";") {
			if c = strings.TrimSpace(c); c != "" {
				set[c] = struct{}{}
			}
		}
	}
	return &ds, nil
}

// checkSpan returns "" for a conforming span, "-" for a span that is not subject to the check, or a reason.
func checkSpan(ds *declaredSet, service string, span ptrace.Span) string {
	v, has := span.Attributes().Get(attrCategories)
	cat := ""
	if has {
		cat = strings.TrimSpace(v.AsString())
	}
	if ds == nil {
		if has {
			return ReasonTiltUnavailable
		}
		return "-"
	}
	set, listed := (*ds)[service]
	switch {
	case !listed && cat == "":
		return "-"
	case !listed:
		return ReasonUnlistedService // H-2 (V3)
	case cat == "":
		return ReasonMissingAnnotation // V2
	}
	if _, ok := set[cat]; ok {
		return ""
	}
	return ReasonUndeclaredCategory // V1
}

// annotate checks every span of td (H-4: no early exit), writes tilt.check_flag / tilt.check_reason and
// returns whether a sampling-relevant violation was found.
func (p *tiltConformity) annotate(td ptrace.Traces) bool {
	ds := p.declared.Load()
	violation := false
	for i := 0; i < td.ResourceSpans().Len(); i++ {
		rs := td.ResourceSpans().At(i)
		svc := ""
		if v, ok := rs.Resource().Attributes().Get(attrServiceName); ok {
			svc = v.AsString()
		}
		for j := 0; j < rs.ScopeSpans().Len(); j++ {
			spans := rs.ScopeSpans().At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				span := spans.At(k)
				r := checkSpan(ds, svc, span)
				if r == "-" {
					continue
				}
				span.Attributes().PutBool(attrCheckFlag, r == "")
				if r != "" {
					span.Attributes().PutStr(attrCheckReason, r)
					if p.sampleOn[r] {
						violation = true
					}
				}
			}
		}
	}
	return violation
}

// Evaluate checks all spans while holding the trace lock (H-5).
func (p *tiltConformity) Evaluate(_ context.Context, _ pcommon.TraceID, trace *TraceData) (Decision, error) {
	if p.cfg.Disabled {
		return NotSampled, nil
	}
	trace.Lock()
	defer trace.Unlock()
	if p.annotate(trace.ReceivedBatches) {
		return Sampled, nil
	}
	return NotSampled, nil
}

// CheckLateSpans implements LateSpanChecker (H-6).
func (p *tiltConformity) CheckLateSpans(td ptrace.Traces) bool {
	if p.cfg.Disabled {
		return false
	}
	return p.annotate(td)
}

// Shutdown stops the refresh goroutine.
func (p *tiltConformity) Shutdown() { p.stopOnce.Do(func() { close(p.stop) }) }

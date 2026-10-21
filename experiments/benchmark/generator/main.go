// tiltgen: synthetic OTLP/gRPC trace generator for the collector benchmark.
//
// Emits multi-span traces that mimic the request types of the instrumented Astronomy Shop:
//   browse   (50 %): frontend, productcatalogservice x2, currencyservice x2, recommendationservice    6 spans, no personal data
//   cart     (30 %): frontend, cartservice GetCart [user id], cartservice HGET (unannotated),
//                    productcatalogservice x2                                                         5 spans
//   checkout (20 %): frontend, checkoutservice PlaceOrder [customer email], checkoutservice gRPC client
//                    (unannotated), cartservice GetCart [user id], paymentservice Charge [Credit Card],
//                    shippingservice ShipOrder [tracking id], emailservice [customer email]          7 spans
// A fraction -drift of checkout traces reports the undeclared category "health data" in paymentservice.
// Mean 5.9 spans/trace. Offered load is given in spans/s.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type spanDef struct {
	svc, name, cat, purpose string
	kind                    ptrace.SpanKind
	parent                  int // index of parent span, -1 = root
}

var browse = []spanDef{
	{"frontend", "HTTP GET", "", "", ptrace.SpanKindServer, -1},
	{"productcatalogservice", "oteldemo.ProductCatalogService/GetProduct", "", "", ptrace.SpanKindServer, 0},
	{"productcatalogservice", "oteldemo.ProductCatalogService/ListProducts", "", "", ptrace.SpanKindServer, 0},
	{"currencyservice", "CurrencyService/Convert", "", "", ptrace.SpanKindServer, 0},
	{"currencyservice", "CurrencyService/GetSupportedCurrencies", "", "", ptrace.SpanKindServer, 0},
	{"recommendationservice", "/oteldemo.RecommendationService/ListRecommendations", "", "", ptrace.SpanKindServer, 0},
}
var cart = []spanDef{
	{"frontend", "HTTP POST", "", "", ptrace.SpanKindServer, -1},
	{"cartservice", "oteldemo.CartService/GetCart", "user id", "To fetch the user's cart", ptrace.SpanKindServer, 0},
	{"cartservice", "HGET", "", "", ptrace.SpanKindClient, 1},
	{"productcatalogservice", "oteldemo.ProductCatalogService/GetProduct", "", "", ptrace.SpanKindServer, 0},
	{"productcatalogservice", "oteldemo.ProductCatalogService/GetProduct", "", "", ptrace.SpanKindServer, 0},
}
var checkout = []spanDef{
	{"frontend", "HTTP POST", "", "", ptrace.SpanKindServer, -1},
	{"checkoutservice", "oteldemo.CheckoutService/PlaceOrder", "customer email", "To send order confirmation email", ptrace.SpanKindServer, 0},
	{"checkoutservice", "oteldemo.PaymentService/Charge", "", "", ptrace.SpanKindClient, 1},
	{"cartservice", "oteldemo.CartService/GetCart", "user id", "To fetch the user's cart", ptrace.SpanKindServer, 1},
	{"paymentservice", "grpc.oteldemo.PaymentService/Charge", "Credit Card", "Processing is necessary for the performance of a contract", ptrace.SpanKindServer, 2},
	{"shippingservice", "oteldemo.ShippingService/ShipOrder", "tracking id", "To ship the order to the customer", ptrace.SpanKindServer, 1},
	{"emailservice", "POST /send_order_confirmation", "customer email", "To send order confirmation email", ptrace.SpanKindServer, 1},
}

type stats struct{ traces, spans, errSpans, driftTraces, batches, errBatches atomic.Int64 }

func main() {
	endpoint := flag.String("endpoint", "collector:4317", "OTLP gRPC endpoint")
	rate := flag.Float64("rate", 1000, "offered load in spans/s")
	duration := flag.Duration("duration", time.Minute, "how long to send")
	workers := flag.Int("workers", 4, "concurrent senders")
	tracesPerBatch := flag.Int("batch-traces", 20, "traces per OTLP export request")
	drift := flag.Float64("drift", 0.10, "fraction of checkout traces with an undeclared category")
	seed := flag.Int64("seed", 1, "random seed")
	out := flag.String("out", "", "write JSON summary to this file")
	flag.Parse()

	conn, err := grpc.Dial(*endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	client := ptraceotlp.NewGRPCClient(conn)
	var st stats
	const meanSpans = 0.5*6 + 0.3*5 + 0.2*7
	batchesPerSec := *rate / meanSpans / float64(*tracesPerBatch)
	interval := time.Duration(float64(time.Second) * float64(*workers) / batchesPerSec)
	start := time.Now()
	deadline := start.Add(*duration)
	var wg sync.WaitGroup
	for w := 0; w < *workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rnd := rand.New(rand.NewSource(*seed*1000 + int64(w)))
			next := start.Add(time.Duration(rnd.Int63n(int64(interval) + 1)))
			for {
				now := time.Now()
				if now.After(deadline) {
					return
				}
				if now.Before(next) {
					time.Sleep(next.Sub(now))
				}
				next = next.Add(interval)
				td, nspans, ndrift := buildBatch(rnd, *tracesPerBatch, *drift)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				_, err := client.Export(ctx, ptraceotlp.NewExportRequestFromTraces(td))
				cancel()
				st.batches.Add(1)
				if err != nil {
					st.errBatches.Add(1)
					st.errSpans.Add(int64(nspans))
				} else {
					st.traces.Add(int64(*tracesPerBatch))
					st.spans.Add(int64(nspans))
					st.driftTraces.Add(int64(ndrift))
				}
			}
		}(w)
	}
	wg.Wait()
	el := time.Since(start).Seconds()
	res := map[string]any{
		"offered_spans_per_s": *rate, "elapsed_s": el, "sent_spans": st.spans.Load(), "sent_traces": st.traces.Load(),
		"failed_spans": st.errSpans.Load(), "batches": st.batches.Load(), "failed_batches": st.errBatches.Load(),
		"drift_traces": st.driftTraces.Load(), "achieved_spans_per_s": float64(st.spans.Load()) / el,
	}
	b, _ := json.Marshal(res)
	fmt.Println(string(b))
	if *out != "" {
		_ = os.WriteFile(*out, b, 0o644)
	}
}

func buildBatch(rnd *rand.Rand, n int, drift float64) (ptrace.Traces, int, int) {
	td := ptrace.NewTraces()
	nspans, ndrift := 0, 0
	now := time.Now()
	for i := 0; i < n; i++ {
		var tmpl []spanDef
		x := rnd.Float64()
		switch {
		case x < 0.5:
			tmpl = browse
		case x < 0.8:
			tmpl = cart
		default:
			tmpl = checkout
		}
		isDrift := len(tmpl) == len(checkout) && rnd.Float64() < drift
		if isDrift {
			ndrift++
		}
		var tid [16]byte
		binary.BigEndian.PutUint64(tid[:8], rnd.Uint64())
		binary.BigEndian.PutUint64(tid[8:], rnd.Uint64())
		ids := make([][8]byte, len(tmpl))
		for j := range tmpl {
			binary.BigEndian.PutUint64(ids[j][:], rnd.Uint64()|1)
		}
		for j, d := range tmpl {
			rs := td.ResourceSpans().AppendEmpty()
			ra := rs.Resource().Attributes()
			ra.PutStr("service.name", d.svc)
			ra.PutStr("service.namespace", "opentelemetry-demo")
			ra.PutStr("telemetry.sdk.name", "opentelemetry")
			ss := rs.ScopeSpans().AppendEmpty()
			ss.Scope().SetName("tiltgen")
			sp := ss.Spans().AppendEmpty()
			sp.SetTraceID(pcommon.TraceID(tid))
			sp.SetSpanID(pcommon.SpanID(ids[j]))
			if d.parent >= 0 {
				sp.SetParentSpanID(pcommon.SpanID(ids[d.parent]))
			}
			sp.SetName(d.name)
			sp.SetKind(d.kind)
			st := now.Add(time.Duration(j) * time.Millisecond)
			sp.SetStartTimestamp(pcommon.NewTimestampFromTime(st))
			sp.SetEndTimestamp(pcommon.NewTimestampFromTime(st.Add(time.Duration(1+rnd.Intn(20)) * time.Millisecond)))
			a := sp.Attributes()
			a.PutStr("rpc.system", "grpc")
			a.PutStr("rpc.service", d.svc)
			a.PutStr("net.peer.name", d.svc)
			a.PutInt("rpc.grpc.status_code", 0)
			a.PutStr("app.user.id", fmt.Sprintf("u-%06d", rnd.Intn(1000000)))
			if d.cat != "" {
				cat := d.cat
				if isDrift && d.svc == "paymentservice" {
					cat = "health data"
				}
				a.PutStr("tilt.dataDisclosed.category", cat)
				a.PutStr("tilt.dataDisclosed.legalBases.reference", "GDPR-6-1-b")
				a.PutStr("tilt.dataDisclosed.purposes.purpose", d.purpose)
			}
			nspans++
		}
	}
	return td, nspans, ndrift
}

// Parts of this file were developed with the assistance of Claude (Anthropic) and reviewed by the authors.
// rdtool: rare-drift sweep tool. Subcommand "gen": synthetic OTLP/gRPC trace generator (derived from the benchmark
// generator tiltgen) with per-trace drift injection and a ground-truth log; subcommand "sink": OTLP/gRPC sink that logs
// every retained trace id (arrival time, span count) instead of discarding it.
//
// Emits multi-span traces that mimic the request types of the instrumented Astronomy Shop:
//   browse   (50 %): frontend, productcatalogservice x2, currencyservice x2, recommendationservice    6 spans, no personal data
//   cart     (30 %): frontend, cartservice GetCart [user id], cartservice HGET (unannotated),
//                    productcatalogservice x2                                                         5 spans
//   checkout (20 %): frontend, checkoutservice PlaceOrder [customer email], checkoutservice gRPC client
//                    (unannotated), cartservice GetCart [user id], paymentservice Charge [Credit Card],
//                    shippingservice ShipOrder [tracking id], emailservice [customer email]          7 spans
// Each trace is, with probability -drift, a drift trace: a checkout trace with
//   class V1: paymentservice reports the undeclared category "health data" (service is listed in the TILT),
//   class V3: the emailservice span is emitted by the service "analyticsservice" that is not listed in the TILT
//             (it keeps its annotation "customer email").
// Every injected trace id is written to the ground-truth log (-gt).
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
	"bufio"
	"os/signal"
	"syscall"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/pdata/ptrace/ptraceotlp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	_ "google.golang.org/grpc/encoding/gzip"
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
	if len(os.Args) < 2 {
		log.Fatal("usage: rdtool gen|sink [flags]")
	}
	switch os.Args[1] {
	case "gen":
		runGen(os.Args[2:])
	case "sink":
		runSink(os.Args[2:])
	default:
		log.Fatal("unknown subcommand ", os.Args[1])
	}
}

type sinkSrv struct {
	ptraceotlp.UnimplementedGRPCServer
	mu sync.Mutex
	w  *bufio.Writer
	n  atomic.Int64
}

func (s *sinkSrv) Export(_ context.Context, req ptraceotlp.ExportRequest) (ptraceotlp.ExportResponse, error) {
	now := time.Now().UnixNano()
	cnt := map[[16]byte]int{}
	rs := req.Traces().ResourceSpans()
	for i := 0; i < rs.Len(); i++ {
		ss := rs.At(i).ScopeSpans()
		for j := 0; j < ss.Len(); j++ {
			sp := ss.At(j).Spans()
			for k := 0; k < sp.Len(); k++ {
				cnt[[16]byte(sp.At(k).TraceID())]++
			}
		}
	}
	s.mu.Lock()
	for id, c := range cnt {
		fmt.Fprintf(s.w, "%d\t%x\t%d\n", now, id, c)
	}
	s.mu.Unlock()
	s.n.Add(1)
	return ptraceotlp.NewExportResponse(), nil
}

// runSink: one line per (export request, trace): unix_ns, trace_id, spans.
func runSink(args []string) {
	fs := flag.NewFlagSet("sink", flag.ExitOnError)
	addr := fs.String("listen", ":4317", "listen address")
	out := fs.String("out", "/data/retained.tsv", "output TSV")
	_ = fs.Parse(args)
	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	s := &sinkSrv{w: bufio.NewWriterSize(f, 1<<20)}
	lis, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	g := grpc.NewServer(grpc.MaxRecvMsgSize(64 << 20))
	ptraceotlp.RegisterGRPCServer(g, s)
	go func() {
		for range time.Tick(time.Second) {
			s.mu.Lock()
			_ = s.w.Flush()
			s.mu.Unlock()
		}
	}()
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGTERM, syscall.SIGINT)
		<-c
		s.mu.Lock()
		_ = s.w.Flush()
		_ = f.Sync()
		s.mu.Unlock()
		os.Exit(0)
	}()
	log.Fatal(g.Serve(lis))
}

func runGen(args []string) {
	fs := flag.NewFlagSet("gen", flag.ExitOnError)
	endpoint := fs.String("endpoint", "collector:4317", "OTLP gRPC endpoint")
	rate := fs.Float64("rate", 1000, "offered load in spans/s")
	duration := fs.Duration("duration", time.Minute, "how long to send")
	workers := fs.Int("workers", 4, "concurrent senders")
	tracesPerBatch := fs.Int("batch-traces", 20, "traces per OTLP export request")
	drift := fs.Float64("drift", 0.001, "per-trace probability of a drift trace")
	class := fs.String("class", "V1", "drift class: V1 (undeclared category) or V3 (unlisted service)")
	seed := fs.Int64("seed", 1, "random seed")
	gtPath := fs.String("gt", "", "ground-truth log (trace_id, class, unix_ns)")
	out := fs.String("out", "", "write JSON summary to this file")
	_ = fs.Parse(args)

	conn, err := grpc.Dial(*endpoint, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		log.Fatal(err)
	}
	client := ptraceotlp.NewGRPCClient(conn)
	var gtMu sync.Mutex
	var gtW *bufio.Writer
	if *gtPath != "" {
		f, err := os.Create(*gtPath)
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		gtW = bufio.NewWriter(f)
		defer gtW.Flush()
	}
	var st stats
	// mean spans/trace with drift traces forced to the 7-span checkout template
	meanSpans := (1-*drift)*(0.5*6+0.3*5+0.2*7) + *drift*7
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
				td, nspans, gt := buildBatch(rnd, *tracesPerBatch, *drift, *class)
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
					st.driftTraces.Add(int64(len(gt)))
					if gtW != nil && len(gt) > 0 {
						gtMu.Lock()
						for _, l := range gt {
							fmt.Fprintf(gtW, "%s\t%s\t%d\n", l, *class, now.UnixNano())
						}
						gtMu.Unlock()
					}
				}
			}
		}(w)
	}
	wg.Wait()
	el := time.Since(start).Seconds()
	res := map[string]any{
		"offered_spans_per_s": *rate, "elapsed_s": el, "start_unix_ns": start.UnixNano(), "sent_spans": st.spans.Load(), "sent_traces": st.traces.Load(),
		"failed_spans": st.errSpans.Load(), "batches": st.batches.Load(), "failed_batches": st.errBatches.Load(),
		"drift_traces": st.driftTraces.Load(), "achieved_spans_per_s": float64(st.spans.Load()) / el,
		"drift_p": *drift, "class": *class, "seed": *seed,
	}
	b, _ := json.Marshal(res)
	fmt.Println(string(b))
	if *out != "" {
		_ = os.WriteFile(*out, b, 0o644)
	}
}

// buildBatch returns the traces, the span count and the hex ids of the injected drift traces.
func buildBatch(rnd *rand.Rand, n int, drift float64, class string) (ptrace.Traces, int, []string) {
	td := ptrace.NewTraces()
	nspans := 0
	var gt []string
	now := time.Now()
	for i := 0; i < n; i++ {
		var tmpl []spanDef
		isDrift := rnd.Float64() < drift
		x := rnd.Float64()
		switch {
		case isDrift:
			tmpl = checkout
		case x < 0.5:
			tmpl = browse
		case x < 0.8:
			tmpl = cart
		default:
			tmpl = checkout
		}
		var tid [16]byte
		binary.BigEndian.PutUint64(tid[:8], rnd.Uint64())
		binary.BigEndian.PutUint64(tid[8:], rnd.Uint64())
		if isDrift {
			gt = append(gt, fmt.Sprintf("%x", tid))
		}
		ids := make([][8]byte, len(tmpl))
		for j := range tmpl {
			binary.BigEndian.PutUint64(ids[j][:], rnd.Uint64()|1)
		}
		for j, d := range tmpl {
			svc := d.svc
			if isDrift && class == "V3" && d.svc == "emailservice" {
				svc = "analyticsservice"
			}
			rs := td.ResourceSpans().AppendEmpty()
			ra := rs.Resource().Attributes()
			ra.PutStr("service.name", svc)
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
			a.PutStr("rpc.service", svc)
			a.PutStr("net.peer.name", svc)
			a.PutInt("rpc.grpc.status_code", 0)
			a.PutStr("app.user.id", fmt.Sprintf("u-%06d", rnd.Intn(1000000)))
			if d.cat != "" {
				cat := d.cat
				if isDrift && class == "V1" && d.svc == "paymentservice" {
					cat = "health data"
				}
				a.PutStr("tilt.dataDisclosed.category", cat)
				a.PutStr("tilt.dataDisclosed.legalBases.reference", "GDPR-6-1-b")
				a.PutStr("tilt.dataDisclosed.purposes.purpose", d.purpose)
			}
			nspans++
		}
	}
	return td, nspans, gt
}

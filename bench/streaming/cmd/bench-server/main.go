// Command bench-server is the multi-transport token-streaming server for the
// Phase-2 streaming bake-off. It serves the SAME deterministic token stub over:
//
//	SSE / HTTP-1.1   plaintext listener            (-h1-addr)
//	SSE / HTTP-2     TLS listener, ALPN h2         (-h2-addr)
//	WebSocket        plaintext listener            (-ws-addr)
//	gRPC stream      HTTP/2 (h2c or TLS)           (-grpc-addr)
//	WebTransport     HTTP/3 over QUIC (if built)   (-wt-addr, requires -wt)
//
// Every listener exposes GET /stats (JSON) for CPU/mem/active-stream sampling and
// the §7.3 cancellation counters. This is transport measurement only: the "model"
// is a fixed token-rate stub, so all deltas are attributable to the wire protocol.
package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	pb "aisotropy.local/bench/streaming/proto/streampb"
)

type server struct {
	metrics *serverMetrics
}

func main() {
	h1Addr := flag.String("h1-addr", ":18085", "plaintext HTTP/1.1 SSE listener")
	h2Addr := flag.String("h2-addr", ":18086", "TLS HTTP/2 SSE listener (ALPN h2)")
	wsAddr := flag.String("ws-addr", ":18087", "plaintext WebSocket listener")
	grpcAddr := flag.String("grpc-addr", ":18088", "gRPC listener")
	grpcTLS := flag.Bool("grpc-tls", false, "serve gRPC over TLS (else h2c cleartext)")
	wtAddr := flag.String("wt-addr", ":18089", "WebTransport (HTTP/3) UDP listener")
	enableWT := flag.Bool("wt", false, "enable the WebTransport listener (experimental)")
	flag.Parse()

	m := &serverMetrics{}
	s := &server{metrics: m}

	tlsCfg, err := selfSignedTLS()
	if err != nil {
		log.Fatalf("tls: %v", err)
	}

	// --- HTTP/1.1 plaintext SSE ---
	h1mux := http.NewServeMux()
	h1mux.HandleFunc("/sse", s.sseHandler)
	h1mux.HandleFunc("/stats", m.statsHandler)
	h1mux.HandleFunc("/capdemo", capDemoHandler)
	h1 := &http.Server{Addr: *h1Addr, Handler: h1mux}
	// Force HTTP/1.1 only: nil TLSNextProto with plaintext keeps us on h1.
	go func() {
		log.Printf("[h1-sse ] plaintext HTTP/1.1 SSE on %s (/sse, /stats, /capdemo)", *h1Addr)
		if err := h1.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("h1 server: %v", err)
		}
	}()

	// --- HTTP/2 TLS SSE ---
	h2mux := http.NewServeMux()
	h2mux.HandleFunc("/sse", s.sseHandler)
	h2mux.HandleFunc("/stats", m.statsHandler)
	h2mux.HandleFunc("/capdemo", capDemoHandler)
	h2 := &http.Server{Addr: *h2Addr, Handler: h2mux, TLSConfig: tlsCfg}
	go func() {
		log.Printf("[h2-sse ] TLS HTTP/2 SSE on %s (ALPN h2)", *h2Addr)
		if err := h2.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			log.Printf("h2 server: %v", err)
		}
	}()

	// --- WebSocket (plaintext HTTP/1.1 upgrade) ---
	wsmux := http.NewServeMux()
	wsmux.HandleFunc("/ws", s.wsHandler)
	wsmux.HandleFunc("/stats", m.statsHandler)
	ws := &http.Server{Addr: *wsAddr, Handler: wsmux}
	go func() {
		log.Printf("[ws     ] WebSocket on %s (/ws, /stats)", *wsAddr)
		if err := ws.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("ws server: %v", err)
		}
	}()

	// --- gRPC server-streaming ---
	var grpcSrv *grpc.Server
	if *grpcTLS {
		grpcSrv = grpc.NewServer(grpc.Creds(credentials.NewTLS(tlsCfg)))
	} else {
		grpcSrv = grpc.NewServer()
	}
	pb.RegisterTokenStreamerServer(grpcSrv, &grpcTokenServer{s: s})
	go func() {
		lis, err := net.Listen("tcp", *grpcAddr)
		if err != nil {
			log.Printf("grpc listen: %v", err)
			return
		}
		log.Printf("[grpc   ] gRPC TokenStreamer on %s (tls=%v)", *grpcAddr, *grpcTLS)
		if err := grpcSrv.Serve(lis); err != nil {
			log.Printf("grpc server: %v", err)
		}
	}()

	// --- WebTransport (experimental, opt-in) ---
	var wtStop func()
	if *enableWT {
		wtStop, err = startWebTransport(*wtAddr, tlsCfg, s)
		if err != nil {
			log.Printf("[wt     ] WebTransport DISABLED: %v", err)
		} else {
			log.Printf("[wt     ] WebTransport (HTTP/3) on %s", *wtAddr)
		}
	}

	log.Printf("bench-server up. Ctrl-C to stop.")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig

	ctx := context.Background()
	_ = h1.Shutdown(ctx)
	_ = h2.Shutdown(ctx)
	_ = ws.Shutdown(ctx)
	grpcSrv.GracefulStop()
	if wtStop != nil {
		wtStop()
	}
	log.Printf("bench-server stopped.")
}

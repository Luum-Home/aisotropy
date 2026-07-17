package main

import (
	"context"
	"crypto/tls"
	"net/http"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"

	"aisotropy.local/bench/streaming/internal/tokens"
)

// startWebTransport brings up an HTTP/3 + WebTransport listener that streams the
// same token stub over a unidirectional WebTransport stream. It is opt-in (-wt)
// and returns an error rather than fatally exiting if the QUIC/H3 stack cannot
// bind, because the Go WebTransport server story is still maturing (see README).
func startWebTransport(addr string, tlsCfg *tls.Config, s *server) (func(), error) {
	// WebTransport requires ALPN h3 + HTTP/3 datagrams + the WebTransport SETTINGS
	// advertised via webtransport.ConfigureHTTP3Server — without that last call the
	// client aborts with "server didn't enable WebTransport" (the exact fiddly
	// wiring the research flagged as immature).
	wtTLS := tlsCfg.Clone()
	wtTLS.NextProtos = []string{"h3"}

	h3 := &http3.Server{
		Addr:      addr,
		TLSConfig: http3.ConfigureTLSConfig(wtTLS),
		QUICConfig: &quic.Config{
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
		},
	}
	webtransport.ConfigureHTTP3Server(h3)

	wtSrv := &webtransport.Server{
		H3:          h3,
		CheckOrigin: func(*http.Request) bool { return true },
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/wt", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wtSrv.Upgrade(w, r)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ctx := conn.Context()
		stream, err := conn.OpenUniStreamSync(ctx)
		if err != nil {
			return
		}
		st := tokens.DefaultStream()
		s.metrics.open()
		completed := false
		defer func() { s.metrics.close(completed) }()
		buf := make([]byte, 0, 64)
		emitErr := st.Emit(ctx, func(t tokens.Token) error {
			buf = buf[:0]
			buf = append(buf, t.Text...)
			buf = append(buf, '\n')
			if _, werr := stream.Write(buf); werr != nil {
				return werr
			}
			s.metrics.tokensSent.Add(1)
			return nil
		})
		if emitErr == nil {
			completed = true
		}
		_ = stream.Close()
	})
	wtSrv.H3.Handler = mux

	errCh := make(chan error, 1)
	go func() {
		errCh <- wtSrv.ListenAndServe()
	}()
	// Give the UDP bind a moment to fail fast if the port/stack is unavailable.
	select {
	case err := <-errCh:
		return nil, err
	case <-time.After(250 * time.Millisecond):
	}
	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = wtSrv.Close()
		_ = ctx
	}
	return stop, nil
}

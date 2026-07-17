package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"

	"aisotropy.local/bench/streaming/internal/tokens"
)

// desiredUDPBuffer is the per-socket kernel UDP buffer quic-go wants (7 MB, its
// protocol.DesiredReceiveBufferSize/DesiredSendBufferSize). We request it
// explicitly on the shared listener socket. On macOS this only actually lands if
// kern.ipc.maxsockbuf is >= this value (default 8 MB is enough); see README
// "UDP buffer precondition". If the OS clamps it lower, quic-go logs a one-shot
// warning and QUIC still runs, just with a smaller reassembly window.
const desiredUDPBuffer = 7 << 20 // 7 MB

// startWebTransport brings up an HTTP/3 + WebTransport listener that streams the
// same token stub over a unidirectional WebTransport stream. It is opt-in (-wt)
// and returns an error rather than fatally exiting if the QUIC/H3 stack cannot
// bind.
//
// Two things make this hold up under 1000+ concurrent sessions where the naive
// setup collapsed (see README "WebTransport, root-caused"):
//  1. ONE explicitly big-buffered UDP socket serves every session. QUIC
//     multiplexes all connections over it via connection IDs, exactly like h2
//     multiplexes streams over one TCP socket. (The default ListenAndServe path
//     binds a socket for us, but we bind it ourselves so we can size the buffer
//     and so the client/server socket models match.)
//  2. A tuned quic.Config: raised per-connection stream limits and idle/handshake
//     timeouts, so a burst of handshakes has headroom instead of starving.
func startWebTransport(addr string, tlsCfg *tls.Config, s *server) (func(), error) {
	// WebTransport requires ALPN h3 + HTTP/3 datagrams + the WebTransport SETTINGS
	// advertised via webtransport.ConfigureHTTP3Server — without that last call the
	// client aborts with "server didn't enable WebTransport".
	wtTLS := tlsCfg.Clone()
	wtTLS.NextProtos = []string{"h3"}

	h3 := &http3.Server{
		Addr:      addr,
		TLSConfig: http3.ConfigureTLSConfig(wtTLS),
		QUICConfig: &quic.Config{
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
			// The default per-connection stream limit is 100. Each session here
			// carries one server-opened uni stream, but we lift both limits well
			// clear of the default so nothing throttles at the stream layer.
			MaxIncomingStreams:    2048,
			MaxIncomingUniStreams: 2048,
			// Give a handshake burst room: the default 5s handshake-idle timeout is
			// what the naive setup tripped ("waiting for HTTP/3 settings" timeout).
			HandshakeIdleTimeout: 30 * time.Second,
			MaxIdleTimeout:       60 * time.Second,
			KeepAlivePeriod:      15 * time.Second,
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

	// Bind ONE UDP socket ourselves and size its kernel buffers up front, then hand
	// it to Serve. This is the server-side half of "one shared socket for all
	// sessions": every QUIC connection is demuxed off this single socket.
	udpAddr, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("resolve wt udp addr: %w", err)
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return nil, fmt.Errorf("listen wt udp: %w", err)
	}
	_ = udpConn.SetReadBuffer(desiredUDPBuffer)
	_ = udpConn.SetWriteBuffer(desiredUDPBuffer)

	errCh := make(chan error, 1)
	go func() {
		errCh <- wtSrv.Serve(udpConn)
	}()
	// Give the UDP bind a moment to fail fast if the stack is unavailable.
	select {
	case err := <-errCh:
		_ = udpConn.Close()
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

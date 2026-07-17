package main

import (
	"time"

	"aisotropy.local/bench/streaming/internal/tokens"
	pb "aisotropy.local/bench/streaming/proto/streampb"
)

// grpcTokenServer implements the generated TokenStreamer server-streaming RPC.
type grpcTokenServer struct {
	pb.UnimplementedTokenStreamerServer
	s *server
}

func (g *grpcTokenServer) Stream(req *pb.StreamRequest, stream pb.TokenStreamer_StreamServer) error {
	st := tokens.DefaultStream()
	if req.GetCount() > 0 {
		st.Count = int(req.GetCount())
	}
	if req.GetIntervalMs() > 0 {
		st.Interval = time.Duration(req.GetIntervalMs()) * time.Millisecond
	}

	g.s.metrics.open()
	completed := false
	defer func() { g.s.metrics.close(completed) }()

	// stream.Context() is cancelled when the client goes away — HTTP/2 RST_STREAM
	// propagates as ctx cancellation, the same §7.3 correctness gate as SSE/WS.
	ctx := stream.Context()
	err := st.Emit(ctx, func(t tokens.Token) error {
		if serr := stream.Send(&pb.TokenChunk{
			Index:        int32(t.Index),
			Text:         t.Text,
			SentUnixNano: time.Now().UnixNano(),
		}); serr != nil {
			return serr
		}
		g.s.metrics.tokensSent.Add(1)
		return nil
	})
	if err == nil {
		completed = true
	}
	return err
}

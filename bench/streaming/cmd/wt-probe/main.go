// Command wt-probe attempts a single WebTransport session against bench-server to
// determine whether the Go WebTransport datapath works end-to-end at all. It is
// a HONESTY probe, not a benchmark: if the server story is too immature to drive
// reliably, this prints the failure so the bake-off can defer WT with evidence.
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"
)

func main() {
	var d webtransport.Dialer
	d.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h3"}}
	d.QUICConfig = &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, conn, err := d.Dial(ctx, "https://127.0.0.1:18089/wt", nil)
	if err != nil {
		fmt.Printf("WT dial FAILED: %v\n", err)
		return
	}
	defer conn.CloseWithError(0, "done")
	fmt.Printf("WT session established in %s\n", time.Since(start).Round(time.Millisecond))
	s, err := conn.AcceptUniStream(ctx)
	if err != nil {
		fmt.Printf("WT AcceptUniStream FAILED: %v\n", err)
		return
	}
	r := bufio.NewReader(s)
	n, firstAt := 0, time.Time{}
	for {
		_, err := r.ReadString('\n')
		if err == io.EOF || err != nil {
			break
		}
		if n == 0 {
			firstAt = time.Now()
		}
		n++
	}
	fmt.Printf("WT tokens received: %d, TTFT=%s, total=%s\n",
		n, firstAt.Sub(start).Round(time.Millisecond), time.Since(start).Round(time.Millisecond))
}

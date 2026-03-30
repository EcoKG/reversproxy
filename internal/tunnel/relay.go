package tunnel

import (
	"context"
	"io"
	"net"
	"sync"
	"time"
)

var relayBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 32*1024)
		return &buf
	},
}

// RelayBiDirectional copies data between a and b until ctx is cancelled,
// either side is closed, or an error occurs. Both connections are closed
// before this function returns.
func RelayBiDirectional(ctx context.Context, a, b net.Conn) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// When context is cancelled, unblock any blocked Read/Write by setting
	// a deadline in the past.
	go func() {
		<-ctx.Done()
		past := time.Now().Add(-1 * time.Second)
		a.SetDeadline(past)
		b.SetDeadline(past)
	}()

	var once sync.Once
	done := make(chan struct{}, 2)

	relay := func(dst, src net.Conn) {
		bufp := relayBufPool.Get().(*[]byte)
		defer relayBufPool.Put(bufp)
		io.CopyBuffer(dst, src, *bufp)
		// Half-close write side if possible
		if tc, ok := dst.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		once.Do(func() {
			// After one direction ends, give the other direction a grace period
			cancel()
		})
		done <- struct{}{}
	}

	go relay(a, b)
	go relay(b, a)
	<-done
	<-done

	a.Close()
	b.Close()
	return nil
}

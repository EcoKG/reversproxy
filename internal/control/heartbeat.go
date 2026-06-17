package control

import (
	"context"
	"log/slog"
	"time"

	"github.com/EcoKG/reversproxy/internal/config"
	"github.com/EcoKG/reversproxy/internal/protocol"
)

const (
	maxMissed = 2
)

// StartHeartbeat sends periodic Ping messages to the client and tracks missed
// Pong responses. If the client fails to respond within the allowed miss window,
// StartHeartbeat logs "heartbeat timeout" and cancels the client's context to
// trigger a disconnect.
//
// It must be called in its own goroutine; it returns when either ctx is
// cancelled (normal shutdown) or the miss limit is exceeded.
func StartHeartbeat(ctx context.Context, client *Client, log *slog.Logger) {
	ticker := time.NewTicker(config.HeartbeatInterval)
	defer ticker.Stop()

	missed := 0

	for {
		select {
		case <-ctx.Done():
			return

		case <-ticker.C:
			// Check whether the client has stopped responding altogether.
			// If the last heartbeat acknowledgement is older than
			// pingInterval*(maxMissed+1) AND we have already fired maxMissed
			// unanswered pings, treat the connection as dead.
			if time.Since(client.LastHeartbeat()) > config.HeartbeatInterval*time.Duration(maxMissed+1) && missed >= maxMissed {
				log.Warn("heartbeat timeout", "id", client.ID)
				client.cancelFn()
				return
			}

			// Send Ping through the serialising writer so it cannot interleave
			// with proxy/control writes on the same connection.
			if err := client.Writer.Write(protocol.MsgPing, protocol.Ping{Seq: uint64(missed)}); err != nil {
				log.Warn("ping write failed", "id", client.ID, "err", err)
				client.cancelFn()
				return
			}

			missed++
		}
	}
}

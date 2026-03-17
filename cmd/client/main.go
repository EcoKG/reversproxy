package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/gob"
	"flag"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/starlyn/reversproxy/internal/control"
	"github.com/starlyn/reversproxy/internal/logger"
	"github.com/starlyn/reversproxy/internal/protocol"
)

func main() {
	server := flag.String("server", "localhost:8443", "server address (host:port)")
	token := flag.String("token", "changeme", "pre-shared auth token")
	name := flag.String("name", "client1", "client label sent to the server")
	insecure := flag.Bool("insecure", true, "skip TLS certificate verification (dev only)")
	flag.Parse()

	log := logger.New("client")

	tlsCfg := control.NewClientTLSConfig(*insecure)
	conn, err := tls.Dial("tcp", *server, tlsCfg)
	if err != nil {
		log.Error("failed to connect to server", "server", *server, "err", err)
		os.Exit(1)
	}
	defer conn.Close()

	// ------------------------------------------------------------------ //
	// Registration handshake
	// ------------------------------------------------------------------ //

	if err := protocol.WriteMessage(conn, protocol.MsgClientRegister, protocol.ClientRegister{
		AuthToken: *token,
		Name:      *name,
		Version:   "0.1.0",
	}); err != nil {
		log.Error("failed to send ClientRegister", "err", err)
		os.Exit(1)
	}

	env, err := protocol.ReadMessage(conn)
	if err != nil {
		log.Error("failed to read RegisterResp", "err", err)
		os.Exit(1)
	}

	var resp protocol.RegisterResp
	if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&resp); err != nil {
		log.Error("failed to decode RegisterResp", "err", err)
		os.Exit(1)
	}

	if resp.Status != "ok" {
		log.Error("registration failed", "error", resp.Error)
		os.Exit(1)
	}

	log.Info("registered", "client_id", resp.AssignedClientID)

	// ------------------------------------------------------------------ //
	// Signal handling — graceful disconnect on SIGINT / SIGTERM
	// ------------------------------------------------------------------ //

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		log.Info("shutting down, sending disconnect")

		_ = protocol.WriteMessage(conn, protocol.MsgDisconnect, protocol.Disconnect{Reason: "client shutdown"})

		// Give the server 2 seconds to acknowledge.
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		_, _ = protocol.ReadMessage(conn) // DisconnectAck (best-effort)

		conn.Close()
		os.Exit(0)
	}()

	// ------------------------------------------------------------------ //
	// Message loop
	// ------------------------------------------------------------------ //

	for {
		env, err := protocol.ReadMessage(conn)
		if err != nil {
			log.Error("connection lost", "err", err)
			os.Exit(1)
		}

		switch env.Type {
		case protocol.MsgPing:
			var ping protocol.Ping
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&ping); err != nil {
				log.Warn("failed to decode Ping", "err", err)
				continue
			}
			if err := protocol.WriteMessage(conn, protocol.MsgPong, protocol.Pong{Seq: ping.Seq}); err != nil {
				log.Error("connection lost", "err", err)
				os.Exit(1)
			}
			log.Debug("pong sent", "seq", ping.Seq)

		case protocol.MsgDisconnect:
			var disc protocol.Disconnect
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&disc); err != nil {
				log.Warn("failed to decode Disconnect from server", "err", err)
			} else {
				log.Info("server requested disconnect", "reason", disc.Reason)
			}
			_ = protocol.WriteMessage(conn, protocol.MsgDisconnectAck, protocol.DisconnectAck{})
			os.Exit(0)

		default:
			log.Warn("unhandled message type", "type", env.Type)
		}
	}
}

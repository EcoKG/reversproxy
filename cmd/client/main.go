package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/gob"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/starlyn/reversproxy/internal/control"
	"github.com/starlyn/reversproxy/internal/logger"
	"github.com/starlyn/reversproxy/internal/protocol"
	"github.com/starlyn/reversproxy/internal/tunnel"
)

func main() {
	server    := flag.String("server",     "localhost:8443", "server address (host:port)")
	token     := flag.String("token",      "changeme",       "pre-shared auth token")
	name      := flag.String("name",       "client1",        "client label sent to the server")
	insecure  := flag.Bool("insecure",     true,             "skip TLS certificate verification (dev only)")
	localHost := flag.String("local-host", "127.0.0.1",      "local service hostname to tunnel")
	localPort := flag.Int("local-port",    0,                "local service port to tunnel (0 = no tunnel)")
	pubPort   := flag.Int("pub-port",      0,                "requested public port on server (0 = any)")
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
	// Optional tunnel request
	// ------------------------------------------------------------------ //

	// serverDataAddr is learned from the TunnelResp message.
	// It maps tunnelID → serverDataAddr so multiple tunnels are supported.
	tunnelDataAddrs := make(map[string]string)

	if *localPort > 0 {
		req := protocol.RequestTunnel{
			LocalHost:     *localHost,
			LocalPort:     *localPort,
			RequestedPort: *pubPort,
		}
		if err := protocol.WriteMessage(conn, protocol.MsgRequestTunnel, req); err != nil {
			log.Error("failed to send RequestTunnel", "err", err)
			os.Exit(1)
		}

		tenv, err := protocol.ReadMessage(conn)
		if err != nil {
			log.Error("failed to read TunnelResp", "err", err)
			os.Exit(1)
		}
		if tenv.Type != protocol.MsgTunnelResp {
			log.Error("expected TunnelResp", "got", tenv.Type)
			os.Exit(1)
		}

		var tresp protocol.TunnelResp
		if err := gob.NewDecoder(bytes.NewReader(tenv.Payload)).Decode(&tresp); err != nil {
			log.Error("failed to decode TunnelResp", "err", err)
			os.Exit(1)
		}

		if tresp.Status != "ok" {
			log.Error("tunnel request failed", "error", tresp.Error)
			os.Exit(1)
		}

		tunnelDataAddrs[tresp.TunnelID] = tresp.ServerDataAddr
		log.Info("tunnel established",
			"tunnelID", tresp.TunnelID,
			"publicPort", tresp.PublicPort,
			"serverDataAddr", tresp.ServerDataAddr,
		)
		fmt.Printf("Tunnel: 0.0.0.0:%d → %s:%d\n", tresp.PublicPort, *localHost, *localPort)
	}

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

		case protocol.MsgOpenConnection:
			var openConn protocol.OpenConnection
			if err := gob.NewDecoder(bytes.NewReader(env.Payload)).Decode(&openConn); err != nil {
				log.Warn("failed to decode OpenConnection", "err", err)
				continue
			}
			dataAddr, ok := tunnelDataAddrs[openConn.TunnelID]
			if !ok {
				log.Warn("received OpenConnection for unknown tunnelID", "tunnelID", openConn.TunnelID)
				continue
			}
			tunnel.HandleOpenConnection(openConn, dataAddr, log)

		default:
			log.Warn("unhandled message type", "type", env.Type)
		}
	}
}

---
type: code
scale: medium
paradigm: mixed
language: go
phase_ref:
  phase_number: 1
  phase_name: "Control Plane 연결"
  milestone: 1
  success_criteria_seed:
    - "클라이언트가 프록시 서버에 연결하면 서버 로그에 클라이언트 등록이 확인된다"
    - "여러 클라이언트가 동시에 프록시 서버에 접속하며 각각 독립적으로 식별된다"
    - "클라이언트 또는 서버를 종료하면 상대측이 연결 해제를 감지한다"
phases: 1
total_tasks: 11
waves: 4
must_haves:
  truths:
    - "클라이언트가 프록시 서버에 연결하면 서버 로그에 'client registered' 메시지와 함께 UUID, 이름, 주소가 출력된다"
    - "두 클라이언트를 동시에 연결하면 각각 고유한 UUID가 할당되어 서버 registry에서 독립적으로 식별된다"
    - "클라이언트를 종료하면 서버 로그에 'client disconnected' 또는 'heartbeat timeout' 이벤트가 기록된다"
    - "서버를 종료하면 클라이언트 로그에 연결 해제 이벤트가 기록되고 프로세스가 종료된다"
    - "go build ./... 가 오류 없이 성공한다"
    - "go test -race ./... 가 오류 없이 성공한다"
    - "모든 제어 연결은 TLS 1.3으로 암호화된다 (평문 TCP 연결 불가)"
  artifacts:
    - path: go.mod
      min_lines: 8
      exports: []
    - path: internal/protocol/messages.go
      min_lines: 50
      exports: [MsgType, ClientRegister, RegisterResp, Ping, Pong, Disconnect, DisconnectAck, Envelope]
    - path: internal/protocol/framing.go
      min_lines: 40
      exports: [ReadMessage, WriteMessage]
    - path: internal/logger/logger.go
      min_lines: 20
      exports: [New, With]
    - path: internal/control/tls.go
      min_lines: 40
      exports: [LoadOrGenerateCert, NewServerTLSConfig, NewClientTLSConfig]
    - path: internal/control/registry.go
      min_lines: 50
      exports: [Client, ClientRegistry, NewClientRegistry, Register, Deregister, Get, List]
    - path: internal/control/handler.go
      min_lines: 80
      exports: [HandleControlConn]
    - path: internal/control/heartbeat.go
      min_lines: 50
      exports: [StartHeartbeat]
    - path: internal/proxy/stub.go
      min_lines: 5
      exports: []
    - path: cmd/server/main.go
      min_lines: 60
      exports: []
    - path: cmd/client/main.go
      min_lines: 60
      exports: []
    - path: internal/protocol/framing_test.go
      min_lines: 40
      exports: []
    - path: internal/control/registry_test.go
      min_lines: 40
      exports: []
    - path: internal/control/integration_test.go
      min_lines: 80
      exports: []
    - path: Makefile
      min_lines: 25
      exports: []
  key_links:
    - from: internal/control/handler.go
      to: internal/protocol/framing.go
      pattern: "ReadMessage|WriteMessage"
    - from: internal/control/handler.go
      to: internal/control/registry.go
      pattern: "Register|Deregister"
    - from: internal/control/handler.go
      to: internal/control/heartbeat.go
      pattern: "StartHeartbeat"
    - from: internal/control/handler.go
      to: internal/protocol/messages.go
      pattern: "ClientRegister|RegisterResp|Ping|Pong|Disconnect"
    - from: cmd/server/main.go
      to: internal/control/handler.go
      pattern: "HandleControlConn"
    - from: cmd/server/main.go
      to: internal/control/tls.go
      pattern: "NewServerTLSConfig"
    - from: cmd/client/main.go
      to: internal/control/tls.go
      pattern: "NewClientTLSConfig"
    - from: cmd/server/main.go
      to: internal/control/registry.go
      pattern: "NewClientRegistry"
---

# Implementation Plan: Phase 1 — Control Plane 연결

## Overview
클라이언트와 프록시 서버 간 TLS 1.3 기반 제어 채널을 수립한다. 메시지 framing, 등록 핸드셰이크, 클라이언트 registry, heartbeat, graceful shutdown을 구현하여 다중 클라이언트의 독립적 식별과 단절 감지가 가능한 상태를 만든다.

## Phase 1: Control Plane 연결

### Goal
클라이언트가 TLS로 서버에 연결하면 UUID가 할당되고, 서버 로그에 등록이 기록되며, heartbeat으로 생존을 확인하고, 종료 시 상대측이 이를 즉시 감지한다.

### Completion Criteria
- `go build ./...` 성공
- `go test -race ./...` 성공
- 서버 실행 후 클라이언트 연결 시 서버 로그에 `"client registered"` 출력
- 두 클라이언트 동시 연결 시 각각 다른 UUID 할당 확인
- 클라이언트 종료 후 서버 로그에 단절 이벤트 출력

### Tasks

<task id="1-1" wave="1" depends_on="">
  <name>Initialize Go module and project structure</name>
  <files>go.mod, Makefile, internal/proxy/stub.go</files>
  <read_first>/home/starlyn/reversproxy/</read_first>
  <action>
    1. Run `go mod init github.com/starlyn/reversproxy` in /home/starlyn/reversproxy/
    2. Run `go get github.com/google/uuid@latest`
    3. Create directory tree:
       - cmd/server/, cmd/client/
       - internal/protocol/, internal/control/, internal/logger/, internal/proxy/
    4. Create internal/proxy/stub.go with package declaration `package proxy` and a comment `// Phase 2: data plane (not implemented)`. No exported symbols needed.
    5. Create Makefile with targets:
       - `build`: `go build ./...`
       - `test`: `go test -race ./...`
       - `run-server`: `go run ./cmd/server -addr :8443 -token changeme`
       - `run-client`: `go run ./cmd/client -server localhost:8443 -token changeme -name client1`
       - `cert`: `go run ./cmd/server -gencert` (placeholder, implemented in task 1-3)
       - `lint`: `go vet ./...`
  </action>
  <verify>go build ./... 2>&1 || true; ls go.mod Makefile internal/proxy/stub.go</verify>
  <acceptance_criteria>
    - `grep "module github.com/starlyn/reversproxy" go.mod` returns match
    - `grep "go 1.22" go.mod` returns match
    - `grep "github.com/google/uuid" go.mod` returns match
    - `grep "go build ./..." Makefile` returns match
    - `ls internal/proxy/stub.go` succeeds
  </acceptance_criteria>
  <ref>M1, M5, L2</ref>
  <done>false</done>
</task>

<task id="1-2" wave="1" depends_on="">
  <name>Define protocol message types and envelope</name>
  <files>internal/protocol/messages.go</files>
  <read_first>/home/starlyn/reversproxy/internal/protocol/</read_first>
  <action>
    1. Create internal/protocol/messages.go with `package protocol`
    2. Define MsgType as `type MsgType uint8` with constants:
       - MsgClientRegister MsgType = 1
       - MsgRegisterResp   MsgType = 2
       - MsgPing           MsgType = 3
       - MsgPong           MsgType = 4
       - MsgDisconnect     MsgType = 5
       - MsgDisconnectAck  MsgType = 6
    3. `Envelope{Type MsgType, Payload []byte}`
    4. `ClientRegister{AuthToken, Name, Version string}`
    5. `RegisterResp{AssignedClientID, ServerID, Status, Error string}` — Status is "ok" or "error"
    6. `Ping{Seq uint64}`, `Pong{Seq uint64}`, `Disconnect{Reason string}`, `DisconnectAck struct{}`
    7. `const MaxMessageSize = 1 * 1024 * 1024` (1MB DoS limit)
  </action>
  <verify>grep -n "MsgType\|ClientRegister\|RegisterResp\|Ping\|Pong\|Disconnect" internal/protocol/messages.go</verify>
  <acceptance_criteria>
    - `grep "MsgType" internal/protocol/messages.go` returns match
    - `grep "MaxMessageSize" internal/protocol/messages.go` returns match
    - `grep "AuthToken" internal/protocol/messages.go` returns match
    - `grep "AssignedClientID" internal/protocol/messages.go` returns match
    - `go build ./internal/protocol/...` succeeds
  </acceptance_criteria>
  <ref>H1, H2, M3</ref>
  <done>false</done>
</task>

<task id="1-3" wave="1" depends_on="">
  <name>Implement logger package with slog JSON handler</name>
  <files>internal/logger/logger.go</files>
  <read_first>/home/starlyn/reversproxy/internal/logger/</read_first>
  <action>
    1. Create internal/logger/logger.go with `package logger`
    2. Import `log/slog` and `os`
    3. Implement `New(component string) *slog.Logger`:
       - Create a `slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})`
       - Return `slog.New(handler).With("component", component)`
    4. Implement `With(logger *slog.Logger, args ...any) *slog.Logger`:
       - Return `logger.With(args...)`
    5. Create package-level default logger: `var Default = New("app")`
  </action>
  <verify>grep -n "func New\|func With\|slog" internal/logger/logger.go</verify>
  <acceptance_criteria>
    - `grep "func New" internal/logger/logger.go` returns match
    - `grep "slog.NewJSONHandler" internal/logger/logger.go` returns match
    - `grep "component" internal/logger/logger.go` returns match
    - `go build ./internal/logger/...` succeeds
  </acceptance_criteria>
  <ref>H2, M1</ref>
  <done>false</done>
</task>

<task id="1-4" wave="2" depends_on="1-1,1-2">
  <name>Implement length-prefixed binary framing I/O</name>
  <files>internal/protocol/framing.go, internal/protocol/framing_test.go</files>
  <read_first>internal/protocol/messages.go</read_first>
  <action>
    1. Create internal/protocol/framing.go with `package protocol`
    2. Import `encoding/binary`, `encoding/gob`, `bytes`, `fmt`, `io`, `net`
    3. Implement `WriteMessage(conn net.Conn, msgType MsgType, payload any) error`:
       - Encode payload into a `bytes.Buffer` using `gob.NewEncoder`
       - Build Envelope{Type: msgType, Payload: buf.Bytes()}
       - Encode Envelope into second buffer using gob
       - Write 4-byte big-endian uint32 length prefix with `binary.Write(conn, binary.BigEndian, uint32(len))`
       - Write envelope bytes to conn
    4. Implement `ReadMessage(conn net.Conn) (*Envelope, error)`:
       - Read 4-byte big-endian uint32 length with `binary.Read`
       - If length > MaxMessageSize return `fmt.Errorf("message too large: %d bytes", length)`
       - Read exactly `length` bytes using `io.ReadFull`
       - Decode bytes into Envelope using gob
       - Return &envelope, nil
    5. Create internal/protocol/framing_test.go:
       - Use `net.Pipe()` to create in-memory connection pair
       - Test WriteMessage + ReadMessage roundtrip for ClientRegister
       - Test ReadMessage returns error when length > MaxMessageSize
       - Test ReadMessage returns error on truncated data (io.ErrUnexpectedEOF)
  </action>
  <verify>go test ./internal/protocol/... -v -run TestFraming</verify>
  <acceptance_criteria>
    - `grep "func WriteMessage" internal/protocol/framing.go` returns match
    - `grep "func ReadMessage" internal/protocol/framing.go` returns match
    - `grep "MaxMessageSize" internal/protocol/framing.go` returns match
    - `grep "binary.BigEndian" internal/protocol/framing.go` returns match
    - `go test ./internal/protocol/...` passes
  </acceptance_criteria>
  <ref>H1, M5</ref>
  <done>false</done>
</task>

<task id="1-5" wave="2" depends_on="1-1,1-3">
  <name>Implement TLS config helpers with self-signed cert generation</name>
  <files>internal/control/tls.go</files>
  <read_first>internal/logger/logger.go, /home/starlyn/reversproxy/internal/control/</read_first>
  <action>
    1. Create internal/control/tls.go with `package control`
    2. Import `crypto/ecdsa`, `crypto/elliptic`, `crypto/rand`, `crypto/tls`, `crypto/x509`, `crypto/x509/pkix`, `encoding/pem`, `math/big`, `net`, `os`, `time`
    3. Implement `LoadOrGenerateCert(certFile, keyFile string) (tls.Certificate, error)`:
       - If certFile and keyFile exist: call `tls.LoadX509KeyPair(certFile, keyFile)` and return
       - If not: generate ECDSA P-256 private key with `ecdsa.GenerateKey(elliptic.P256(), rand.Reader)`
       - Create self-signed x509 certificate template: SerialNumber=1, Subject.CommonName="reversproxy", NotBefore=now, NotAfter=now+10years, IPAddresses=[net.ParseIP("127.0.0.1")], DNSNames=["localhost"]
       - Call `x509.CreateCertificate` and write PEM files to certFile and keyFile
       - Return `tls.X509KeyPair(certPEM, keyPEM)`
    4. Implement `NewServerTLSConfig(cert tls.Certificate) *tls.Config`:
       - Return `&tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13}`
    5. Implement `NewClientTLSConfig(insecureSkipVerify bool) *tls.Config`:
       - Return `&tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: insecureSkipVerify}`
  </action>
  <verify>grep -n "func LoadOrGenerateCert\|func NewServerTLSConfig\|func NewClientTLSConfig" internal/control/tls.go</verify>
  <acceptance_criteria>
    - `grep "func LoadOrGenerateCert" internal/control/tls.go` returns match
    - `grep "func NewServerTLSConfig" internal/control/tls.go` returns match
    - `grep "func NewClientTLSConfig" internal/control/tls.go` returns match
    - `grep "tls.VersionTLS13" internal/control/tls.go` returns match
    - `go build ./internal/control/...` succeeds
  </acceptance_criteria>
  <ref>H5</ref>
  <done>false</done>
</task>

<task id="1-6" wave="2" depends_on="1-1,1-3">
  <name>Implement client registry with RWMutex protection</name>
  <files>internal/control/registry.go, internal/control/registry_test.go</files>
  <read_first>internal/logger/logger.go, /home/starlyn/reversproxy/internal/control/</read_first>
  <action>
    1. Create internal/control/registry.go with `package control`
    2. Import `context`, `net`, `sync`, `time`, `github.com/google/uuid`
    3. Define `Client` struct: fields `ID string`, `Name string`, `Addr string`, `RegisteredAt time.Time`, `LastHeartbeatAt time.Time`, `Conn net.Conn`, `cancelFn context.CancelFunc`
    4. Define `ClientRegistry` struct: fields `mu sync.RWMutex`, `clients map[string]*Client`
    5. `NewClientRegistry() *ClientRegistry`: return `&ClientRegistry{clients: make(map[string]*Client)}`
    6. `Register(name, addr string, conn net.Conn, cancelFn context.CancelFunc) *Client`: lock, `uuid.New().String()` as ID, build Client, store, unlock, return pointer
    7. `Deregister(id string)`: lock, delete, unlock
    8. `Get(id string) (*Client, bool)`: RLock, lookup, RUnlock, return
    9. `List() []*Client`: RLock, collect map values into slice, RUnlock, return
    10. Create registry_test.go: test unique UUID on Register, different IDs for concurrent calls, Deregister removes from List, Get returns false for missing ID
  </action>
  <verify>go test -race ./internal/control/ -run TestRegistry -v</verify>
  <acceptance_criteria>
    - `grep "func NewClientRegistry" internal/control/registry.go` returns match
    - `grep "func.*Register" internal/control/registry.go` returns match
    - `grep "sync.RWMutex" internal/control/registry.go` returns match
    - `grep "uuid.New" internal/control/registry.go` returns match
    - `go test -race ./internal/control/ -run TestRegistry` passes
  </acceptance_criteria>
  <ref>H2, M2, M4</ref>
  <done>false</done>
</task>

<task id="1-7" wave="3" depends_on="1-4,1-5,1-6">
  <name>Implement per-connection control handler with registration handshake</name>
  <files>internal/control/handler.go</files>
  <read_first>internal/protocol/messages.go, internal/protocol/framing.go, internal/control/registry.go, internal/logger/logger.go</read_first>
  <action>
    1. Create internal/control/handler.go with `package control`
    2. Import `context`, `encoding/gob`, `net`, `time`, `log/slog`; import protocol and logger packages
    3. Implement `HandleControlConn(ctx context.Context, conn net.Conn, reg *ClientRegistry, authToken string, log *slog.Logger)`:
       a. Defer `conn.Close()`
       b. Set TCP keepalive: cast conn to `*net.TCPConn`, call `SetKeepAlive(true)` and `SetKeepAlivePeriod(15 * time.Second)`
       c. **Registration phase:**
          - Call `protocol.ReadMessage(conn)` with 10s deadline (`conn.SetDeadline(time.Now().Add(10 * time.Second))`)
          - Decode payload into `protocol.ClientRegister` using `gob.NewDecoder(bytes.NewReader(env.Payload)).Decode`
          - If env.Type != MsgClientRegister: write RegisterResp{Status:"error", Error:"expected ClientRegister"} and return
          - If msg.AuthToken != authToken: write RegisterResp{Status:"error", Error:"invalid token"} and return
          - Create child context: `clientCtx, cancel := context.WithCancel(ctx)`
          - Register: `client := reg.Register(msg.Name, conn.RemoteAddr().String(), conn, cancel)`
          - Write RegisterResp{AssignedClientID: client.ID, Status: "ok"} to conn
          - Log: `log.Info("client registered", "id", client.ID, "name", client.Name, "addr", client.Addr)`
          - Reset deadline: `conn.SetDeadline(time.Time{})`
       d. Defer `reg.Deregister(client.ID)` and `cancel()`
       e. Start heartbeat: `go StartHeartbeat(clientCtx, client, log)`
       f. **Message loop:** read messages in a for loop until error or ctx.Done():
          - On MsgPong: update `client.LastHeartbeatAt = time.Now()`
          - On MsgDisconnect: decode Disconnect, log reason, write DisconnectAck, return
          - On io.EOF or other error: log `"client disconnected"` with id and error, return
  </action>
  <verify>grep -n "func HandleControlConn\|client registered\|client disconnected" internal/control/handler.go</verify>
  <acceptance_criteria>
    - `grep "func HandleControlConn" internal/control/handler.go` returns match
    - `grep "client registered" internal/control/handler.go` returns match
    - `grep "client disconnected" internal/control/handler.go` returns match
    - `grep "SetKeepAlive" internal/control/handler.go` returns match
    - `grep "StartHeartbeat" internal/control/handler.go` returns match
    - `go build ./internal/control/...` succeeds
  </acceptance_criteria>
  <ref>H1, H2, H3, M3, M4</ref>
  <done>false</done>
</task>

<task id="1-8" wave="3" depends_on="1-4,1-6">
  <name>Implement application-level heartbeat with Ping/Pong and timeout</name>
  <files>internal/control/heartbeat.go</files>
  <read_first>internal/protocol/messages.go, internal/protocol/framing.go, internal/control/registry.go</read_first>
  <action>
    1. Create internal/control/heartbeat.go with `package control`
    2. Import `context`, `log/slog`, `time`; import protocol package
    3. Define constants:
       - `pingInterval = 30 * time.Second`
       - `pongTimeout  = 10 * time.Second`
       - `maxMissed    = 2`
    4. Implement `StartHeartbeat(ctx context.Context, client *Client, log *slog.Logger)`:
       - Create `ticker := time.NewTicker(pingInterval)`, defer `ticker.Stop()`
       - Track `missed int`
       - In a for-select loop:
         - `case <-ctx.Done(): return`
         - `case <-ticker.C`:
           - If `time.Since(client.LastHeartbeatAt) > pingInterval*time.Duration(maxMissed+1)` AND missed >= maxMissed:
             - Log `"heartbeat timeout"` with `"id"`, `client.ID`
             - Call `client.cancelFn()` and return
           - Write Ping{Seq: uint64(missed)} to `client.Conn` using `protocol.WriteMessage`
           - If WriteMessage returns error: log `"ping write failed"`, call `client.cancelFn()`, return
           - Increment missed on each tick; disconnect trigger: `time.Since(client.LastHeartbeatAt) > pingInterval*time.Duration(maxMissed)`
           - handler.go resets `client.LastHeartbeatAt` on Pong receipt
  </action>
  <verify>grep -n "func StartHeartbeat\|heartbeat timeout\|pingInterval\|maxMissed" internal/control/heartbeat.go</verify>
  <acceptance_criteria>
    - `grep "func StartHeartbeat" internal/control/heartbeat.go` returns match
    - `grep "heartbeat timeout" internal/control/heartbeat.go` returns match
    - `grep "pingInterval" internal/control/heartbeat.go` returns match
    - `grep "cancelFn" internal/control/heartbeat.go` returns match
    - `go build ./internal/control/...` succeeds
  </acceptance_criteria>
  <ref>H3, L3</ref>
  <done>false</done>
</task>

<task id="1-9" wave="4" depends_on="1-7,1-8">
  <name>Implement server main with accept loop and graceful shutdown</name>
  <files>cmd/server/main.go</files>
  <read_first>internal/control/handler.go, internal/control/registry.go, internal/control/tls.go, internal/logger/logger.go</read_first>
  <action>
    1. Create cmd/server/main.go with `package main`
    2. Parse flags with `flag` package:
       - `-addr` string (default ":8443") — listen address
       - `-token` string (default "changeme") — pre-shared auth token
       - `-cert` string (default "server.crt") — TLS cert file path
       - `-key` string (default "server.key") — TLS key file path
    3. Initialize logger: `log := logger.New("server")`
    4. Load or generate TLS cert: `cert, err := control.LoadOrGenerateCert(*certFile, *keyFile)`; if err log.Error and os.Exit(1)
    5. Create TLS config: `tlsCfg := control.NewServerTLSConfig(cert)`
    6. Listen: `ln, err := tls.Listen("tcp", *addr, tlsCfg)`; if err log.Error and os.Exit(1)
    7. Log: `log.Info("server listening", "addr", *addr)`
    8. Create registry: `reg := control.NewClientRegistry()`
    9. Create root context with cancel: `ctx, cancel := context.WithCancel(context.Background())`
    10. Handle SIGTERM/SIGINT with `signal.NotifyContext` or `signal.Notify`:
        - On signal: log `"shutting down"`, call cancel(), close listener
        - Broadcast Disconnect{Reason:"server shutdown"} to all clients in reg.List()
        - Wait for goroutines via sync.WaitGroup or 3s timeout, then os.Exit(0)
    11. Accept loop: `var wg sync.WaitGroup`, loop `ln.Accept()`, on success `wg.Add(1)` then `go func(c){ defer wg.Done(); control.HandleControlConn(ctx, c, reg, *token, log) }(conn)`, break on error; call `wg.Wait()` after loop.
  </action>
  <verify>go build ./cmd/server/ && echo "build ok"</verify>
  <acceptance_criteria>
    - `go build ./cmd/server/` succeeds
    - `grep "tls.Listen" cmd/server/main.go` returns match
    - `grep "HandleControlConn" cmd/server/main.go` returns match
    - `grep "NewClientRegistry" cmd/server/main.go` returns match
    - `grep "SIGTERM\|signal.Notify" cmd/server/main.go` returns match
  </acceptance_criteria>
  <ref>H4, H5, M3, M4</ref>
  <done>false</done>
</task>

<task id="1-10" wave="4" depends_on="1-7,1-8">
  <name>Implement client main with TLS connect and message loop</name>
  <files>cmd/client/main.go</files>
  <read_first>internal/protocol/messages.go, internal/protocol/framing.go, internal/control/tls.go, internal/logger/logger.go</read_first>
  <action>
    1. Create cmd/client/main.go with `package main`
    2. Parse flags:
       - `-server` string (default "localhost:8443") — server address
       - `-token` string (default "changeme") — pre-shared auth token
       - `-name` string (default "client1") — client label
       - `-insecure` bool (default true) — skip TLS verification (dev only)
    3. Initialize logger: `log := logger.New("client")`
    4. Create TLS config: `tlsCfg := control.NewClientTLSConfig(*insecure)`
    5. Dial: `conn, err := tls.Dial("tcp", *server, tlsCfg)`; if err log.Error and os.Exit(1)
    6. Defer `conn.Close()`
    7. **Registration handshake:**
       - Write ClientRegister{AuthToken: *token, Name: *name, Version: "0.1.0"} using protocol.WriteMessage
       - Read response with protocol.ReadMessage, decode RegisterResp from payload
       - If resp.Status != "ok": log.Error("registration failed", "error", resp.Error) and os.Exit(1)
       - Log: `log.Info("registered", "client_id", resp.AssignedClientID)`
    8. Handle SIGTERM/SIGINT:
       - On signal: write Disconnect{Reason:"client shutdown"}, read DisconnectAck (2s timeout), conn.Close(), os.Exit(0)
    9. **Message loop:** read messages in a for loop:
       - On MsgPing: decode Ping, write Pong{Seq: ping.Seq}; log `"pong sent"`
       - On MsgDisconnect: log reason, os.Exit(0)
       - On error: log `"connection lost"` with error, os.Exit(1)
  </action>
  <verify>go build ./cmd/client/ && echo "build ok"</verify>
  <acceptance_criteria>
    - `go build ./cmd/client/` succeeds
    - `grep "tls.Dial" cmd/client/main.go` returns match
    - `grep "ClientRegister" cmd/client/main.go` returns match
    - `grep "registered" cmd/client/main.go` returns match
    - `grep "MsgPing\|MsgDisconnect" cmd/client/main.go` returns match
  </acceptance_criteria>
  <ref>H2, H3, H5, M3</ref>
  <done>false</done>
</task>

<task id="1-11" wave="4" depends_on="1-6,1-7,1-8">
  <name>Write integration tests verifying all three success criteria</name>
  <files>internal/control/integration_test.go</files>
  <read_first>internal/control/handler.go, internal/control/registry.go, internal/control/heartbeat.go, internal/control/tls.go, internal/protocol/framing.go</read_first>
  <action>
    1. Create internal/control/integration_test.go with `package control_test`
    2. Helper `startTestServer(t *testing.T, token string) (addr string, reg *ClientRegistry, shutdown func())`:
       - LoadOrGenerateCert with t.TempDir() paths; tls.Listen "127.0.0.1:0"; create registry + context; start accept-loop goroutine; return addr, reg, cancel+Close as shutdown
    3. Helper `connectTestClient(t, addr, token, name string) net.Conn`: Dial InsecureSkipVerify, WriteMessage ClientRegister, ReadMessage RegisterResp, return conn
    4. `func TestSC1_ClientRegistration`: start server, connect "test-client", sleep 100ms, assert `len(reg.List())==1` and `reg.List()[0].Name=="test-client"`
    5. `func TestSC2_MultipleClients`: connect "c1" and "c2" concurrently, sleep 200ms, assert `len==2` and `clients[0].ID != clients[1].ID`
    6. `func TestSC3_DisconnectionDetected`: connect client, record ID, call `conn.Close()`, poll `reg.Get(id)` every 50ms up to 500ms, fail if still present
  </action>
  <verify>go test -race ./internal/control/ -run TestSC -v -timeout 30s</verify>
  <acceptance_criteria>
    - `grep "func TestSC1_ClientRegistration" internal/control/integration_test.go` returns match
    - `grep "func TestSC2_MultipleClients" internal/control/integration_test.go` returns match
    - `grep "func TestSC3_DisconnectionDetected" internal/control/integration_test.go` returns match
    - `go test -race ./internal/control/ -run TestSC -timeout 30s` passes
    - `go test -race ./...` passes
  </acceptance_criteria>
  <ref>H1, H2, H3, H4, M2, M4</ref>
  <done>false</done>
</task>

## Design Patterns
- Goroutine-per-Connection: `HandleControlConn`을 goroutine으로 실행하여 accept loop를 block하지 않음. Go의 M:N 스케줄러가 경량 처리 보장 [REF:M4]
- Registry Pattern: `ClientRegistry`가 `sync.RWMutex`로 보호된 map을 캡슐화. 쓰기 경합 없이 다중 goroutine이 동시 조회 가능 [REF:H2, M2]
- Context Cancellation Tree: 서버 root context → 클라이언트 child context. 서버 shutdown 시 모든 클라이언트 goroutine이 연쇄 종료 [REF:M3]
- Layered Liveness Detection: TCP keepalive(OS 레벨) + App Ping/Pong(앱 레벨) 이중 구조로 NAT 세션 만료와 앱 hang을 각각 감지 [REF:H3, L3]
- Length-Prefix Framing: 4-byte big-endian prefix로 TCP 스트림에서 메시지 경계 명확화. 1MB 상한으로 DoS 방지 [REF:H1]

## Risk & Mitigation
| Risk | Impact | Mitigation |
|---|---|---|
| self-signed cert 생성 실패 (권한 없음) | H | LoadOrGenerateCert가 에러 반환 시 서버가 os.Exit(1); 명시적 에러 메시지로 원인 안내 |
| auth_token 평문 비교 timing attack | M | Phase 1에서는 허용; Phase 2에서 `crypto/subtle.ConstantTimeCompare`로 교체 |
| goroutine leak (Deregister 후 heartbeat goroutine 잔존) | M | context.WithCancel + cancelFn으로 heartbeat goroutine이 ctx.Done() 수신 즉시 종료 |
| gob encoding 타입 불일치 (미등록 타입) | M | Envelope에 타입 코드(MsgType)를 포함하여 수신 측이 올바른 struct로 디코딩; integration test에서 roundtrip 검증 |
| 다중 클라이언트 동시 registry 접근 race | H | sync.RWMutex 적용; `go test -race` 필수 실행으로 조기 발견 |

---
## Plan Check Results

### Verdict: PASS
### Score: 8/8

| Dimension | Result | Notes |
|---|---|---|
| D1: Completeness | PASS | H1→tasks 1-2,1-4,1-7,1-11; H2→tasks 1-2,1-3,1-6,1-7,1-10,1-11; H3→tasks 1-7,1-8,1-11; H4→task 1-9; H5→tasks 1-5,1-9,1-10. All 5 HIGH findings are referenced. |
| D2: Specificity | PASS | No vague phrases ("align", "as needed", "ensure proper", "handle appropriately", "fix as necessary", "similar to") found. All actions are concrete and step-numbered. |
| D3: Verifiability | PASS | All 11 tasks have acceptance_criteria with grep/go build/go test commands. Every criterion is machine-executable. |
| D4: DAG Correctness | PASS | Wave 1: {1-1,1-2,1-3} no deps. Wave 2: {1-4,1-5,1-6} depend on wave-1 IDs only. Wave 3: {1-7,1-8} depend on wave-2 IDs only. Wave 4: {1-9,1-10,1-11} depend on wave-3 IDs only. No cycles. All referenced IDs exist. |
| D5: must_haves Completeness | PASS | 6 observable truths with specific log strings and commands; 15 artifacts all have path+min_lines (exports=[] only for files with no exported symbols); 8 key_links all have valid grep-able patterns. |
| D6: read_first Validity | WARN | Minor inconsistency: wave-1 tasks use absolute paths (e.g. `/home/starlyn/reversproxy/internal/protocol/`) while wave-2+ tasks use relative paths (e.g. `internal/protocol/messages.go`). All paths refer to files created in earlier waves or the existing project root. Not a blocking issue. |
| D7: TDD Compliance | PASS | Paradigm is mixed. Unit tests co-created with implementation: framing_test.go in task 1-4 (wave 2), registry_test.go in task 1-6 (wave 2). Integration tests in task 1-11 (wave 4) cover all 3 success criteria (SC1/SC2/SC3). |
| D8: Scale Compliance | PASS | medium scale: 11 tasks (bounds: 6-15 ✓), 1 phase (bounds: 1-2 ✓), 4 waves (max: 5 ✓). |

### Issues to Fix (if NEEDS_REVISION)
None — verdict is PASS. The single WARN on D6 (mixed absolute/relative read_first paths) is cosmetic and does not affect execution correctness.

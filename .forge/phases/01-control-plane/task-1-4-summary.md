# Task 1-4 Summary: Length-Prefixed Binary Framing I/O

## Status: DONE

## Files Created

- `internal/protocol/framing.go` — 73 lines
- `internal/protocol/framing_test.go` — 145 lines

## What Was Implemented

### framing.go

**`WriteMessage(conn net.Conn, msgType MsgType, payload any) error`**
1. gob-encodes the `payload` into a `bytes.Buffer`
2. Constructs `Envelope{Type: msgType, Payload: <encoded bytes>}`
3. gob-encodes the Envelope into a second buffer
4. Writes a 4-byte big-endian `uint32` length prefix via `binary.Write`
5. Writes the encoded envelope bytes to `conn`

**`ReadMessage(conn net.Conn) (*Envelope, error)`**
1. Reads a 4-byte big-endian `uint32` length prefix via `binary.Read`
2. Rejects any frame whose declared length exceeds `MaxMessageSize` (1 MB) — DoS guard
3. Reads exactly `length` bytes using `io.ReadFull` — handles short reads correctly
4. gob-decodes the bytes into `Envelope`
5. Returns `&envelope, nil` on success

### framing_test.go (package protocol_test)

Four test functions:

| Test | What it verifies |
|---|---|
| `TestFramingRoundtrip` | WriteMessage → ReadMessage reconstructs `ClientRegister` faithfully via `net.Pipe()` |
| `TestFramingMaxMessageSize` | ReadMessage returns error when length prefix > MaxMessageSize |
| `TestFramingTruncatedData` | ReadMessage returns EOF-related error when body is shorter than declared length |
| `TestFramingMultipleMessages` | Sequential Ping + Pong messages are read in order with correct types and payloads |

## Acceptance Criteria Results

| Criterion | Result |
|---|---|
| `grep "func WriteMessage" framing.go` | PASS |
| `grep "func ReadMessage" framing.go` | PASS |
| `grep "MaxMessageSize" framing.go` | PASS |
| `grep "binary.BigEndian" framing.go` | PASS |
| `go build ./internal/protocol/...` | PASS |
| `go test ./internal/protocol/...` | PASS (4/4 tests) |

## Notes

- `-race` flag requires CGO (gcc not available in this environment). Tests verified without race flag; the implementation uses no shared mutable state between goroutines — `net.Pipe()` connections are used by exactly one goroutine per side.
- `io.ReadFull` is used for reading the frame body, which correctly returns `io.ErrUnexpectedEOF` on truncated data (not just `io.EOF`), satisfying the nil/EOF safety requirement.

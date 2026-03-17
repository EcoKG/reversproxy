#!/usr/bin/env python3
# ============================================================
# ProxyServer - 192.168.1.221 (폐쇄망) 에서 실행
# ============================================================
# 실행: python3 server.py [터널포트] [프록시포트]
#       기본값: python3 server.py 9000 8080
# ============================================================
import socket
import struct
import threading
import sys
import time

# Frame types
OPEN = 1
DATA = 2
CLOSE = 3
ACK = 4
NACK = 5

tunnel_sock = None
tunnel_lock = threading.Lock()
channel_seq = 0
channel_seq_lock = threading.Lock()

# channelId -> ChannelState
channels = {}
channels_lock = threading.Lock()


class ChannelState:
    def __init__(self, client_sock):
        self.client = client_sock
        self.established = False
        self.failed = False
        self.ack_event = threading.Event()


def send_frame(frame_type, channel_id, data=b''):
    sock = tunnel_sock
    if sock is None:
        return
    header = struct.pack('!BII', frame_type, channel_id, len(data))
    try:
        with tunnel_lock:
            sock.sendall(header + data)
    except Exception:
        pass


def recv_exact(sock, n):
    buf = b''
    while len(buf) < n:
        chunk = sock.recv(n - len(buf))
        if not chunk:
            return None
        buf += chunk
    return buf


def close_channel(channel_id):
    with channels_lock:
        ch = channels.pop(channel_id, None)
    if ch:
        try:
            ch.client.close()
        except Exception:
            pass


# ==================== Tunnel ====================

def tunnel_listener(tunnel_port):
    global tunnel_sock
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(('0.0.0.0', tunnel_port))
    srv.listen(1)

    while True:
        client, addr = srv.accept()
        print(f'[tunnel] 224 connected from {addr}')

        old = tunnel_sock
        if old:
            try:
                old.close()
            except Exception:
                pass

        client.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        client.setsockopt(socket.SOL_SOCKET, socket.SO_KEEPALIVE, 1)
        tunnel_sock = client

        t = threading.Thread(target=read_tunnel_frames, args=(client,), daemon=True)
        t.start()


def read_tunnel_frames(sock):
    global tunnel_sock
    try:
        while True:
            header = recv_exact(sock, 9)
            if header is None:
                break

            frame_type, channel_id, length = struct.unpack('!BII', header)

            data = b''
            if length > 0:
                data = recv_exact(sock, length)
                if data is None:
                    break

            with channels_lock:
                ch = channels.get(channel_id)

            if frame_type == ACK:
                if ch:
                    ch.established = True
                    ch.ack_event.set()

            elif frame_type == NACK:
                if ch:
                    ch.failed = True
                    ch.ack_event.set()

            elif frame_type == DATA:
                if ch:
                    try:
                        ch.client.sendall(data)
                    except Exception:
                        close_channel(channel_id)

            elif frame_type == CLOSE:
                close_channel(channel_id)

    except Exception as e:
        print(f'[tunnel] Read error: {e}')

    print('[tunnel] 224 disconnected')
    tunnel_sock = None

    with channels_lock:
        for cid, ch in list(channels.items()):
            ch.ack_event.set()
            try:
                ch.client.close()
            except Exception:
                pass
        channels.clear()


# ==================== HTTP CONNECT Proxy ====================

def proxy_listener(proxy_port):
    srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    srv.bind(('127.0.0.1', proxy_port))
    srv.listen(50)

    while True:
        client, addr = srv.accept()
        t = threading.Thread(target=handle_proxy_client, args=(client, proxy_port), daemon=True)
        t.start()


def read_line(sock):
    line = b''
    while True:
        b = sock.recv(1)
        if not b:
            return None
        if b == b'\r':
            continue
        if b == b'\n':
            return line.decode('utf-8', errors='replace')
        line += b


def send_http_error(sock, status):
    body = (status + '\r\n').encode()
    resp = f'HTTP/1.1 {status}\r\nContent-Type: text/plain\r\nContent-Length: {len(body)}\r\n\r\n'.encode()
    try:
        sock.sendall(resp + body)
    except Exception:
        pass


def handle_proxy_client(client, proxy_port):
    global channel_seq
    try:
        client.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)

        request_line = read_line(client)
        if not request_line:
            client.close()
            return

        # Consume headers
        while True:
            line = read_line(client)
            if line is None or line == '':
                break

        parts = request_line.split(' ')
        if len(parts) < 2:
            send_http_error(client, '400 Bad Request')
            client.close()
            return

        method = parts[0].upper()
        target = parts[1]

        if method == 'CONNECT':
            handle_connect(client, target)
        else:
            body = f'CloseProxy - HTTPS CONNECT Proxy\r\nSet HTTPS_PROXY=http://127.0.0.1:{proxy_port} to use.\r\n'.encode()
            resp = f'HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: {len(body)}\r\n\r\n'.encode()
            client.sendall(resp + body)
            client.close()

    except Exception as e:
        print(f'[proxy] Error: {e}')
        try:
            client.close()
        except Exception:
            pass


def handle_connect(client, target):
    global channel_seq

    if tunnel_sock is None:
        send_http_error(client, '502 Tunnel Not Connected')
        client.close()
        return

    with channel_seq_lock:
        channel_seq += 1
        channel_id = channel_seq

    print(f'[proxy] CONNECT {target} -> ch:{channel_id}')

    state = ChannelState(client)
    with channels_lock:
        channels[channel_id] = state

    # Send OPEN to 224
    send_frame(OPEN, channel_id, target.encode())

    # Wait for ACK/NACK (30s timeout)
    if not state.ack_event.wait(30) or state.failed:
        status = 'rejected' if state.failed else 'timed out'
        print(f'[proxy] ch:{channel_id} connection {status}')
        send_http_error(client, '502 Target Unreachable')
        send_frame(CLOSE, channel_id)
        close_channel(channel_id)
        return

    # Connection established
    try:
        client.sendall(b'HTTP/1.1 200 Connection Established\r\n\r\n')
    except Exception:
        send_frame(CLOSE, channel_id)
        close_channel(channel_id)
        return

    print(f'[proxy] ch:{channel_id} established')

    # Forward client -> tunnel
    try:
        while True:
            data = client.recv(32768)
            if not data:
                break
            send_frame(DATA, channel_id, data)
    except Exception:
        pass

    send_frame(CLOSE, channel_id)
    close_channel(channel_id)
    print(f'[proxy] ch:{channel_id} closed')


# ==================== Main ====================

def main():
    tunnel_port = int(sys.argv[1]) if len(sys.argv) >= 2 else 9000
    proxy_port = int(sys.argv[2]) if len(sys.argv) >= 3 else 8080

    t1 = threading.Thread(target=tunnel_listener, args=(tunnel_port,), daemon=True)
    t1.start()

    t2 = threading.Thread(target=proxy_listener, args=(proxy_port,), daemon=True)
    t2.start()

    print('========================================')
    print(' CloseProxy Server (221 - Air-gapped)')
    print('========================================')
    print(f'[tunnel]  0.0.0.0:{tunnel_port} (224 connects here)')
    print(f'[proxy]   127.0.0.1:{proxy_port} (Claude Code connects here)')
    print()
    print('Usage:')
    print(f'  export HTTPS_PROXY=http://127.0.0.1:{proxy_port}')
    print(f'  claude')
    print()
    print('Press Ctrl+C to stop.')
    print('========================================')

    try:
        while True:
            time.sleep(1)
    except KeyboardInterrupt:
        print('\nStopped.')


if __name__ == '__main__':
    main()

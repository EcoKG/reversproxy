// ============================================================
// ProxyServer - 192.168.1.221 (폐쇄망) 에서 실행
// ============================================================
// 컴파일: C:\Windows\Microsoft.NET\Framework64\v4.0.30319\csc.exe /out:server.exe server.cs
// 실행:   server.exe [터널포트] [프록시포트]
//         기본값: server.exe 9000 8080
// ============================================================
using System;
using System.Collections.Concurrent;
using System.IO;
using System.Net;
using System.Net.Sockets;
using System.Text;
using System.Threading;

class ProxyServer
{
    // Frame types
    const byte OPEN = 1;
    const byte DATA = 2;
    const byte CLOSE = 3;
    const byte ACK = 4;   // from 224: target connection established
    const byte NACK = 5;  // from 224: target connection failed

    static volatile TcpClient tunnelClient;
    static volatile NetworkStream tunnelStream;
    static readonly object writeLock = new object();
    static int channelSeq = 0;

    // Channel state
    class ChannelState
    {
        public TcpClient Client;
        public NetworkStream Stream;
        public volatile bool Established; // ACK received from 224
        public ManualResetEventSlim AckEvent = new ManualResetEventSlim(false);
        public volatile bool Failed;
    }

    static readonly ConcurrentDictionary<int, ChannelState> channels =
        new ConcurrentDictionary<int, ChannelState>();

    static int tunnelPort = 9000;
    static int proxyPort = 8080;

    static void Main(string[] args)
    {
        if (args.Length >= 1) tunnelPort = int.Parse(args[0]);
        if (args.Length >= 2) proxyPort = int.Parse(args[1]);

        // Start tunnel listener (224 connects here)
        var tunnelThread = new Thread(TunnelListener) { IsBackground = true };
        tunnelThread.Start();

        // Start CONNECT proxy (Claude Code connects here)
        var proxyThread = new Thread(ConnectProxyListener) { IsBackground = true };
        proxyThread.Start();

        Console.WriteLine("========================================");
        Console.WriteLine(" CloseProxy Server (221 - Air-gapped)");
        Console.WriteLine("========================================");
        Console.WriteLine("[tunnel]  0.0.0.0:" + tunnelPort + " (224 connects here)");
        Console.WriteLine("[proxy]   127.0.0.1:" + proxyPort + " (Claude Code connects here)");
        Console.WriteLine();
        Console.WriteLine("Usage:");
        Console.WriteLine("  set HTTPS_PROXY=http://127.0.0.1:" + proxyPort);
        Console.WriteLine("  claude");
        Console.WriteLine();
        Console.WriteLine("Press Ctrl+C to stop.");
        Console.WriteLine("========================================");

        Thread.Sleep(Timeout.Infinite);
    }

    // ==================== Frame I/O ====================

    static void SendFrame(byte type, int channelId, byte[] data, int offset, int length)
    {
        var ns = tunnelStream;
        if (ns == null) return;

        // Build single frame buffer to prevent partial write corruption
        var frame = new byte[9 + length];
        frame[0] = type;
        frame[1] = (byte)(channelId >> 24);
        frame[2] = (byte)(channelId >> 16);
        frame[3] = (byte)(channelId >> 8);
        frame[4] = (byte)channelId;
        frame[5] = (byte)(length >> 24);
        frame[6] = (byte)(length >> 16);
        frame[7] = (byte)(length >> 8);
        frame[8] = (byte)length;
        if (length > 0) Array.Copy(data, offset, frame, 9, length);

        try
        {
            lock (writeLock)
            {
                ns.Write(frame, 0, frame.Length);
                ns.Flush();
            }
        }
        catch (Exception) { }
    }

    static void SendFrame(byte type, int channelId, byte[] data)
    {
        SendFrame(type, channelId, data, 0, data != null ? data.Length : 0);
    }

    static bool ReadExact(NetworkStream ns, byte[] buf, int offset, int count)
    {
        int read = 0;
        while (read < count)
        {
            int n;
            try { n = ns.Read(buf, offset + read, count - read); }
            catch { return false; }
            if (n <= 0) return false;
            read += n;
        }
        return true;
    }

    // ==================== Tunnel ====================

    static void TunnelListener()
    {
        var listener = new TcpListener(IPAddress.Any, tunnelPort);
        listener.Start();

        while (true)
        {
            var client = listener.AcceptTcpClient();
            Console.WriteLine("[tunnel] 224 connected from " + client.Client.RemoteEndPoint);

            var old = tunnelClient;
            if (old != null) try { old.Close(); } catch { }

            client.NoDelay = true;
            client.Client.SetSocketOption(SocketOptionLevel.Socket, SocketOptionName.KeepAlive, true);
            tunnelClient = client;
            tunnelStream = client.GetStream();

            var thread = new Thread(() => ReadTunnelFrames(client)) { IsBackground = true };
            thread.Start();
        }
    }

    static void ReadTunnelFrames(TcpClient client)
    {
        var ns = client.GetStream();
        var headerBuf = new byte[9];

        try
        {
            while (client.Connected)
            {
                if (!ReadExact(ns, headerBuf, 0, 9)) break;

                byte type = headerBuf[0];
                int channelId = (headerBuf[1] << 24) | (headerBuf[2] << 16) |
                                (headerBuf[3] << 8) | headerBuf[4];
                int length = (headerBuf[5] << 24) | (headerBuf[6] << 16) |
                             (headerBuf[7] << 8) | headerBuf[8];

                byte[] data = new byte[length];
                if (length > 0 && !ReadExact(ns, data, 0, length)) break;

                ChannelState ch;

                if (type == ACK)
                {
                    if (channels.TryGetValue(channelId, out ch))
                    {
                        ch.Established = true;
                        ch.AckEvent.Set();
                    }
                }
                else if (type == NACK)
                {
                    if (channels.TryGetValue(channelId, out ch))
                    {
                        ch.Failed = true;
                        ch.AckEvent.Set();
                    }
                }
                else if (type == DATA)
                {
                    if (channels.TryGetValue(channelId, out ch))
                    {
                        try
                        {
                            ch.Stream.Write(data, 0, data.Length);
                            ch.Stream.Flush();
                        }
                        catch { CloseChannel(channelId); }
                    }
                }
                else if (type == CLOSE)
                {
                    CloseChannel(channelId);
                }
            }
        }
        catch (Exception ex)
        {
            Console.WriteLine("[tunnel] Read error: " + ex.Message);
        }

        Console.WriteLine("[tunnel] 224 disconnected");
        tunnelStream = null;
        tunnelClient = null;

        foreach (var kv in channels)
        {
            kv.Value.AckEvent.Set(); // unblock waiting threads
            try { kv.Value.Client.Close(); } catch { }
        }
        channels.Clear();
    }

    static void CloseChannel(int channelId)
    {
        ChannelState ch;
        if (channels.TryRemove(channelId, out ch))
        {
            try { ch.Client.Close(); } catch { }
        }
    }

    // ==================== HTTP CONNECT Proxy ====================

    static void ConnectProxyListener()
    {
        var listener = new TcpListener(IPAddress.Loopback, proxyPort);
        listener.Start();

        while (true)
        {
            var client = listener.AcceptTcpClient();
            var thread = new Thread(() => HandleProxyClient(client)) { IsBackground = true };
            thread.Start();
        }
    }

    static void HandleProxyClient(TcpClient client)
    {
        NetworkStream ns = null;
        try
        {
            client.NoDelay = true;
            ns = client.GetStream();

            string requestLine = ReadLine(ns);
            if (requestLine == null) { client.Close(); return; }

            // Consume headers until empty line
            while (true)
            {
                string line = ReadLine(ns);
                if (line == null || line == "") break;
            }

            string[] parts = requestLine.Split(' ');
            if (parts.Length < 2)
            {
                SendHttpError(ns, "400 Bad Request");
                client.Close();
                return;
            }

            string method = parts[0].ToUpper();
            string target = parts[1];

            if (method == "CONNECT")
            {
                HandleConnect(client, ns, target);
            }
            else
            {
                string body = "CloseProxy - HTTPS CONNECT Proxy\r\n"
                    + "Set HTTPS_PROXY=http://127.0.0.1:" + proxyPort + " to use.\r\n";
                byte[] bodyBytes = Encoding.UTF8.GetBytes(body);
                string response = "HTTP/1.1 200 OK\r\n"
                    + "Content-Type: text/plain\r\n"
                    + "Content-Length: " + bodyBytes.Length + "\r\n"
                    + "\r\n";
                byte[] respBytes = Encoding.UTF8.GetBytes(response);
                ns.Write(respBytes, 0, respBytes.Length);
                ns.Write(bodyBytes, 0, bodyBytes.Length);
                ns.Flush();
                client.Close();
            }
        }
        catch (Exception ex)
        {
            Console.WriteLine("[proxy] Error: " + ex.Message);
            try { client.Close(); } catch { }
        }
    }

    static void HandleConnect(TcpClient client, NetworkStream clientStream, string target)
    {
        if (tunnelStream == null)
        {
            SendHttpError(clientStream, "502 Tunnel Not Connected");
            client.Close();
            return;
        }

        int channelId = Interlocked.Increment(ref channelSeq);
        Console.WriteLine("[proxy] CONNECT " + target + " -> ch:" + channelId);

        // Create channel state
        var state = new ChannelState { Client = client, Stream = clientStream };
        channels[channelId] = state;

        // Send OPEN to 224 and wait for ACK
        byte[] targetBytes = Encoding.UTF8.GetBytes(target);
        SendFrame(OPEN, channelId, targetBytes);

        // Wait for ACK/NACK from 224 (up to 30 seconds)
        if (!state.AckEvent.Wait(30000) || state.Failed)
        {
            Console.WriteLine("[proxy] ch:" + channelId + " connection " +
                (state.Failed ? "rejected" : "timed out"));
            SendHttpError(clientStream, "502 Target Unreachable");
            SendFrame(CLOSE, channelId, new byte[0]);
            CloseChannel(channelId);
            return;
        }

        // Connection established - respond 200
        byte[] ok = Encoding.UTF8.GetBytes("HTTP/1.1 200 Connection Established\r\n\r\n");
        try
        {
            clientStream.Write(ok, 0, ok.Length);
            clientStream.Flush();
        }
        catch
        {
            SendFrame(CLOSE, channelId, new byte[0]);
            CloseChannel(channelId);
            return;
        }

        Console.WriteLine("[proxy] ch:" + channelId + " established");

        // Forward client -> tunnel
        byte[] buf = new byte[32768];
        try
        {
            while (true)
            {
                int n = clientStream.Read(buf, 0, buf.Length);
                if (n <= 0) break;
                SendFrame(DATA, channelId, buf, 0, n);
            }
        }
        catch { }

        SendFrame(CLOSE, channelId, new byte[0]);
        CloseChannel(channelId);
        Console.WriteLine("[proxy] ch:" + channelId + " closed");
    }

    static string ReadLine(NetworkStream ns)
    {
        var sb = new StringBuilder();
        int b;
        while (true)
        {
            try { b = ns.ReadByte(); }
            catch { return null; }
            if (b < 0) return null;
            if (b == '\r') continue;
            if (b == '\n') return sb.ToString();
            sb.Append((char)b);
        }
    }

    static void SendHttpError(NetworkStream ns, string status)
    {
        string body = status + "\r\n";
        byte[] bodyBytes = Encoding.UTF8.GetBytes(body);
        string response = "HTTP/1.1 " + status + "\r\n"
            + "Content-Type: text/plain\r\n"
            + "Content-Length: " + bodyBytes.Length + "\r\n"
            + "\r\n";
        byte[] respBytes = Encoding.UTF8.GetBytes(response);
        try
        {
            ns.Write(respBytes, 0, respBytes.Length);
            ns.Write(bodyBytes, 0, bodyBytes.Length);
            ns.Flush();
        }
        catch { }
    }
}

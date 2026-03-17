// ============================================================
// ProxyClient - 192.168.4.224 (인터넷 가능) 에서 실행
// ============================================================
// 컴파일: C:\Windows\Microsoft.NET\Framework64\v4.0.30319\csc.exe /out:client.exe client.cs
// 실행:   client.exe [221_IP] [터널포트]
//         기본값: client.exe 192.168.1.221 9000
// ============================================================
using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Net;
using System.Net.Sockets;
using System.Text;
using System.Threading;

class ProxyClient
{
    const byte OPEN = 1;
    const byte DATA = 2;
    const byte CLOSE = 3;
    const byte ACK = 4;   // connection established
    const byte NACK = 5;  // connection failed

    static string serverHost = "192.168.1.221";
    static int serverPort = 9000;

    static volatile TcpClient tunnel;
    static volatile NetworkStream tunnelStream;
    static readonly object writeLock = new object();

    // Channel state to handle buffering before connection is ready
    class ChannelState
    {
        public TcpClient Remote;
        public volatile bool Connected;
        public readonly List<byte[]> PendingData = new List<byte[]>();
        public readonly object Lock = new object();
    }

    static readonly ConcurrentDictionary<int, ChannelState> channels =
        new ConcurrentDictionary<int, ChannelState>();

    static void Main(string[] args)
    {
        if (args.Length >= 1) serverHost = args[0];
        if (args.Length >= 2) serverPort = int.Parse(args[1]);

        Console.WriteLine("========================================");
        Console.WriteLine(" CloseProxy Client (224 - Internet)");
        Console.WriteLine("========================================");
        Console.WriteLine("[target] " + serverHost + ":" + serverPort);
        Console.WriteLine("Press Ctrl+C to stop.");
        Console.WriteLine("========================================");

        while (true)
        {
            Connect();
            Console.WriteLine("[tunnel] Reconnecting in 3 seconds...");
            Thread.Sleep(3000);
        }
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

    static void Connect()
    {
        TcpClient client = null;
        try
        {
            Console.WriteLine("[tunnel] Connecting to " + serverHost + ":" + serverPort + "...");
            client = new TcpClient();
            client.Connect(serverHost, serverPort);
            client.NoDelay = true;
            client.Client.SetSocketOption(SocketOptionLevel.Socket, SocketOptionName.KeepAlive, true);

            tunnel = client;
            tunnelStream = client.GetStream();
            Console.WriteLine("[tunnel] Connected to 221");

            ReadFrames(client);
        }
        catch (Exception ex)
        {
            Console.WriteLine("[tunnel] Connection failed: " + ex.Message);
        }
        finally
        {
            tunnelStream = null;
            tunnel = null;
            if (client != null) try { client.Close(); } catch { }

            foreach (var kv in channels)
            {
                if (kv.Value.Remote != null)
                    try { kv.Value.Remote.Close(); } catch { }
            }
            channels.Clear();
        }
    }

    static void ReadFrames(TcpClient client)
    {
        var ns = client.GetStream();
        var headerBuf = new byte[9];

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

            if (type == OPEN)
            {
                string target = Encoding.UTF8.GetString(data);
                HandleOpen(channelId, target);
            }
            else if (type == DATA)
            {
                HandleData(channelId, data);
            }
            else if (type == CLOSE)
            {
                CloseChannel(channelId);
            }
        }

        Console.WriteLine("[tunnel] Disconnected from 221");
    }

    static void HandleData(int channelId, byte[] data)
    {
        ChannelState ch;
        if (!channels.TryGetValue(channelId, out ch)) return;

        lock (ch.Lock)
        {
            if (!ch.Connected)
            {
                // Buffer data until connection is ready
                byte[] copy = new byte[data.Length];
                Array.Copy(data, copy, data.Length);
                ch.PendingData.Add(copy);
                return;
            }
        }

        // Already connected, forward directly
        try
        {
            ch.Remote.GetStream().Write(data, 0, data.Length);
            ch.Remote.GetStream().Flush();
        }
        catch { CloseChannel(channelId); }
    }

    static void HandleOpen(int channelId, string target)
    {
        int colonIdx = target.LastIndexOf(':');
        if (colonIdx < 0)
        {
            Console.WriteLine("[ch:" + channelId + "] Invalid target: " + target);
            SendFrame(NACK, channelId, new byte[0]);
            return;
        }

        string host = target.Substring(0, colonIdx);
        int port;
        if (!int.TryParse(target.Substring(colonIdx + 1), out port))
        {
            Console.WriteLine("[ch:" + channelId + "] Invalid port: " + target);
            SendFrame(NACK, channelId, new byte[0]);
            return;
        }

        Console.WriteLine("[ch:" + channelId + "] -> " + host + ":" + port);

        // Register channel state before connecting
        var state = new ChannelState();
        channels[channelId] = state;

        var thread = new Thread(() => ConnectToTarget(channelId, state, host, port)) { IsBackground = true };
        thread.Start();
    }

    static void ConnectToTarget(int channelId, ChannelState state, string host, int port)
    {
        TcpClient remote = null;
        try
        {
            remote = new TcpClient();
            remote.Connect(host, port);
            remote.NoDelay = true;
            state.Remote = remote;

            Console.WriteLine("[ch:" + channelId + "] Connected to " + host + ":" + port);

            // Flush pending data and mark as connected
            lock (state.Lock)
            {
                var ns = remote.GetStream();
                foreach (var pending in state.PendingData)
                {
                    ns.Write(pending, 0, pending.Length);
                }
                ns.Flush();
                state.PendingData.Clear();
                state.Connected = true;
            }

            // Send ACK to server
            SendFrame(ACK, channelId, new byte[0]);

            // Read from remote, send to tunnel
            var stream = remote.GetStream();
            byte[] buf = new byte[32768];
            while (true)
            {
                int n = stream.Read(buf, 0, buf.Length);
                if (n <= 0) break;
                SendFrame(DATA, channelId, buf, 0, n);
            }
        }
        catch (Exception ex)
        {
            Console.WriteLine("[ch:" + channelId + "] Error: " + ex.Message);
            if (!state.Connected)
            {
                SendFrame(NACK, channelId, Encoding.UTF8.GetBytes(ex.Message));
            }
        }

        SendFrame(CLOSE, channelId, new byte[0]);
        CloseChannel(channelId);
        Console.WriteLine("[ch:" + channelId + "] Closed");
    }

    static void CloseChannel(int channelId)
    {
        ChannelState ch;
        if (channels.TryRemove(channelId, out ch))
        {
            if (ch.Remote != null)
                try { ch.Remote.Close(); } catch { }
        }
    }
}

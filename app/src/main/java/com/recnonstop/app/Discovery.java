package com.recnonstop.app;

import org.json.JSONObject;

import java.net.DatagramPacket;
import java.net.DatagramSocket;
import java.net.InetAddress;
import java.net.InterfaceAddress;
import java.net.NetworkInterface;
import java.net.SocketTimeoutException;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Enumeration;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Set;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/**
 * UDP 局域网发现。电脑端一直应答，手机扫到就能直接点，
 * 不需要先跳去系统设置里做蓝牙配对。
 */
public final class Discovery {

    public static final int DISCOVERY_PORT = 18083;

    private static final String MAGIC = "RECNONSTOP_DISCOVER_V2";
    private static final ExecutorService EXEC = Executors.newSingleThreadExecutor();

    public static class Device {
        public String kind = "pc"; // pc | phone
        public String id = "";
        public String name = "";
        public String host = "";
        public String fp = "";
        public int port = 18082;

        public boolean isPc() {
            return !"phone".equals(kind);
        }

        public String label() {
            String icon = isPc() ? "💻 " : "📱 ";
            String suffix = fp == null || fp.isEmpty() ? "" : " · " + fp;
            return icon + name + suffix + "\n" + host + ":" + port;
        }
    }

    public interface Callback {
        void onResult(List<Device> devices, String error);
    }

    private Discovery() {
    }

    public static void discover(Callback cb) {
        EXEC.execute(() -> {
            List<Device> out = new ArrayList<>();
            String err = null;
            try (DatagramSocket socket = new DatagramSocket()) {
                socket.setBroadcast(true);
                socket.setSoTimeout(500);

                byte[] probe = MAGIC.getBytes(StandardCharsets.UTF_8);
                for (InetAddress addr : broadcastAddresses()) {
                    try {
                        socket.send(new DatagramPacket(probe, probe.length, addr, DISCOVERY_PORT));
                    } catch (Exception ignored) {
                    }
                }

                long end = System.currentTimeMillis() + 3000;
                while (System.currentTimeMillis() < end) {
                    byte[] buf = new byte[2048];
                    DatagramPacket p = new DatagramPacket(buf, buf.length);
                    try {
                        socket.receive(p);
                    } catch (SocketTimeoutException e) {
                        continue;
                    }
                    try {
                        String s = new String(p.getData(), 0, p.getLength(), StandardCharsets.UTF_8);
                        JSONObject o = new JSONObject(s);
                        if (!"recnonstop".equals(o.optString("service"))) continue;

                        Device d = new Device();
                        d.kind = o.optString("kind", "pc");
                        d.id = o.optString("id", "");
                        d.name = o.optString("name", p.getAddress().getHostAddress());
                        d.fp = o.optString("fp", "");
                        d.host = p.getAddress().getHostAddress();
                        d.port = o.optInt("port", 18082);

                        String key = d.kind + "/" + (d.id.isEmpty() ? d.host + ":" + d.port : d.id);
                        boolean dup = false;
                        for (Device x : out) {
                            String xk = x.kind + "/" + (x.id.isEmpty() ? x.host + ":" + x.port : x.id);
                            if (xk.equals(key)) {
                                dup = true;
                                break;
                            }
                        }
                        if (!dup) out.add(d);
                    } catch (Exception ignored) {
                    }
                }
            } catch (Exception e) {
                err = e.getMessage();
            }
            cb.onResult(out, err);
        });
    }

    private static Set<InetAddress> broadcastAddresses() {
        Set<InetAddress> set = new LinkedHashSet<>();
        try {
            set.add(InetAddress.getByName("255.255.255.255"));
        } catch (Exception ignored) {
        }
        try {
            Enumeration<NetworkInterface> ifaces = NetworkInterface.getNetworkInterfaces();
            while (ifaces != null && ifaces.hasMoreElements()) {
                NetworkInterface ni = ifaces.nextElement();
                if (!ni.isUp() || ni.isLoopback()) continue;
                for (InterfaceAddress ia : ni.getInterfaceAddresses()) {
                    InetAddress b = ia.getBroadcast();
                    if (b != null) set.add(b);
                }
            }
        } catch (Exception ignored) {
        }
        return set;
    }
}

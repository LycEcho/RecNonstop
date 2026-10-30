package discovery

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"strings"
	"time"

	"warehouse-audio/internal/pairing"
)

const (
	MagicV1 = "RECNONSTOP_DISCOVER_V1"
	MagicV2 = "RECNONSTOP_DISCOVER_V2"

	DefaultPort = 18083
)

// Info 是电脑在广播里对外的身份。
type Info struct {
	Kind string
	ID   string
	Name string
	Port int
	FP   string
}

type Service struct {
	Port int
	Info Info
	Pair *pairing.Manager
}

type response struct {
	Service string `json:"service"`
	Version int    `json:"v"`
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Name    string `json:"name"`
	Port    int    `json:"port"`
	FP      string `json:"fp"`
	Pairing bool   `json:"pairing"`
}

// Device 是一次扫描到的设备。
type Device struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
	Host string `json:"host"`
	Port int    `json:"port"`
	FP   string `json:"fp"`
}

// Start 监听探测包。电脑端一直应答（不再要求配对窗口打开），
// 手机才能“扫到就能点”。pairing 字段只用于兼容旧的 6 位码流程。
func Start(s Service) error {
	if s.Port == 0 {
		s.Port = DefaultPort
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: s.Port})
	if err != nil {
		return err
	}

	go func() {
		defer conn.Close()
		buf := make([]byte, 512)
		for {
			n, remote, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			probe := string(buf[:n])
			if probe != MagicV1 && probe != MagicV2 {
				continue
			}
			name := strings.TrimSpace(s.Info.Name)
			if name == "" {
				name = "电脑"
			}
			open := false
			if s.Pair != nil {
				open = s.Pair.WindowOpen()
			}
			resp, _ := json.Marshal(response{
				Service: "recnonstop",
				Version: 2,
				Kind:    firstNonEmpty(s.Info.Kind, "pc"),
				ID:      s.Info.ID,
				Name:    name,
				Port:    s.Info.Port,
				FP:      s.Info.FP,
				Pairing: open,
			})
			_, _ = conn.WriteToUDP(resp, remote)
		}
	}()

	log.Printf("discovery listening on udp/%d", s.Port)
	return nil
}

// Scan 向局域网广播探测包并收集应答。
func Scan(port int, timeout time.Duration) ([]Device, error) {
	return scanAt(broadcastAddrs(), port, timeout)
}

func scanAt(addrs []net.IP, port int, timeout time.Duration) ([]Device, error) {
	if port == 0 {
		port = DefaultPort
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	probe := []byte(MagicV2)
	for _, ip := range addrs {
		_, _ = conn.WriteToUDP(probe, &net.UDPAddr{IP: ip, Port: port})
	}

	deadline := time.Now().Add(timeout)
	_ = conn.SetReadDeadline(deadline)

	seen := map[string]bool{}
	out := make([]Device, 0, 4)
	buf := make([]byte, 2048)
	for {
		n, remote, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				break
			}
			return out, err
		}
		var msg response
		if json.Unmarshal(buf[:n], &msg) != nil || msg.Service != "recnonstop" {
			continue
		}
		kind := firstNonEmpty(msg.Kind, "pc") // 旧版电脑端没有 kind
		id := msg.ID
		if id == "" {
			id = remote.IP.String() + ":" + itoa(msg.Port)
		}
		key := kind + "/" + id
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, Device{
			Kind: kind,
			ID:   id,
			Name: msg.Name,
			Host: remote.IP.String(),
			Port: msg.Port,
			FP:   msg.FP,
		})
	}
	return out, nil
}

func broadcastAddrs() []net.IP {
	out := []net.IP{net.IPv4bcast}
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil || len(ipnet.Mask) != net.IPv4len {
				continue
			}
			b := make(net.IP, net.IPv4len)
			for i := 0; i < net.IPv4len; i++ {
				b[i] = ip4[i] | ^ipnet.Mask[i]
			}
			out = append(out, b)
		}
	}
	return out
}

// Send 给指定主机发一条 UDP 消息（用于电脑主动邀请手机连接）。
func Send(host string, port int, v any) error {
	ip := net.ParseIP(host)
	if ip == nil {
		return errors.New("invalid host")
	}
	if port == 0 {
		port = DefaultPort
	}
	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: ip, Port: port})
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = conn.Write(b)
	return err
}

func firstNonEmpty(v, def string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return def
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

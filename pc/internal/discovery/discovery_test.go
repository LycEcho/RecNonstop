package discovery

import (
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"warehouse-audio/internal/pairing"
)

func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	_ = conn.Close()
	return port
}

func TestRespondAndScan(t *testing.T) {
	port := freeUDPPort(t)
	m, err := pairing.NewManager(filepath.Join(t.TempDir(), "devices.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	err = Start(Service{
		Port: port,
		Info: Info{Kind: "pc", ID: "abcdefabcdefabcd", Name: "测试电脑", Port: 18082, FP: "1234"},
		Pair: m,
	})
	if err != nil {
		t.Fatal(err)
	}

	devs, err := scanAt([]net.IP{net.IPv4(127, 0, 0, 1)}, port, 800*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 {
		t.Fatalf("devices = %d, want 1: %+v", len(devs), devs)
	}
	d := devs[0]
	if d.Kind != "pc" || d.ID != "abcdefabcdefabcd" || d.Name != "测试电脑" || d.FP != "1234" || d.Port != 18082 {
		t.Fatalf("unexpected device: %+v", d)
	}
	if d.Host != "127.0.0.1" {
		t.Fatalf("host = %q", d.Host)
	}
}

func TestLegacyProbeGetsResponse(t *testing.T) {
	port := freeUDPPort(t)
	m, _ := pairing.NewManager(filepath.Join(t.TempDir(), "devices.json"), time.Minute)
	if err := Start(Service{
		Port: port,
		Info: Info{Kind: "pc", ID: "0011223344556677", Name: "电脑", Port: 18082},
		Pair: m,
	}); err != nil {
		t.Fatal(err)
	}

	conn, err := net.DialUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(MagicV1)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	resp := string(buf[:n])
	if !strings.Contains(resp, `"service":"recnonstop"`) || !strings.Contains(resp, `"id":"0011223344556677"`) {
		t.Fatalf("unexpected response: %s", resp)
	}
}

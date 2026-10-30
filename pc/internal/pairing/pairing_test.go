package pairing

import (
	"path/filepath"
	"testing"
	"time"
)

func TestPairPersistAndAuth(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	m, err := NewManager(path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := m.StartWindow()
	d, token, err := m.Pair("Pixel", code)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == "" || token == "" {
		t.Fatal("empty device/token")
	}
	if _, ok := m.Authenticate(token); !ok {
		t.Fatal("authenticate failed")
	}

	// 重新加载：token 仍然有效，否则每次 PC 重启都要重配。
	m2, err := NewManager(path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m2.Authenticate(token); !ok {
		t.Fatal("token lost after reload")
	}
}

func TestWrongCode(t *testing.T) {
	m, _ := NewManager(filepath.Join(t.TempDir(), "devices.json"), time.Minute)
	code, _ := m.StartWindow()
	wrong := "000000"
	if code == wrong {
		wrong = "111111"
	}
	if _, _, err := m.Pair("Pixel", wrong); err == nil {
		t.Fatal("wrong code accepted")
	}
	// 错误的尝试不能把窗口关掉。
	if !m.WindowOpen() {
		t.Fatal("window closed after wrong code")
	}
	if _, _, err := m.Pair("Pixel", code); err != nil {
		t.Fatalf("correct code rejected: %v", err)
	}
}

func TestAtomicFileReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "devices.json")
	m, _ := NewManager(path, time.Minute)
	code, _ := m.StartWindow()
	if _, _, err := m.Pair("Pixel", code); err != nil {
		t.Fatal(err)
	}
	// 主文件和备份都应该存在且能被新 Manager 读取。
	if _, err := NewManager(path, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := NewManager(path+".bak", time.Minute); err != nil {
		t.Fatal(err)
	}
}

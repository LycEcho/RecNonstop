package pairing

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type Device struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	TokenHash string    `json:"token_hash"`
	CreatedAt time.Time `json:"created_at"`
	LastSeen  time.Time `json:"last_seen"`
}

type Manager struct {
	mu       sync.Mutex
	path     string
	devices  map[string]*Device
	code     string
	expires  time.Time
	attempts int
	window   time.Duration
	lastSave time.Time

	// 局域网内等待本机确认的连接请求；只在内存里，重启即失效。
	joins    map[string]*JoinRequest
	lastJoin map[string]time.Time
	// 电脑主动发起的连接邀请：手机凭 token 回来时直接放行。
	invites map[string]invite
	// 电脑端下发给手机的指令（例如“立即同步”），手机拉取后清空。
	commands map[string][]string
}

func NewManager(path string, window time.Duration) (*Manager, error) {
	if window <= 0 {
		window = 10 * time.Minute
	}
	m := &Manager{
		path:     path,
		devices:  map[string]*Device{},
		joins:    map[string]*JoinRequest{},
		lastJoin: map[string]time.Time{},
		invites:  map[string]invite{},
		commands: map[string][]string{},
		window:   window,
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	if b, err := os.ReadFile(path); err == nil {
		var f struct {
			Devices []*Device `json:"devices"`
		}
		if json.Unmarshal(b, &f) == nil {
			for _, d := range f.Devices {
				m.devices[d.ID] = d
			}
		}
	} else if b, err := os.ReadFile(path + ".bak"); err == nil {
		// 主文件损坏/丢失时用备份，避免所有手机都要重新配对。
		var f struct {
			Devices []*Device `json:"devices"`
		}
		if json.Unmarshal(b, &f) == nil {
			for _, d := range f.Devices {
				m.devices[d.ID] = d
			}
		}
	}
	return m, nil
}

func (m *Manager) StartWindow() (string, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	code := fmt.Sprintf("%06d", randomInt(1000000))
	m.code = code
	m.expires = time.Now().Add(m.window)
	m.attempts = 0
	return code, m.expires
}

func (m *Manager) HasDevices() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.devices) > 0
}

func (m *Manager) WindowOpen() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.code != "" && time.Now().Before(m.expires)
}

func (m *Manager) Status() (code string, expires time.Time, open bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.code, m.expires, m.code != "" && time.Now().Before(m.expires)
}

func (m *Manager) Pair(name, code string) (*Device, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.code == "" || time.Now().After(m.expires) {
		return nil, "", errors.New("pairing window closed")
	}
	if m.attempts >= 20 {
		return nil, "", errors.New("too many attempts")
	}
	m.attempts++
	if subtle.ConstantTimeCompare([]byte(code), []byte(m.code)) != 1 {
		return nil, "", errors.New("invalid pairing code")
	}

	token := randomHex(32)
	d := &Device{
		ID:        randomHex(8),
		Name:      name,
		TokenHash: hashToken(token),
		CreatedAt: time.Now(),
		LastSeen:  time.Now(),
	}
	m.devices[d.ID] = d
	m.code = ""
	m.expires = time.Time{}
	m.saveLocked()
	return d, token, nil
}

func (m *Manager) Authenticate(token string) (*Device, bool) {
	if token == "" {
		return nil, false
	}
	h := hashToken(token)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.devices {
		if d.TokenHash == h {
			d.LastSeen = time.Now()
			// 以前每个请求都重写 devices.json（手机每 2 秒轮询一次），
			// 现在最多 5 分钟落一次盘，减少锁内 I/O。
			if time.Since(m.lastSave) > 5*time.Minute {
				m.saveLocked()
			}
			return d, true
		}
	}
	return nil, false
}

func (m *Manager) Devices() []Device {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Device, 0, len(m.devices))
	for _, d := range m.devices {
		cp := *d
		cp.TokenHash = ""
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (m *Manager) Revoke(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.devices[id]; !ok {
		return false
	}
	delete(m.devices, id)
	m.saveLocked()
	return true
}

func (m *Manager) saveLocked() {
	f := struct {
		Devices []*Device `json:"devices"`
	}{
		Devices: make([]*Device, 0, len(m.devices)),
	}
	for _, d := range m.devices {
		f.Devices = append(f.Devices, d)
	}
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return
	}
	if err := atomicWrite(m.path, b, 0o600); err != nil {
		return
	}
	m.lastSave = time.Now()
	// 备份一份，主文件损坏时还能恢复。
	_ = atomicWrite(m.path+".bak", b, 0o600)
}

func atomicWrite(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randomInt(max int) int {
	if max <= 0 {
		return 0
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0
	}
	return int(n.Int64())
}

// Package identity 维护本机（电脑端）在局域网里的稳定身份。
//
// ID 是首次运行时随机生成的，不取主机名、MAC 或任何硬件标识，
// 避免把设备指纹和真实机器绑定。指纹只用于界面上区分设备。
package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type Identity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// LoadOrCreate 从 path 读取身份；不存在时生成一个。
// defaultName 非空时以配置为准（方便改电脑名）；为空时沿用已保存的名字，
// 首次生成则使用 "电脑 <指纹后四位>"，方便同一网络里区分多台电脑。
func LoadOrCreate(path, defaultName string) (*Identity, error) {
	if b, err := os.ReadFile(path); err == nil {
		var id Identity
		if json.Unmarshal(b, &id) == nil && validID(id.ID) {
			want := strings.TrimSpace(defaultName)
			switch {
			case want != "" && want != id.Name:
				id.Name = want
				_ = save(path, &id)
			case strings.TrimSpace(id.Name) == "":
				id.Name = defaultNameFor(id.ID)
				_ = save(path, &id)
			}
			return &id, nil
		}
	}

	id := &Identity{ID: randomHex(8)}
	id.Name = strings.TrimSpace(defaultName)
	if id.Name == "" {
		id.Name = defaultNameFor(id.ID)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := save(path, id); err != nil {
		return nil, err
	}
	return id, nil
}

func save(path string, id *Identity) error {
	b, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

// Fingerprint 返回 4 位十六进制短指纹，只用于人工区分设备。
func (i *Identity) Fingerprint() string {
	if i == nil {
		return ""
	}
	sum := sha256.Sum256([]byte(i.ID))
	return strings.ToUpper(hex.EncodeToString(sum[:2]))
}

func defaultNameFor(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "电脑 " + strings.ToUpper(hex.EncodeToString(sum[:2]))
}

func validID(s string) bool {
	if len(s) != 16 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

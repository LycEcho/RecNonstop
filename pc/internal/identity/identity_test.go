package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreatePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")

	id, err := LoadOrCreate(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(id.ID) != 16 {
		t.Fatalf("id = %q", id.ID)
	}
	if id.Name == "" || id.Fingerprint() == "" {
		t.Fatalf("empty name/fingerprint: %+v", id)
	}

	// 第二次启动：ID / 指纹不变，名字也保持。
	again, err := LoadOrCreate(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != id.ID || again.Fingerprint() != id.Fingerprint() {
		t.Fatal("identity changed across restarts")
	}
	if again.Name != id.Name {
		t.Fatalf("name changed to %q", again.Name)
	}

	// 配置里显式给了名字：以配置为准。
	renamed, err := LoadOrCreate(path, "书房电脑")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ID != id.ID || renamed.Name != "书房电脑" {
		t.Fatalf("rename failed: %+v", renamed)
	}
}

func TestCustomName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	id, err := LoadOrCreate(path, "书房电脑")
	if err != nil {
		t.Fatal(err)
	}
	if id.Name != "书房电脑" {
		t.Fatalf("name = %q", id.Name)
	}
}

func TestCorruptFileRegenerated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err := LoadOrCreate(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(id.ID) != 16 {
		t.Fatalf("id = %q", id.ID)
	}
}

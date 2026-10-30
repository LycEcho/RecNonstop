package pairing

import (
	"strings"
	"testing"
	"time"
)

func newJoinManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(t.TempDir()+"/devices.json", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestJoinApproveAndToken(t *testing.T) {
	m := newJoinManager(t)

	req, err := m.RequestJoin("0011223344556677", "手机 3F2A", "3F2A", "192.0.2.20", "")
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != "pending" {
		t.Fatalf("status = %q", req.Status)
	}
	if len(m.Pending()) != 1 {
		t.Fatal("pending request not listed")
	}
	// 未确认前不能拿到 token。
	if s, tok, _ := m.JoinStatus(req.ID); s != "pending" || tok != "" {
		t.Fatalf("pending status leaked token: %q %q", s, tok)
	}

	d, err := m.Approve(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "0011223344556677" {
		t.Fatalf("device id = %q", d.ID)
	}
	status, token, dev := m.JoinStatus(req.ID)
	if status != "approved" || token == "" || dev == nil {
		t.Fatalf("approved status = %q token empty=%v dev=%v", status, token == "", dev == nil)
	}
	if _, ok := m.Authenticate(token); !ok {
		t.Fatal("token does not authenticate")
	}
	// 重复查询仍是 approved（手机可能重试），token 不变。
	if _, token2, _ := m.JoinStatus(req.ID); token2 != token {
		t.Fatal("token changed between polls")
	}
}

func TestJoinReject(t *testing.T) {
	m := newJoinManager(t)
	req, _ := m.RequestJoin("aabbccddeeff0011", "手机", "", "192.0.2.21", "")
	if err := m.Reject(req.ID); err != nil {
		t.Fatal(err)
	}
	if s, tok, _ := m.JoinStatus(req.ID); s != "rejected" || tok != "" {
		t.Fatalf("status = %q", s)
	}
	if _, err := m.Approve(req.ID); err == nil {
		t.Fatal("rejected request was approved")
	}
}

func TestJoinExpires(t *testing.T) {
	m := newJoinManager(t)
	req, _ := m.RequestJoin("aabbccddeeff0022", "手机", "", "192.0.2.22", "")
	m.mu.Lock()
	m.joins[req.ID].expires = time.Now().Add(-time.Second)
	m.mu.Unlock()

	if s, _, _ := m.JoinStatus(req.ID); s != "expired" {
		t.Fatalf("status = %q", s)
	}
	if len(m.Pending()) != 0 {
		t.Fatal("expired request still listed as pending")
	}
	if _, err := m.Approve(req.ID); err != ErrJoinExpired {
		t.Fatalf("approve expired err = %v", err)
	}
}

func TestRejoinRotatesToken(t *testing.T) {
	m := newJoinManager(t)
	id := "00112233445566aa"
	r1, _ := m.RequestJoin(id, "手机", "AAAA", "192.0.2.23", "")
	if _, err := m.Approve(r1.ID); err != nil {
		t.Fatal(err)
	}
	_, tok1, _ := m.JoinStatus(r1.ID)

	// 手机清空 token 后重新申请：复用同一台设备，旧 token 失效。
	r2, err := m.RequestJoin(id, "手机", "AAAA", "192.0.2.24", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Approve(r2.ID); err != nil {
		t.Fatal(err)
	}
	_, tok2, _ := m.JoinStatus(r2.ID)
	if tok2 == "" || tok2 == tok1 {
		t.Fatal("token was not rotated")
	}
	if _, ok := m.Authenticate(tok1); ok {
		t.Fatal("old token still valid")
	}
	if _, ok := m.Authenticate(tok2); !ok {
		t.Fatal("new token invalid")
	}
	if len(m.Devices()) != 1 {
		t.Fatalf("devices = %d, want 1", len(m.Devices()))
	}
}

func TestJoinRateLimit(t *testing.T) {
	m := newJoinManager(t)
	if _, err := m.RequestJoin("1111111111111111", "a", "", "192.0.2.30", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RequestJoin("2222222222222222", "b", "", "192.0.2.30", ""); err != ErrTooManyRequests {
		t.Fatalf("err = %v, want rate limit", err)
	}
	if _, err := m.RequestJoin("2222222222222222", "b", "", "192.0.2.31", ""); err != nil {
		t.Fatalf("other host rejected: %v", err)
	}
}

func TestSyncCommand(t *testing.T) {
	m := newJoinManager(t)
	req, _ := m.RequestJoin("00112233445566bb", "手机", "", "192.0.2.25", "")
	d, err := m.Approve(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RequestSync(d.ID); err != nil {
		t.Fatal(err)
	}
	// 重复点同步只排一次。
	if err := m.RequestSync(d.ID); err != nil {
		t.Fatal(err)
	}
	cmds := m.TakeCommands(d.ID)
	if len(cmds) != 1 || cmds[0] != "sync" {
		t.Fatalf("commands = %v", cmds)
	}
	if got := m.TakeCommands(d.ID); len(got) != 0 {
		t.Fatalf("commands not cleared: %v", got)
	}
	if err := m.RequestSync("not-a-device"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestInviteAutoApprove(t *testing.T) {
	m := newJoinManager(t)
	const id = "00112233445566cc"

	tok, err := m.CreateInvite(id)
	if err != nil {
		t.Fatal(err)
	}
	req, err := m.RequestJoin(id, "手机", "BBBB", "192.0.2.40", tok)
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != "approved" {
		t.Fatalf("status = %q, want approved", req.Status)
	}
	s, token, d := m.JoinStatus(req.ID)
	if s != "approved" || token == "" || d == nil {
		t.Fatalf("invite token: %q %v %v", s, token, d)
	}
	if _, ok := m.Authenticate(token); !ok {
		t.Fatal("invite token invalid")
	}

	// 邀请一次性：同一个 token 再用只能走普通待确认流程。
	req2, err := m.RequestJoin(id, "手机", "BBBB", "192.0.2.41", tok)
	if err != nil {
		t.Fatal(err)
	}
	if req2.Status != "pending" {
		t.Fatalf("reused invite status = %q", req2.Status)
	}
}

func TestWrongInviteStaysPending(t *testing.T) {
	m := newJoinManager(t)
	req, err := m.RequestJoin("00112233445566dd", "手机", "", "192.0.2.42", "deadbeefdeadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if req.Status != "pending" {
		t.Fatalf("status = %q", req.Status)
	}
}

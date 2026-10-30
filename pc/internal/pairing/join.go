package pairing

import (
	"crypto/subtle"
	"errors"
	"sort"
	"time"
)

const (
	joinTTL         = 5 * time.Minute  // 点“连接”后等对方确认的时间
	joinKeep        = 30 * time.Minute // 结束后还留多久，方便重复轮询
	maxPendingJoins = 5
	joinCooldown    = 2 * time.Second
)

var (
	ErrTooManyRequests = errors.New("too many pending requests")
	ErrJoinNotFound    = errors.New("join request not found")
	ErrJoinExpired     = errors.New("join request expired")
)

// JoinRequest 是手机在局域网里发起的连接申请，需要在本机页面点“允许”。
type JoinRequest struct {
	ID       string    `json:"id"`
	DeviceID string    `json:"device_id"`
	Name     string    `json:"name"`
	FP       string    `json:"fp"`
	Created  time.Time `json:"created_at"`
	Status   string    `json:"status"`

	token   string
	expires time.Time
}

// 电脑主动发起的邀请：只有拿到这个 token 的手机才能免确认加入。
type invite struct {
	token   string
	expires time.Time
}

// RequestJoin 登记一台新手机。deviceID 由手机自己生成并长期复用，
// 这样重新安装/丢 token 后仍能认出是同一台设备。
//
// inviteToken 非空且匹配电脑刚发出的邀请时，直接通过（电脑点“连接”已经算确认，
// 手机端再点一次允许即可），避免两边来回确认。
func (m *Manager) RequestJoin(deviceID, name, fp, remote, inviteToken string) (*JoinRequest, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.pruneJoinsLocked()
	if last, ok := m.lastJoin[remote]; ok && time.Since(last) < joinCooldown {
		return nil, ErrTooManyRequests
	}

	pending := 0
	for _, r := range m.joins {
		if r.Status != "pending" {
			continue
		}
		if r.DeviceID == deviceID {
			// 同一台手机重复点连接：复用原请求，避免堆一堆弹窗。
			r.Name = name
			r.FP = fp
			m.lastJoin[remote] = time.Now()
			return copyJoin(r), nil
		}
		pending++
	}
	if pending >= maxPendingJoins {
		return nil, ErrTooManyRequests
	}

	now := time.Now()
	r := &JoinRequest{
		ID:       randomHex(8),
		DeviceID: deviceID,
		Name:     name,
		FP:       fp,
		Created:  now,
		Status:   "pending",
		expires:  now.Add(joinTTL),
	}
	m.joins[r.ID] = r
	m.lastJoin[remote] = now

	if inv, ok := m.invites[deviceID]; ok && now.Before(inv.expires) &&
		subtle.ConstantTimeCompare([]byte(inv.token), []byte(inviteToken)) == 1 {
		delete(m.invites, deviceID)
		_, _ = m.approveJoinLocked(r)
	}
	return copyJoin(r), nil
}

// CreateInvite 由电脑发起：给指定手机发一个一次性邀请 token。
func (m *Manager) CreateInvite(deviceID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneJoinsLocked()
	token := randomHex(16)
	m.invites[deviceID] = invite{token: token, expires: time.Now().Add(joinTTL)}
	return token, nil
}

func (r *JoinRequest) Expires() time.Time { return r.expires }

func (m *Manager) Pending() []JoinRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneJoinsLocked()

	out := make([]JoinRequest, 0, len(m.joins))
	for _, r := range m.joins {
		if r.Status == "pending" {
			out = append(out, *copyJoin(r))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
	return out
}

// Approve 允许连接：已有设备则轮换 token（手机丢 token 后可重新连），否则新建。
func (m *Manager) Approve(id string) (*Device, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	r, ok := m.joins[id]
	if !ok {
		return nil, ErrJoinNotFound
	}
	return m.approveJoinLocked(r)
}

func (m *Manager) approveJoinLocked(r *JoinRequest) (*Device, error) {
	if r.Status != "pending" {
		return nil, ErrJoinExpired
	}
	if time.Now().After(r.expires) {
		r.Status = "expired"
		return nil, ErrJoinExpired
	}

	token := randomHex(32)
	d := m.devices[r.DeviceID]
	if d == nil {
		d = &Device{ID: r.DeviceID, CreatedAt: time.Now()}
		m.devices[d.ID] = d
	}
	d.Name = r.Name
	d.TokenHash = hashToken(token)
	d.LastSeen = time.Now()

	r.token = token
	r.Status = "approved"
	m.saveLocked()
	return d, nil
}

func (m *Manager) Reject(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.joins[id]
	if !ok {
		return ErrJoinNotFound
	}
	if r.Status == "pending" {
		r.Status = "rejected"
	}
	return nil
}

// JoinStatus 供手机轮询。approved 时返回 token。
func (m *Manager) JoinStatus(id string) (status, token string, d *Device) {
	m.mu.Lock()
	defer m.mu.Unlock()

	r, ok := m.joins[id]
	if !ok {
		return "expired", "", nil
	}
	if r.Status == "pending" && time.Now().After(r.expires) {
		r.Status = "expired"
	}
	if r.Status != "approved" {
		return r.Status, "", nil
	}
	dev := m.devices[r.DeviceID]
	return "approved", r.token, dev
}

func (m *Manager) HasDevice(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.devices[id]
	return ok
}

func (m *Manager) RequestSync(deviceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.devices[deviceID]; !ok {
		return ErrJoinNotFound
	}
	for _, c := range m.commands[deviceID] {
		if c == "sync" {
			return nil
		}
	}
	if len(m.commands[deviceID]) >= 10 {
		return ErrTooManyRequests
	}
	m.commands[deviceID] = append(m.commands[deviceID], "sync")
	return nil
}

// TakeCommands 返回并清空该设备待执行的指令。
func (m *Manager) TakeCommands(deviceID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.commands[deviceID]
	delete(m.commands, deviceID)
	return c
}

func (m *Manager) pruneJoinsLocked() {
	now := time.Now()
	for id, r := range m.joins {
		if r.Status == "pending" && now.After(r.expires) {
			r.Status = "expired"
		}
		if now.Sub(r.Created) > joinKeep {
			delete(m.joins, id)
		}
	}
	for id, inv := range m.invites {
		if now.After(inv.expires) {
			delete(m.invites, id)
		}
	}
	// 冷却表只记最近一分钟，避免无限增长。
	for ip, t := range m.lastJoin {
		if time.Since(t) > time.Minute {
			delete(m.lastJoin, ip)
		}
	}
}

func copyJoin(r *JoinRequest) *JoinRequest {
	c := *r
	c.token = ""
	return &c
}

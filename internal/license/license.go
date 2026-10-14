// Package license 实现商业化收费授权（license）的文档模型、签名与校验。
//
// 授权文档编码为 base64(payloadJSON) + "." + base64(Ed25519 签名)。载荷
// 携带产品、设备绑定、计划与时间窗；签名私钥由运营侧持有（铸造工具
// 私有运维侧铸造），NVR 侧只持公钥验签。授权文档非机密（签名即完整性，
// 无可泄露内容），明文存于配置 yaml。
//
// 商业语义：最低计费粒度 0.5 元 / 12 小时——铸造侧以 12h 块为最小时间窗
// （[Block]）；校验侧只认时间窗本身，任何窗口长度均合法，块粒度是铸造
// 政策而非校验政策。
package license

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Block 是最小授权时间块，对齐最低计费粒度（0.5 元 / 12 小时）。
const Block = 12 * time.Hour

// ExpiringSoonWindow 是「即将到期」提示窗（信息性状态，不拦截放行）。
// 取 6h（< 最小授权块 12h）：标准 12h 授权前半程 valid、后半程 expiring。
const ExpiringSoonWindow = 6 * time.Hour

// Product 是 NVR 侧授权产品命名空间。
const Product = "mibeenvr"

// State 是授权评估结果。
type State string

const (
	// StateNone 未配置授权文档（或未配公钥）。
	StateNone State = "none"
	// StateInvalid 验签失败/字段不合法/产品或设备不匹配/尚未生效。
	StateInvalid State = "invalid"
	// StateExpired 已过 expires_at。
	StateExpired State = "expired"
	// StateExpiringSoon 有效但即将到期（≤ ExpiringSoonWindow）。
	StateExpiringSoon State = "expiring"
	// StateValid 有效。
	StateValid State = "valid"
)

// Document 是授权文档载荷。
type Document struct {
	Version   int       `json:"v"`
	Product   string    `json:"product"`    // 授权产品（"mibeenvr"）
	DeviceID  string    `json:"device_id"`  // 设备绑定（NVR 安装级 Server.DeviceID）
	Plan      string    `json:"plan"`       // 计划名（如 "p2p-remote"）
	Features  []string  `json:"features"`   // 授权能力清单（如 ["p2p"]）
	Serial    string    `json:"serial"`     // 运营侧序列号（审计/对账）
	NotBefore time.Time `json:"not_before"` // 生效时刻（含）
	ExpiresAt time.Time `json:"expires_at"` // 失效时刻（不含）
	IssuedAt  time.Time `json:"issued_at"`  // 铸造时刻

	// MaxClients 是该授权允许的 P2P 并发客户端配额（0 = 不限）。与
	// p2p.max_clients 配置取更严者生效。
	MaxClients int `json:"max_clients,omitempty"`
}

// Sign 用私钥对文档编码签名，返回 "payload.sig" 形态的授权文档串。
func Sign(priv ed25519.PrivateKey, doc *Document) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("license: private key must be %d bytes, got %d", ed25519.PrivateKeySize, len(priv))
	}
	payload, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("license: marshal payload: %w", err)
	}
	sig := ed25519.Sign(priv, payload)
	return base64.RawStdEncoding.EncodeToString(payload) + "." +
		base64.RawStdEncoding.EncodeToString(sig), nil
}

// Decode 拆解授权文档串，返回载荷原文（验签输入）、签名与解析后的文档。
// 不做任何语义校验——调用方继续 Verify / Evaluate。
func Decode(document string) (payload, sig []byte, doc *Document, err error) {
	parts := strings.Split(strings.TrimSpace(document), ".")
	if len(parts) != 2 {
		return nil, nil, nil, fmt.Errorf("license: document must be payload.sig")
	}
	payload, err = base64.RawStdEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("license: payload decode: %w", err)
	}
	sig, err = base64.RawStdEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, nil, nil, fmt.Errorf("license: signature decode: %w", err)
	}
	doc = &Document{}
	if err := json.Unmarshal(payload, doc); err != nil {
		return nil, nil, nil, fmt.Errorf("license: payload unmarshal: %w", err)
	}
	return payload, sig, doc, nil
}

// Verify 验证文档签名（仅密码学校验）。
func Verify(pub ed25519.PublicKey, payload, sig []byte) bool {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(pub, payload, sig)
}

// ParsePublicKey 解析 base64 编码的 Ed25519 公钥（32 字节）。
func ParsePublicKey(b64 string) (ed25519.PublicKey, error) {
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		// 兼容带填充的标准编码。
		if raw, err = base64.StdEncoding.DecodeString(strings.TrimSpace(b64)); err != nil {
			return nil, fmt.Errorf("license: public key decode: %w", err)
		}
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("license: public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

// Evaluate 在给定时刻对文档做完整评估（验签由调用方先行；此处为语义与
// 时间窗）。返回终态与原因（终态非 valid 时非空）。
func Evaluate(doc *Document, deviceID string, now time.Time) (State, string) {
	switch {
	case doc.Product != Product:
		return StateInvalid, fmt.Sprintf("product %q does not match %q", doc.Product, Product)
	case deviceID != "" && doc.DeviceID != deviceID:
		return StateInvalid, fmt.Sprintf("document bound to device %q, this device is %q", doc.DeviceID, deviceID)
	case doc.ExpiresAt.IsZero() || doc.NotBefore.IsZero():
		return StateInvalid, "not_before/expires_at required"
	case now.Before(doc.NotBefore):
		return StateInvalid, "not valid before " + doc.NotBefore.Format(time.RFC3339)
	case !now.Before(doc.ExpiresAt):
		return StateExpired, "expired at " + doc.ExpiresAt.Format(time.RFC3339)
	case doc.ExpiresAt.Sub(now) <= ExpiringSoonWindow:
		return StateExpiringSoon, ""
	default:
		return StateValid, ""
	}
}

// Status 是一次授权评估的完整快照（API/诊断用）。
type Status struct {
	State       State     `json:"state"`
	Required    bool      `json:"required"`
	Document    *Document `json:"document,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	TimeLeftSec int64     `json:"time_left_sec,omitempty"`
	Allowed     bool      `json:"allowed"`
}

// Manager 持有当前授权配置并线程安全地评估状态。配置热更新（API 安装
// 新文档/换公钥/改 required）后，消费方（P2P 门控）在下一个检查周期
// 自然感知，无需重启。
type Manager struct {
	mu       sync.RWMutex
	document string
	pub      ed25519.PublicKey
	deviceID string
	required bool
}

// NewManager 构造授权管理器。deviceID 是安装级设备身份（授权绑定目标）。
func NewManager(deviceID string) *Manager {
	return &Manager{deviceID: deviceID}
}

// Configure 全量更新授权配置。document/publicKey 为空串表示清除该项。
// 只做可解码性预检（语法层）；验签与时间窗在 Status 评估时进行——过期
// 或无效不是配置错误，是运行态。
func (m *Manager) Configure(document, publicKey string, required bool) error {
	var pub ed25519.PublicKey
	if strings.TrimSpace(publicKey) != "" {
		var err error
		if pub, err = ParsePublicKey(publicKey); err != nil {
			return err
		}
	}
	if strings.TrimSpace(document) != "" {
		if _, _, _, err := Decode(document); err != nil {
			return err
		}
	}
	m.mu.Lock()
	m.document, m.pub, m.required = strings.TrimSpace(document), pub, required
	m.mu.Unlock()
	return nil
}

// SetRequired 单独切换强制开关（保留已配文档/公钥）。
func (m *Manager) SetRequired(required bool) {
	m.mu.Lock()
	m.required = required
	m.mu.Unlock()
}

// Required 报告授权是否为强制项。
func (m *Manager) Required() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.required
}

// Status 在此刻评估授权。未配文档或未配公钥 → StateNone（配置不完整
// 无法评估）；其余走验签 + Evaluate。
func (m *Manager) Status() Status {
	m.mu.RLock()
	document, pub, deviceID, required := m.document, m.pub, m.deviceID, m.required
	m.mu.RUnlock()

	st := Status{State: StateNone, Required: required}
	if strings.TrimSpace(document) == "" || pub == nil {
		st.Reason = "no license document or public key configured"
		st.Allowed = !required
		return st
	}
	payload, sig, doc, err := Decode(document)
	if err != nil {
		st.State, st.Reason = StateInvalid, err.Error()
		st.Allowed = !required
		return st
	}
	if !Verify(pub, payload, sig) {
		st.State, st.Reason = StateInvalid, "signature verification failed"
		st.Allowed = !required
		return st
	}
	now := time.Now()
	state, reason := Evaluate(doc, deviceID, now)
	st.State, st.Document, st.Reason = state, doc, reason
	if doc != nil && !doc.ExpiresAt.IsZero() {
		if left := time.Until(doc.ExpiresAt); left > 0 {
			st.TimeLeftSec = int64(left.Seconds())
		}
	}
	st.Allowed = !required || state == StateValid || state == StateExpiringSoon
	return st
}

// Allowed 报告 P2P 等受控能力此刻是否放行：非强制恒放行（能力在场但不
// 启用）；强制时仅 valid/expiring 放行。
func (m *Manager) Allowed() bool {
	return m.Status().Allowed
}

// FeatureAllowed 报告命名能力（如 "p2p"）是否被授权授予。授权未强制或
// 未配置文档时恒放行（与 Allowed 的语义一致——缺席的授权不约束任何
// 能力）；文档有效时按 features 清单判定，空清单 = 授予全部（向后兼容
// 存量文档）。
func (m *Manager) FeatureAllowed(feature string) bool {
	st := m.Status()
	if !st.Required {
		return true
	}
	if st.Document == nil {
		return st.Allowed
	}
	if len(st.Document.Features) == 0 {
		return true
	}
	for _, f := range st.Document.Features {
		if f == feature {
			return true
		}
	}
	return false
}

// MaxClients 报告授权文档携带的 P2P 并发客户端配额（0 = 不限/未配）。
// 仅在文档处于 valid/expiring 时有意义——被拦截（invalid/expired）的
// 授权本来就不放行拨号。
func (m *Manager) MaxClients() int {
	st := m.Status()
	if st.Document == nil {
		return 0
	}
	switch st.State {
	case StateValid, StateExpiringSoon:
		return st.Document.MaxClients
	}
	return 0
}

// State 返回当前状态名（日志/诊断简报）。
func (m *Manager) State() string {
	return string(m.Status().State)
}

// GenerateKeyPair 生成运营用 Ed25519 密钥对（铸造工具与测试共用）。
func GenerateKeyPair() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	return ed25519.GenerateKey(rand.Reader)
}

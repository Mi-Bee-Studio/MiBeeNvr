// Package viewer 管理被分享账号在 NVR 上的受限只读会话（分享执行链的
// 设备端，user-tenant-model §7）。会话由平台核验结论铸造：owner 全功能、
// 被分享账号按 cameras 白名单（空=整机）；只读面由 auth 中间件的
// viewer 分支强制（方法/路径/摄像头三重过滤）。
package viewer

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

// TokenPrefix 观看者会话 token 前缀（区别于 mbs_ 管理会话与 mbv_ API key）。
const TokenPrefix = "mbviewer_"

// Session 一场已核验的观看者会话。
type Session struct {
	AccountID string   `json:"account_id"`
	IsOwner   bool     `json:"is_owner"`
	Cameras   []string `json:"cameras,omitempty"` // 空 = 整机
	ExpiresAt time.Time
}

// CameraAllowed 报告会话是否可看指定摄像头（整机范围恒真）。
func (s *Session) CameraAllowed(cameraID string) bool {
	if len(s.Cameras) == 0 {
		return true
	}
	for _, c := range s.Cameras {
		if c == cameraID {
			return true
		}
	}
	return false
}

// Store 进程内会话仓（登录铸造/核验/过期清扫）。
type Store struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func NewStore() *Store {
	return &Store{sessions: make(map[string]*Session)}
}

// Login 铸造一枚会话 token。TTL 取 min(2h, scopeExpire)——平台核验不带
// 显式过期时用上限值；吊销经下一次登录拒绝生效（会话最长 2h）。
func (st *Store) Login(scope Scope, now time.Time) (string, *Session, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	ttl := 2 * time.Hour
	sess := &Session{
		AccountID: scope.AccountID,
		IsOwner:   scope.IsOwner,
		Cameras:   scope.Cameras,
		ExpiresAt: now.Add(ttl),
	}
	token := TokenPrefix + hex.EncodeToString(raw)
	st.mu.Lock()
	st.pruneLocked(now)
	st.sessions[token] = sess
	st.mu.Unlock()
	return token, sess, nil
}

// Verify 校验 token；过期即失效（顺手清扫）。
func (st *Store) Verify(token string, now time.Time) (*Session, bool) {
	if !strings.HasPrefix(token, TokenPrefix) {
		return nil, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	st.pruneLocked(now)
	s, ok := st.sessions[token]
	if !ok || now.After(s.ExpiresAt) {
		return nil, false
	}
	return s, true
}

func (st *Store) pruneLocked(now time.Time) {
	for k, s := range st.sessions {
		if now.After(s.ExpiresAt) {
			delete(st.sessions, k)
		}
	}
}

// Scope 是登录输入（来自平台核验结论）。
type Scope struct {
	AccountID string
	IsOwner   bool
	Cameras   []string
}

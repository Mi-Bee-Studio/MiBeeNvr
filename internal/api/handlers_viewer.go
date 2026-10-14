package api

// handlers_viewer.go — 分享执行链的 NVR HTTP 面（user-tenant-model §7）。
//
//	POST /api/p2p/viewer/login   匿名+限流：观看者用户 JWT → 平台核验 →
//	                            铸 mbviewer_ 只读会话（范围=grant 的
//	                            cameras 白名单，空=整机；owner 全功能）
//	GET  /api/viewer/session     会话自述（谁/范围/到期）
//	GET  /api/viewer/cameras     摄像头列表（按范围过滤，只读字段）
//	GET  /api/viewer/recordings  录像列表（按范围过滤；?camera_id= 收窄）
//
// 直播/快照走既有 /api/cameras/{id}/stream*、/snapshot 端点——auth 中间件
// 的 viewer 分支按「GET+白名单子路径+摄像头范围」三重过滤放行。
// 回放下载带不了摄像头上下文（路径里是录像 id），经
// GET /api/viewer/recordings/{id}/download 做归属校验后转发。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/middleware"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/p2p"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/viewer"
)

// ViewerLoginFn 核验观看者用户 JWT（由 pkg/app 注入——桥接 p2p Manager 的
// 设备身份；P2P 未启用/未绑定时返回错误）。
type ViewerLoginFn func(ctx context.Context, viewerJWT string) (*p2p.ViewerScope, error)

// SetViewerAuth wires the share-flow viewer surface. Any nil disables it.
func (h *Handler) SetViewerAuth(store *viewer.Store, login ViewerLoginFn) {
	h.viewerStore = store
	h.viewerLogin = login
}

// registerViewerLoginRoutes mounts the anonymous login endpoint (rate-limited:
// the platform核验 is the credential; brute-force noise still gets bounded).
// 恒挂载——P2P 未启用/未绑定时请求层返回 503（路由构建先于 Manager 拉起，
// 条件挂载会让端点永不出现）。
func (h *Handler) registerViewerLoginRoutes(r chi.Router) {
	rl := middleware.NewRateLimiter(context.Background(), middleware.RateLimiterConfig{
		MaxRequests: 30,
		Window:      time.Minute,
	})
	r.Group(func(r chi.Router) {
		r.Use(rl.Handler)
		r.Post("/api/p2p/viewer/login", h.handleViewerLogin)
	})
}

// registerViewerRoutes mounts the authenticated read-only surface.
// 恒挂载——与 login 路由同因：路由构建先于 P2P Manager 拉起（SetViewerAuth
// 晚于 RegisterRoutes），条件挂载会让 /api/viewer/* 永久 404（2026-10-06
// E2E 实证：guest 首刷 listViewerCameras 404）。未就绪时请求层 503。
func (h *Handler) registerViewerRoutes(r chi.Router) {
	r.Get("/api/viewer/session", h.requireViewerAuth(h.handleViewerSession))
	r.Get("/api/viewer/cameras", h.requireViewerAuth(h.handleViewerCameras))
	r.Get("/api/viewer/recordings", h.requireViewerAuth(h.handleViewerRecordings))
	r.Get("/api/viewer/recordings/{id}/download", h.requireViewerAuth(h.handleViewerRecordingDownload))
}

// requireViewerAuth gates the viewer surface on runtime availability.
func (h *Handler) requireViewerAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.viewerStore == nil || h.viewerLogin == nil {
			WriteError(w, http.StatusServiceUnavailable, "viewer auth not active (p2p not enrolled)")
			return
		}
		next(w, r)
	}
}

func (h *Handler) handleViewerLogin(w http.ResponseWriter, r *http.Request) {
	if h.viewerStore == nil || h.viewerLogin == nil {
		WriteError(w, http.StatusServiceUnavailable, "viewer auth not active (p2p not enrolled)")
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body)
	tok := strings.TrimSpace(body.Token)
	if tok == "" {
		tok = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if tok == "" || strings.HasPrefix(tok, viewer.TokenPrefix) {
		WriteError(w, http.StatusBadRequest, "platform token required")
		return
	}
	scope, err := h.viewerLogin(r.Context(), tok)
	if err != nil {
		if errors.Is(err, p2p.ErrNoShare) {
			WriteError(w, http.StatusForbidden, "no active share for this account")
			return
		}
		if errors.Is(err, p2p.ErrMintBackoff) {
			WriteError(w, http.StatusTooManyRequests, "device busy; retry shortly")
			return
		}
		WriteError(w, http.StatusBadGateway, "platform verify failed")
		return
	}
	token, sess, err := h.viewerStore.Login(viewer.Scope{
		AccountID: scope.AccountID,
		IsOwner:   scope.IsOwner,
		Cameras:   scope.Cameras,
	}, time.Now())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "session mint failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":      token,
		"account_id": sess.AccountID,
		"is_owner":   sess.IsOwner,
		"cameras":    sess.Cameras,
		"expires_at": sess.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func (h *Handler) viewerOf(r *http.Request) *viewer.Session {
	return middleware.ViewerFromContext(r.Context())
}

func (h *Handler) handleViewerSession(w http.ResponseWriter, r *http.Request) {
	s := h.viewerOf(r)
	if s == nil {
		WriteError(w, http.StatusForbidden, "viewer session required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"account_id": s.AccountID,
		"is_owner":   s.IsOwner,
		"cameras":    s.Cameras,
		"expires_at": s.ExpiresAt.UTC().Format(time.RFC3339),
	})
}

func (h *Handler) handleViewerCameras(w http.ResponseWriter, r *http.Request) {
	s := h.viewerOf(r)
	if s == nil {
		WriteError(w, http.StatusForbidden, "viewer session required")
		return
	}
	cameras, err := h.db.ListCameras(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list cameras")
		return
	}
	status := map[string]model.RecorderStatus{}
	if h.camMgr != nil {
		status = h.camMgr.Status()
	}
	type viewerCamera struct {
		ID     string               `json:"id"`
		Name   string               `json:"name"`
		Status model.RecorderStatus `json:"status"`
		Group  string               `json:"group,omitempty"`
	}
	out := make([]viewerCamera, 0, len(cameras))
	for _, c := range cameras {
		if !s.CameraAllowed(c.ID) {
			continue
		}
		st, ok := status[c.ID]
		if !ok {
			st = model.StatusStopped
		}
		out = append(out, viewerCamera{ID: c.ID, Name: c.Name, Status: st, Group: c.GroupName})
	}
	writeJSON(w, http.StatusOK, map[string]any{"cameras": out, "count": len(out)})
}

func (h *Handler) handleViewerRecordings(w http.ResponseWriter, r *http.Request) {
	s := h.viewerOf(r)
	if s == nil {
		WriteError(w, http.StatusForbidden, "viewer session required")
		return
	}
	filter := model.RecordingFilter{CameraID: r.URL.Query().Get("camera_id")}
	if filter.CameraID != "" && !s.CameraAllowed(filter.CameraID) {
		WriteError(w, http.StatusForbidden, "camera outside share scope")
		return
	}
	if v := r.URL.Query().Get("start"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.StartTime = t
		}
	}
	if v := r.URL.Query().Get("end"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.EndTime = t
		}
	}
	recordings, err := h.db.ListRecordings(r.Context(), filter)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list recordings")
		return
	}
	out := make([]model.Recording, 0, len(recordings))
	for _, rec := range recordings {
		if !s.CameraAllowed(rec.CameraID) {
			continue
		}
		out = append(out, rec)
	}
	writeJSON(w, http.StatusOK, map[string]any{"recordings": out, "count": len(out)})
}

func (h *Handler) handleViewerRecordingDownload(w http.ResponseWriter, r *http.Request) {
	s := h.viewerOf(r)
	if s == nil {
		WriteError(w, http.StatusForbidden, "viewer session required")
		return
	}
	id := chi.URLParam(r, "id")
	rec, err := h.db.GetRecording(r.Context(), id)
	if err != nil {
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}
	if !s.CameraAllowed(rec.CameraID) {
		// 统一 404：不泄漏录像存在性。
		WriteError(w, http.StatusNotFound, "recording not found")
		return
	}
	f, err := os.Open(rec.FilePath)
	if err != nil {
		WriteError(w, http.StatusNotFound, "file unavailable")
		return
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "stat failed")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, "", st.ModTime(), f)
}

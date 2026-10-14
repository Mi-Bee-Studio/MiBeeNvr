package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/viewer"
)

func testViewerMW(sess *viewer.Session) http.Handler {
	mw, _ := NewAuthMiddleware(AuthProvider{
		GetUsername: func() string { return "admin" },
		// 随便一个不合任何明文的 bcrypt 串：viewer 分支在 BasicAuth 之前，
		// 正常用例根本走不到密码比较。
		GetHash: func() string { return "$2a$10$N9qo8uLOickgx2ZMRZoMye" },
		VerifyViewer: func(token string) (*viewer.Session, bool) {
			if sess == nil {
				return nil, false
			}
			return sess, true
		},
	}, "", AuthRateLimitConfig{})
	return mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func viewerGet(mw http.Handler, path string) int {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+viewer.TokenPrefix+"abc")
	mw.ServeHTTP(rec, req)
	return rec.Code
}

func TestViewerSurface_AllowAndDeny(t *testing.T) {
	scoped := &viewer.Session{AccountID: "bob", Cameras: []string{"cam-1"}, ExpiresAt: time.Now().Add(time.Hour)}
	mw := testViewerMW(scoped)

	cases := []struct {
		path string
		want int
	}{
		// 只读面：观看者 API + 直播/快照/事件 + 单机详情。
		{"/api/viewer/cameras", 200},
		{"/api/viewer/recordings", 200},
		{"/api/cameras/cam-1/stream.flv", 200},
		{"/api/cameras/cam-1/stream/ws", 200},
		{"/api/cameras/cam-1/stream/sub/index.m3u8", 200},
		{"/api/cameras/cam-1/snapshot", 200},
		{"/api/cameras/cam-1", 200},
		{"/api/cameras/cam-1/protocols", 200},
		// 摄像头范围外。
		{"/api/cameras/cam-2/stream.flv", 403},
		{"/api/cameras/cam-2/snapshot", 403},
		// 配置/变更/越界面。
		{"/api/cameras/cam-1", 200}, // GET 单机详情可读（上一行同路径）
		{"/api/settings", 403},
		{"/api/recordings/r1/download", 403},
		{"/api/cameras/cam-1/rediscover", 403},
		{"/api/cameras/cam-1/ptz/presets", 403}, // PTZ=控制，观看者禁
		// base-path 部署形态（前缀剥离）。
		{"/nvr/api/viewer/cameras", 200},
	}
	// PUT/POST 一律拒。
	for _, tc := range cases {
		if got := viewerGet(mw, tc.path); got != tc.want {
			t.Errorf("GET %s = %d, want %d", tc.path, got, tc.want)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/cameras/cam-1/stop", nil)
	req.Header.Set("Authorization", "Bearer "+viewer.TokenPrefix+"abc")
	mw.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("POST mutation = %d, want 403", rec.Code)
	}

	// 整机范围（cameras 空）放行任意摄像头。
	whole := &viewer.Session{AccountID: "carol", ExpiresAt: time.Now().Add(time.Hour)}
	mw2 := testViewerMW(whole)
	if got := viewerGet(mw2, "/api/cameras/any-cam/stream.flv"); got != 200 {
		t.Errorf("whole-device scope cam = %d, want 200", got)
	}

	// 无效 token 回退 401（BasicAuth 缺失）。
	mw3 := testViewerMW(nil)
	if got := viewerGet(mw3, "/api/viewer/cameras"); got != 401 {
		t.Errorf("invalid viewer token = %d, want 401", got)
	}
}

func TestViewerStoreLifecycle(t *testing.T) {
	st := viewer.NewStore()
	now := time.Now()
	tok, sess, err := st.Login(viewer.Scope{AccountID: "bob", Cameras: []string{"cam-1"}}, now)
	if err != nil || tok == "" || !sess.CameraAllowed("cam-1") || sess.CameraAllowed("cam-2") {
		t.Fatalf("login/camera scope: %v %+v", err, sess)
	}
	if _, ok := st.Verify(tok, now.Add(3*time.Hour)); ok {
		t.Fatal("expired session must not verify")
	}
	if _, ok := st.Verify("mbviewer_nope", now); ok {
		t.Fatal("unknown token must fail")
	}
}

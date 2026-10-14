package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// 预投产调试上报（2026-10-07，用户需求：解决 10 公里外真机调试沟通）。
// App 侧 DiagLog 通过本端点把结构化事件流送到 NVR 落盘——开发者直接
// SSH 到 NVR 读文件即可复盘现场（链路死亡序列/网络切换时刻/API 失败
// 明细），不再依赖用户口述截图。
//
// POST /api/diagnostics/app（会话/设备 token 认证，authed 组内）
//   body: {boot_id, app_version, package_kind, platform, meta, events[]}
//
// 存储：<storage-root>/diag/app/<safeID>_<unixms>.json，保留最近 50 份。
// 不进 DB（一次性调试数据，文件即档）；journal INFO 一行留索引。

// diagMaxBody 限制上报体积（App 端环形缓冲 2000 条 ~ 数百 KB）。
const diagMaxBody = 1 << 20 // 1 MiB

// diagKeepFiles 每设备保留的最近上报份数。
const diagKeepFiles = 50

var diagIDSanitize = regexp.MustCompile(`[^a-zA-Z0-9_.-]`)

// diagVerSanitize 版本串文件名化（保住 0.1.3-debug+6108 原貌，其余
// 字符折成 -）。版本进文件名（用户 2026-10-08 需求）：ls 一眼分辦
// 报告来自哪个 App 版本，旧版本上报不再伪装成新版本的问题。
var diagVerSanitize = regexp.MustCompile(`[^a-zA-Z0-9.+-]`)

type diagAppReport struct {
	BootID      string            `json:"boot_id"`
	AppVersion  string            `json:"app_version"`
	PackageKind string            `json:"package_kind"`
	Platform    string            `json:"platform"`
	Meta        map[string]string `json:"meta"`
	Events      []struct {
		T    string            `json:"t"`
		Cat  string            `json:"cat"`
		Lvl  string            `json:"lvl"`
		Msg  string            `json:"msg"`
		Attr map[string]string `json:"attrs"`
	} `json:"events"`
}

func (h *Handler) handleAppDiagnosticsReport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, diagMaxBody)
	var rep diagAppReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid report body: "+err.Error())
		return
	}
	if len(rep.Events) == 0 {
		WriteError(w, http.StatusBadRequest, "empty events")
		return
	}

	id := diagIDSanitize.ReplaceAllString(rep.BootID, "_")
	if id == "" {
		id = "unknown"
	}
	dir := filepath.Join(h.store.RootDir(), "diag", "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		WriteError(w, http.StatusInternalServerError, "mkdir failed")
		return
	}
	// 文件名：<bootId>_<version>_<ts>.json——首段（首个 _ 前）仍是
	// bootId，pruneDiagReports 的按设备分组语义不变。
	ver := diagVerSanitize.ReplaceAllString(rep.AppVersion, "-")
	if ver == "" {
		ver = "unknown-ver"
	}
	name := filepath.Join(dir, id+"_"+ver+"_"+time.Now().Format("20060102-150405.000")+".json")
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "marshal failed")
		return
	}
	if err := os.WriteFile(name, raw, 0o644); err != nil {
		WriteError(w, http.StatusInternalServerError, "write failed")
		return
	}
	h.pruneDiagReports(dir)

	logger.Info("app diagnostics report stored",
		"file", filepath.Base(name),
		"boot_id", rep.BootID,
		"app_version", rep.AppVersion,
		"package", rep.PackageKind,
		"events", len(rep.Events),
	)
	writeJSON(w, http.StatusOK, map[string]any{
		"stored": filepath.Base(name),
		"events": len(rep.Events),
	})
}

// pruneDiagReports 按文件名前缀（boot id）分组各保最近 diagKeepFiles 份；
// 上限兜底全目录 200 份（多设备共存的防爆仓）。
func (h *Handler) pruneDiagReports(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return
	}
	byPrefix := map[string][]string{}
	var all []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		prefix := e.Name()
		if i := strings.IndexByte(prefix, '_'); i > 0 {
			prefix = prefix[:i]
		}
		byPrefix[prefix] = append(byPrefix[prefix], e.Name())
		all = append(all, e.Name())
	}
	remove := func(names []string, keep int) {
		if len(names) <= keep {
			return
		}
		sort.Strings(names) // 时间戳字典序即时间序
		for _, n := range names[:len(names)-keep] {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
	for _, names := range byPrefix {
		remove(names, diagKeepFiles)
	}
	if len(all) > 200 {
		sort.Strings(all)
		for _, n := range all[:len(all)-200] {
			_ = os.Remove(filepath.Join(dir, n))
		}
	}
}

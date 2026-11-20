package systemapi

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

func (s *server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if s.deps.DB == nil {
		WriteError(w, http.StatusInternalServerError, "database not available")
		return
	}
	backupDir := filepath.Join(filepath.Dir(s.deps.ConfigPath), "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to create backup directory")
		return
	}
	filename := fmt.Sprintf("nvr-backup-%s.db", time.Now().Format("20060102-150405"))
	destPath := filepath.Join(backupDir, filename)
	if err := s.deps.DB.Backup(r.Context(), destPath); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to create backup")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "created", "file": filename})
}

func (s *server) handleListBackups(w http.ResponseWriter, r *http.Request) {
	backupDir := filepath.Join(filepath.Dir(s.deps.ConfigPath), "backups")
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	var backups []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".db") {
			backups = append(backups, e.Name())
		}
	}
	if backups == nil {
		backups = []string{}
	}
	writeJSON(w, http.StatusOK, backups)
}

// protocolInfo describes a protocol for the /api/protocols endpoint.

// formatUptime converts a duration to a human-readable string like "2h 15m 30s".
func formatUptime(d time.Duration) string {
	rounded := d.Round(time.Second)
	h := rounded / time.Hour
	rounded -= h * time.Hour
	m := rounded / time.Minute
	rounded -= m * time.Minute
	s := rounded / time.Second
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

// --- System stats helpers (Linux /proc) ---

// readCPURaw 读取 /proc/stat 聚合 cpu 行的累计 jiffies:total=全部计数器之和,
// idle=第 4 计数器,iowait=第 5 计数器(客户端两次轮询差分得出 iowait%)。
func readCPURaw() (total, idle, iowait uint64, err error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, 0, err
	}
	return parseProcStat(data)
}

// parseProcStat 是 readCPURaw 的纯函数内核(可测)。
func parseProcStat(data []byte) (total, idle, iowait uint64, err error) {
	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return 0, 0, 0, fmt.Errorf("empty /proc/stat")
	}
	fields := strings.Fields(lines[0])
	if len(fields) < 5 {
		return 0, 0, 0, fmt.Errorf("unexpected /proc/stat format")
	}
	for i := 1; i < len(fields); i++ {
		v, _ := strconv.ParseUint(fields[i], 10, 64)
		total += v
	}
	idle, _ = strconv.ParseUint(fields[4], 10, 64)
	if len(fields) > 5 {
		iowait, _ = strconv.ParseUint(fields[5], 10, 64)
	}
	return
}

// readLoadAvg 读取 /proc/loadavg 的 1/5/15 分钟负载。
func readLoadAvg() (one, five, fifteen float64, err error) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0, err
	}
	return parseLoadAvg(data)
}

// parseLoadAvg 是 readLoadAvg 的纯函数内核(可测)。
func parseLoadAvg(data []byte) (one, five, fifteen float64, err error) {
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return 0, 0, 0, fmt.Errorf("unexpected /proc/loadavg format")
	}
	if one, err = strconv.ParseFloat(fields[0], 64); err != nil {
		return 0, 0, 0, err
	}
	if five, err = strconv.ParseFloat(fields[1], 64); err != nil {
		return 0, 0, 0, err
	}
	if fifteen, err = strconv.ParseFloat(fields[2], 64); err != nil {
		return 0, 0, 0, err
	}
	return
}

func readMemoryInfo() (total, available uint64, err error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		v, _ := strconv.ParseUint(fields[1], 10, 64)
		switch fields[0] {
		case "MemTotal:":
			total = v * 1024
		case "MemAvailable:":
			available = v * 1024
		}
	}
	return
}

func readNetworkInfo() (bytesSent, bytesRecv uint64, err error) {
	data, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return 0, 0, err
	}
	// Try eth0 or wlan0 first
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "eth0:") && !strings.HasPrefix(trimmed, "wlan0:") {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) < 2 {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 10 {
			continue
		}
		bytesRecv, _ = strconv.ParseUint(fields[0], 10, 64)
		bytesSent, _ = strconv.ParseUint(fields[8], 10, 64)
		return
	}
	// Fallback: sum all interfaces
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, ":") {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) < 2 {
			continue
		}
		fields := strings.Fields(parts[1])
		if len(fields) < 10 {
			continue
		}
		r, _ := strconv.ParseUint(fields[0], 10, 64)
		s, _ := strconv.ParseUint(fields[8], 10, 64)
		bytesRecv += r
		bytesSent += s
	}
	return
}

func readProcessRSS() uint64 {
	data, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0
	}
	rssPages, _ := strconv.ParseUint(fields[1], 10, 64)
	return rssPages * uint64(os.Getpagesize())
}

func (s *server) handleSystemStats(w http.ResponseWriter, r *http.Request) {
	cpuTotal, cpuIdle, cpuIowait, _ := readCPURaw()
	memTotal, memAvailable, _ := readMemoryInfo()
	netSent, netRecv, _ := readNetworkInfo()
	processRSS := readProcessRSS()

	resp := SystemStats{
		CPU:       CPUStats{Total: cpuTotal, Idle: cpuIdle, Iowait: cpuIowait},
		Memory:    MemoryStats{Total: memTotal, Available: memAvailable, ProcessRSS: processRSS},
		Network:   NetworkStats{BytesSent: netSent, BytesRecv: netRecv},
		Uptime:    formatUptime(time.Since(s.upSince())),
		Timestamp: time.Now().Unix(),
	}

	if one, five, fifteen, err := readLoadAvg(); err == nil {
		resp.Load = &LoadStats{One: one, Five: five, Fifteen: fifteen}
	}

	root := "/"
	watermark := 90.0
	if s.deps.Config != nil {
		if s.deps.Config.Storage.RootDir != "" {
			root = s.deps.Config.Storage.RootDir
		}
		if p := s.deps.Config.Cleanup.DiskThresholdPercent; p > 0 {
			watermark = float64(p)
		}
	}
	if total, free, ok := diskUsageFor(root); ok && total > 0 {
		usedPct := (1 - float64(free)/float64(total)) * 100
		status := "ok"
		if usedPct >= watermark {
			status = "high"
		}
		resp.Disk = &DiskStats{
			Path: root, TotalBytes: total, FreeBytes: free,
			UsedPct: usedPct, WatermarkPct: watermark, Status: status,
		}
	} else {
		resp.Disk = &DiskStats{Path: root, WatermarkPct: watermark, Status: "unknown"}
	}

	resp.DB = s.dbTxnStats()

	writeJSON(w, http.StatusOK, resp)
}

// dbTxnPrev samples the previous /api/system/stats poll for rate math.
type dbTxnSample struct {
	at     time.Time
	counts map[string]int64
}

// dbTxnStats derives the per-source DB write panel from storage's cumulative
// counters (#759): totals since start + rates over the poll window (two
// samples minimum). Handler-field state, single-writer per HTTP poll.
func (s *server) dbTxnStats() *DBTxnStats {
	counts, nanos := storage.TxnSnapshot()
	now := time.Now()

	s.dbTxnMu.Lock()
	var prev dbTxnSample
	rates := map[string]float64{}
	if s.dbTxnPrev != nil {
		prev = *s.dbTxnPrev
		if dt := now.Sub(prev.at).Seconds(); dt > 0.5 {
			for src, c := range counts {
				if before, ok := prev.counts[src]; ok {
					rates[src] = float64(c-before) / dt
				}
			}
			s.dbTxnPrev = &dbTxnSample{at: now, counts: counts}
		}
	} else {
		s.dbTxnPrev = &dbTxnSample{at: now, counts: counts}
	}
	s.dbTxnMu.Unlock()

	_ = prev
	out := &DBTxnStats{Sources: make([]DBTxnSource, 0, len(counts))}
	for src, c := range counts {
		if c == 0 {
			continue
		}
		avgMs := 0.0
		if nanos[src] > 0 {
			avgMs = float64(nanos[src]/int64(time.Millisecond)) / float64(c)
		}
		out.Sources = append(out.Sources, DBTxnSource{
			Name: src, Total: c, PerSecond: rates[src], AvgMs: avgMs,
		})
	}
	sort.Slice(out.Sources, func(i, j int) bool { return out.Sources[i].Total > out.Sources[j].Total })
	if len(out.Sources) == 0 {
		return nil
	}
	return out
}

// SystemStats is the response from /api/stats/system.
type SystemStats struct {
	CPU       CPUStats     `json:"cpu"`
	Memory    MemoryStats  `json:"memory"`
	Network   NetworkStats `json:"network"`
	Uptime    string       `json:"uptime"`
	Timestamp int64        `json:"timestamp"`
	// Load / Disk 补齐饱和观测(2026-09-11 M5 IO 饱和事故:73% iowait + 负载
	// 15 在既有 CPU 瓦片上完全不可见——CPU 忙闲是低的)。nil = 不可用平台。
	Load *LoadStats `json:"load,omitempty"`
	Disk *DiskStats `json:"disk,omitempty"`
	// DB 写事务来源观测(#759):每来源累计数 + 每秒速率(服务端按两次采样
	// 窗口计算)。nil = 尚无第二个采样窗口。
	DB *DBTxnStats `json:"db,omitempty"`
}

// DBTxnStats 是按来源聚合的 DB 写事务面板数据(#759)。
type DBTxnStats struct {
	// Sources 按累计数降序(写热点排序一眼可见)。
	Sources []DBTxnSource `json:"sources"`
}

type DBTxnSource struct {
	Name      string  `json:"name"`
	Total     int64   `json:"total"`
	PerSecond float64 `json:"per_second"`
	AvgMs     float64 `json:"avg_ms"`
}

type CPUStats struct {
	Total  uint64 `json:"total"`  // cumulative total jiffies
	Idle   uint64 `json:"idle"`   // cumulative idle jiffies
	Iowait uint64 `json:"iowait"` // cumulative iowait jiffies (delta between polls = iowait%)
}

// LoadStats 是 /proc/loadavg 快照(1/5/15 分钟)。
type LoadStats struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}

// DiskStats 是录像根的容量水位。WatermarkPct 取 cleanup.disk_threshold_percent
// (清理已在执行的阈值),used_pct 越线即 status=high——录满盘前的可见预警。
type DiskStats struct {
	Path         string  `json:"path"`
	TotalBytes   uint64  `json:"total_bytes"`
	FreeBytes    uint64  `json:"free_bytes"`
	UsedPct      float64 `json:"used_pct"`
	WatermarkPct float64 `json:"watermark_pct"`
	Status       string  `json:"status"` // ok | high | unknown
}

type MemoryStats struct {
	Total      uint64 `json:"total"`       // MemTotal bytes
	Available  uint64 `json:"available"`   // MemAvailable bytes
	ProcessRSS uint64 `json:"process_rss"` // NVR process RSS bytes
}

type NetworkStats struct {
	BytesSent uint64 `json:"bytes_sent"`
	BytesRecv uint64 `json:"bytes_recv"`
}

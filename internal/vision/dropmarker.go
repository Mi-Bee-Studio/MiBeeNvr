package vision

// dropmarker.go (#671): 解读消费者心跳 v2 的批量丢弃报告,把受影响的录像
// 标记为 ai_status='skipped'(ai_error 携带原因)。被丢弃的段同时被排除在
// 离线补偿重推之外(ListRecordingsForVisionRepush 只取 ''/pending/processing)
// ——不会把消费者刚刚丢掉的东西再灌回去。

import (
	"context"
	"log/slog"
	"strings"
	"time"
)

// DropMarkerDB abstracts the recording-marking queries used by ApplyDrops.
// Production wiring passes *storage.DB; tests use fakes.
type DropMarkerDB interface {
	MarkRecordingsSkippedByIDs(ctx context.Context, ids []string, aiErr string) (int64, error)
	MarkRecordingsSkippedByRange(ctx context.Context, cameraID string, from, to time.Time, aiErr string) (int64, error)
}

// stripSubLayerSuffix 去掉子码流推送在录像 ID 后拼接的 "#<纳秒>" 后缀
// (#514 的 joinRecordingID),与 AI 事件回写路径的剥壳规则一致。
func stripSubLayerSuffix(id string) string {
	if i := strings.LastIndexByte(id, '#'); i > 0 {
		suffix := id[i+1:]
		if suffix != "" && isAllDigits(suffix) {
			return id[:i]
		}
	}
	return id
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sanitizeReason 清洗外部上报的丢弃原因:截取开头连续的 [a-z0-9_] 小写段,
// 最长 32 字符,空则 unknown。该值会写进 ai_error 展示给用户。
func sanitizeReason(reason string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(reason) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			break
		}
	}
	out := b.String()
	if len(out) > 32 {
		out = out[:32]
	}
	if out == "" {
		out = "unknown"
	}
	return out
}

// ApplyDrops 按报告逐个范围标记录像,返回累计标记行数。
//
// 规则:
//   - 范围带 ids → 精确按 ID 标记(id 先剥 "#nano" 子层后缀、去重)——
//     时间字段此时仅作参考,解析失败不影响精确标记。
//   - 范围无 ids → 相机 + [from,to] 时间窗批量标记;时间或相机不可用则
//     跳过该范围(记 Warn,不让一条坏数据拖垮整份报告)。
//   - 标记永远不覆盖终态(completed/failed/skipped),由 DB 层的状态卫兵保证。
//
// db 为 nil(测试/降级部署)时直接返回 0。
func ApplyDrops(ctx context.Context, db DropMarkerDB, drops *VisionDrops) int64 {
	if db == nil || drops == nil {
		return 0
	}
	var total int64
	for _, r := range drops.Ranges {
		errMsg := "vision drop:" + sanitizeReason(r.Reason)

		if ids := normalizeIDs(r.IDs); len(ids) > 0 {
			n, err := db.MarkRecordingsSkippedByIDs(ctx, ids, errMsg)
			if err != nil {
				slog.Warn("vision drop report: mark by ids failed", "error", err, "count", len(ids))
				continue
			}
			total += n
			continue
		}

		from, errFrom := time.Parse(time.RFC3339, r.From)
		to, errTo := time.Parse(time.RFC3339, r.To)
		if errFrom != nil || errTo != nil || r.CameraID == "" || to.Before(from) {
			slog.Warn("vision drop report: unusable range skipped",
				"camera_id", r.CameraID, "reason", r.Reason, "count", r.Count)
			continue
		}
		n, err := db.MarkRecordingsSkippedByRange(ctx, r.CameraID, from, to, errMsg)
		if err != nil {
			slog.Warn("vision drop report: mark by range failed", "error", err, "camera_id", r.CameraID)
			continue
		}
		total += n
	}
	return total
}

// normalizeIDs 剥子层后缀 + 去重,保持首次出现顺序。
func normalizeIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		norm := stripSubLayerSuffix(id)
		if _, dup := seen[norm]; dup {
			continue
		}
		seen[norm] = struct{}{}
		out = append(out, norm)
	}
	return out
}

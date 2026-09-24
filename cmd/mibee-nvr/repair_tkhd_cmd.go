package main

// repair append-tkhd —— 2026-09-24 现场回归（#853 追加桶）的历史产物修复：
// 2026-09-19 20:5x 至修复之间，追加桶合并输出的 moov 模板把视频轨的 stsd
// 样本条目与 tkhd 宽高都写成 0×0。Chromium 一族构建流配置读 stsd 样本条目
// 尺寸 → "no supported streams" 整文件拒播（网页回放全灭）；ffprobe/VLC
// 解码时从 SPS 重推尺寸照常可播（下载可播掩盖网页不可播）。
//
// 尺寸可从文件自身 avcC/hvcC 携带的 SPS 无损还原：本命令就地改写 stsd
// 样本条目的 u16 宽高与 tkhd 末尾 8 字节（16.16 定点宽高），不触碰任何
// 采样数据，秒级完成、无 IO 压力。幂等——非零字段不再改写，可安全重跑。
//
// 安全：
//   - 只处理「宽高为 0 且 SPS 可解析出合理尺寸」的文件，其余原样跳过；
//   - --dry-run 默认（只检测报告），--execute 应用；
//   - --camera 限定相机，--limit 限制数量。

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/merge"
)

func runRepairAppendTkhd() int {
	opts := parseRepairFlags(3)
	if opts.configPath == "__help__" {
		printRepairAppendTkhdUsage()
		return 0
	}

	cfg, err := config.Load(opts.configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Error: config validation: %v\n", err)
		return 1
	}

	mode := "DRY RUN (no changes)"
	if !opts.dryRun {
		mode = "EXECUTE"
	}
	fmt.Printf("repair append-tkhd — %s\n", mode)
	if opts.cameraID != "" {
		fmt.Printf("  camera: %s\n", opts.cameraID)
	} else {
		fmt.Println("  camera: (all cameras)")
	}
	if opts.limit > 0 {
		fmt.Printf("  limit:  %d files\n", opts.limit)
	}
	fmt.Println()

	rootDir := cfg.Storage.RootDir
	var cameras []string
	if opts.cameraID != "" {
		cameras = []string{opts.cameraID}
	} else {
		entries, err := os.ReadDir(rootDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading storage root %q: %v\n", rootDir, err)
			return 1
		}
		for _, e := range entries {
			if e.IsDir() {
				cameras = append(cameras, e.Name())
			}
		}
	}

	scanned, broken, patched, failed := 0, 0, 0, 0
	for _, cam := range cameras {
		if opts.limit > 0 && broken >= opts.limit {
			break
		}
		camRoot := filepath.Join(rootDir, cam)
		walkErr := filepath.WalkDir(camRoot, func(path string, d os.DirEntry, werr error) error {
			if werr != nil {
				return nil // tolerate per-entry FS noise, keep walking
			}
			if d.IsDir() {
				// 只下钻小时树（YYYYMM/DD/HH 均为纯数字命名）——MJPEG
				// 相机目录下有百万级帧文件树，全量递归会走几十分钟
				// （#745 深扫同坑）。合并产物只存在于小时树。
				if d.Name() != cam && !allDigits(d.Name()) {
					return fs.SkipDir
				}
				return nil
			}
			if opts.limit > 0 && broken >= opts.limit {
				return filepath.SkipAll
			}
			if !strings.HasSuffix(path, ".mp4") {
				return nil
			}
			scanned++
			vt, err := merge.ProbeVideoTrack(path)
			if err != nil {
				return nil
			}
			tkhdZero := vt.Width == 0 || vt.Height == 0
			seZero := vt.SampleEntryWidth == 0 || vt.SampleEntryHeight == 0
			if !tkhdZero && !seZero {
				return nil
			}
			broken++
			if opts.dryRun {
				which := "tkhd+stsd"
				switch {
				case tkhdZero && !seZero:
					which = "tkhd"
				case !tkhdZero && seZero:
					which = "stsd"
				}
				fmt.Printf("  [would fix] %s (%s)\n", path, which)
				return nil
			}
			tk, se, w, h, rerr := merge.RepairZeroDimensions(path)
			if rerr != nil {
				failed++
				fmt.Printf("  [FAILED]   %s: %v\n", path, rerr)
				return nil
			}
			if tk || se {
				patched++
				fmt.Printf("  [fixed]    %s → %d×%d (%s)\n", path, w, h, repairedWhich(tk, se))
			}
			return nil
		})
		if walkErr != nil {
			fmt.Fprintf(os.Stderr, "  WARN: walk %s: %v\n", camRoot, walkErr)
		}
	}

	fmt.Printf("\nscanned %d mp4 files: %d with zero dims (tkhd/stsd), %d patched, %d failed\n",
		scanned, broken, patched, failed)
	if opts.dryRun && broken > 0 {
		fmt.Println("dry run — re-run with --execute to patch in place")
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func repairedWhich(tkhd, se bool) string {
	switch {
	case tkhd && se:
		return "tkhd+stsd"
	case tkhd:
		return "tkhd"
	case se:
		return "stsd"
	}
	return "none"
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func printRepairAppendTkhdUsage() {
	fmt.Print(`Usage: mibee-nvr repair append-tkhd [options]

Bulk-repair merged MP4 outputs whose video dimensions are 0×0 in the stsd
sample entry and/or tkhd (append-bucket outputs written between 2026-09-19
and the fix). Chromium-family browsers refuse to play such files ("no
supported streams"); dimensions are recovered losslessly from the file's own
SPS and written in place — sample data is untouched.

Options:
  --camera ID   Limit to one camera (default: all cameras)
  --limit N     Stop after N broken files
  --execute     Apply repairs (default: dry run)
  --config PATH Config path (default: mibee-nvr.yaml)
`)
}

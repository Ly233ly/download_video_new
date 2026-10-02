package media

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// dirExists 判断目录是否已存在（用于区分"我建的空目录"与"本来就有的目录"）。
func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// isWithin 判断 target 是否位于 base 之内（含 base 本身）。
//
// 用于两条边界：临时产物必须留在计划目录内（[05 §11] 的归属要求），
// 以及"只删自己造的目录"。相对路径比较而非字符串前缀：
// 前缀比较会把 `C:\tmp\ab` 误判成 `C:\tmp\a` 的子路径。
func isWithin(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// removeOwnedFile 删除一个**由本包本次创建**的文件；不存在视为成功。
//
// 归属由调用点保证：本包只会对 `临时\<plan_id>\` 里的路径调用它
// （[05 §1]：无法证明归属的文件永不删除；B-402/B-404）。
func removeOwnedFile(path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("清理临时产物失败",
			"component", "download", "event", "temp_remove_failed",
			"path", path, "cause", err.Error())
	}
}

// removeEmptyDir 只在目录**确实为空**时删除它（[05 §11]："计划结束后清理自己创建的空目录"）。
//
// `MkdirAll` 会连带创建多级目录，所以清理时要沿着路径向上回溯，
// 但**只能回溯到本次创建的那一级**——再往上可能是数据目录本身。
func removeEmptyDir(path, stopAt string) {
	for {
		if path == "" || path == stopAt || !isWithin(stopAt, path) {
			return
		}
		if err := os.Remove(path); err != nil {
			// 非空（ENOTEMPTY）是正常情况，不记日志；其余失败只在 Debug 留痕。
			if !errors.Is(err, fs.ErrNotExist) {
				slog.Debug("保留非空的临时目录",
					"component", "download", "event", "temp_dir_kept",
					"path", path, "cause", err.Error())
			}
			return
		}
		path = filepath.Dir(path)
	}
}

// cleanupAfterFailure 判定本次失败该删什么（[05 §1]、[05 §4.1]、[05 §10]、[05 §11]）。
//
// 三种情况必须清理临时产物，理由是它们**都不构成可续传的中间状态**：
//   - `disk_full`：字节写不进去，且 [05 §4.1] 明确要求"清理本次临时产物"；
//   - 调用方取消：用户已停止，留着的分片不再有归属方（[05 §10]）；
//   - 校验不通过：字节已被证明是坏的，留着会在下次续传时被当成"已有分片"复用。
//
// 其余失败（网络中断等）**保留分片**：它正是 [05 §4.1] 断点续传的起点。
func (d *Downloader) cleanupAfterFailure(req Request, dirs workDirs, cause error, planDirExisted bool) {
	if dirs.staging == "" {
		return
	}

	code, hasCode := CodeOf(cause)
	removeTemp := errors.Is(cause, context.Canceled) ||
		errors.Is(cause, context.DeadlineExceeded) ||
		(hasCode && (code == CodeDiskFull ||
			code == CodeOutputNoStreams ||
			code == CodeOutputNoVideo ||
			code == CodeOutputNoAudio ||
			code == CodeOutputDurationMismatch))

	if !removeTemp {
		return
	}

	// 清理前再确认一次归属：只有落在计划目录里的分片才会被删（[05 §1]）。
	if isWithin(dirs.planDir, dirs.staging) {
		removeOwnedFile(dirs.staging)
	}
	if !planDirExisted {
		removeEmptyDir(dirs.planDir, filepath.Dir(dirs.planDir))
	}
}

// isDiskFull 判断错误是否为"磁盘满"。
//
// 只认 `ENOSPC` 这个具体错误：磁盘满必须映射到独立的 `disk_full` 码
// 且**不自动重试**（[05 §4.1]），所以判定不能宽泛到把别的 I/O 错误也吞进来。
func isDiskFull(err error) bool {
	return err != nil && errors.Is(err, syscall.ENOSPC)
}

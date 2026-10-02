package media

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// delivery 是一次成功交付的结果。
type delivery struct {
	Path  string
	Bytes int64
}

// deliverFile 把校验通过的临时产物**原子地**落到「已完成」目录（[05 §11]、B-406）。
//
// 流程刻意分成"预留名字"与"搬进去"两步：
//
//  1. 用 `O_CREATE|O_EXCL` **原子创建**目标文件，成功即表示这个名字归我们；
//  2. 再把临时文件重命名到这个名字上。
//
// 不允许"先 `Stat` 判断不存在、再覆盖写"——那中间有一个窗口，
// 并发的另一个计划（或用户手动放进去的同名文件）会被无声覆盖，
// 而这个目录里的文件**永不移动、删除或修改**（B-401/B-406）。
// 冲突时按 `名字 (1).ext`、`名字 (2).ext` … 生成唯一名，
// 超过 [MaxUniqueNameAttempts] 次报 `output_name_exhausted`（[05 §11]）。
//
// **绝不触碰**用户文件与 IDM 文件（[05 §1]、B-401）：本函数只搬
// 自己刚校验过的那一个临时文件。
func deliverFile(outputDir, outputName, staging string, expectedBytes int64) (delivery, error) {
	if strings.TrimSpace(outputName) == "" {
		return delivery{}, ErrorOf(CodeOutputNameExhausted, errors.New("输出文件名为空"))
	}
	// OutputName 按约定已由 [03 §4.4] 清洗过，这里再兜一道：
	// 带分隔符的名字会写到「已完成」之外，那是最不该出的一类 bug。
	if strings.ContainsAny(outputName, `\/`) || filepath.Base(outputName) != outputName {
		return delivery{}, ErrorOf(CodeOutputNameExhausted, errors.New("输出文件名含路径分隔符"))
	}

	// 先确认长度与校验时的观测一致，再动「已完成」目录：
	// 不一致说明落盘过程本身出了问题，此时不应在交付目录留下任何痕迹。
	size, err := fileSizeOf(staging)
	if err != nil {
		return delivery{}, ErrorOf(CodeDownloadFailed, err)
	}
	if expectedBytes > 0 && size != expectedBytes {
		return delivery{}, ErrorOf(CodeDownloadFailed,
			fmt.Errorf("临时产物长度与已校验长度不符：%d ≠ %d", size, expectedBytes))
	}

	for attempt := 0; attempt < MaxUniqueNameAttempts; attempt++ {
		finalPath := filepath.Join(outputDir, uniqueCandidate(outputName, attempt))

		reserved, err := reservePath(finalPath)
		if err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue // 名字被占：换下一个唯一名（B-406）
			}
			// 目录不存在、权限不足等：报错**而不是**自建目录。
			// 「已完成」目录由上层按 [05 §11] 的布局创建；在这里顺手
			// MkdirAll 会把"目录配错"掩盖成"下载成功"。
			return delivery{}, ErrorOf(CodeDownloadFailed, fmt.Errorf("预留输出文件失败: %w", err))
		}
		if closeErr := reserved.Close(); closeErr != nil {
			removeOwnedFile(finalPath)
			return delivery{}, ErrorOf(CodeDownloadFailed, fmt.Errorf("预留输出文件失败: %w", closeErr))
		}

		if renameErr := os.Rename(staging, finalPath); renameErr != nil {
			// 预留之后到改名之间仍可能被抢占（另一个实例同时在跑同一计划）。
			// 这时删掉**自己刚建的空占位文件**再换名字，绝不覆盖别人（B-406）。
			removeOwnedFile(finalPath)
			if isExistingTarget(renameErr) {
				continue
			}
			return delivery{}, ErrorOf(CodeDownloadFailed, fmt.Errorf("交付文件失败: %w", renameErr))
		}

		slog.Info("文件已交付",
			"component", "download", "event", "delivered", "path", finalPath, "bytes", size)
		return delivery{Path: finalPath, Bytes: size}, nil
	}

	return delivery{}, ErrorOf(CodeOutputNameExhausted,
		fmt.Errorf("同名冲突超过 %d 次仍无法生成唯一文件名", MaxUniqueNameAttempts))
}

// uniqueCandidate 生成第 attempt 个候选名：第 0 个就是原名。
//
// 序号插在扩展名之前，保住容器后缀——后缀是播放器识别格式的依据。
// 以 `.` 开头的名字（`.hidden`）没有扩展名：`filepath.Ext` 会把整个名字当扩展名，
// 那样生成的 `" (2).hidden"` 会丢掉原名的字面部分。
func uniqueCandidate(name string, attempt int) string {
	if attempt <= 0 {
		return name
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	if base == "" {
		// 全是扩展名的名字（`.hidden`）：序号追加在末尾，不制造空基名。
		return name + " (" + strconv.Itoa(attempt) + ")"
	}
	return base + " (" + strconv.Itoa(attempt) + ")" + ext
}

// reservePath 以 `O_CREATE|O_EXCL` 原子创建空文件，占住这个名字。
func reservePath(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
}

// isExistingTarget 判断改名失败的原因是否为"目标已被占用"。
//
// 只用于决定"换个名字重试"：判错的代价仅是一次多余的重试，
// **绝不会**变成覆盖别人文件的理由——所以这里的判定可以宽松。
// Windows 上 `os.Rename` 遇到已存在的目标返回 `ERROR_ALREADY_EXISTS`，
// 而 Go 在不同路径上未必都归一成 `fs.ErrExist`，故同时认系统文本特征。
func isExistingTarget(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, fs.ErrExist) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "already exists")
}

// fileSizeOf 返回文件长度。
func fileSizeOf(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("读取临时产物信息失败: %w", err)
	}
	if info.IsDir() {
		return 0, errors.New("临时产物路径指向目录")
	}
	return info.Size(), nil
}

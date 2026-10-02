package media

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// workDirs 是本次执行用到的绝对目录。
//
// 全部规范化成**绝对路径 + 无尾分隔符**：后面的归属判定（[isWithin]）、
// 以及"清理自己造出来的空目录"都依赖这个不变式；
// 混进相对路径会让判定在进程工作目录变化时悄悄失效。
type workDirs struct {
	planDir   string // `临时\<plan_id>\`
	outputDir string // `已完成\`
	staging   string // 本次的临时产物
}

// resolveDirs 校验并规范化请求里的目录。
//
// 除了规范化，还要守住两条：[05 §11] 要求临时目录按任务归属
// （`临时\<plan_id>\`），所以 `plan_id` 必须**留在** `TempDir` 之内——
// 否则后续的"清理本次产物"就可能删到计划目录之外（[05 §1]：
// 无法证明归属的文件永不删除）。
func resolveDirs(req Request) (workDirs, error) {
	// P3 的地址在每条轨上（[Request.Tracks]），所以"没有 URL"不一定是缺地址。
	if strings.TrimSpace(req.URL) == "" && len(req.Tracks) == 0 {
		return workDirs{}, ErrorOf(CodeDownloadFailed, errors.New("缺少媒体地址"))
	}
	if strings.TrimSpace(req.TempDir) == "" || strings.TrimSpace(req.OutputDir) == "" {
		return workDirs{}, ErrorOf(CodeDownloadFailed, errors.New("缺少临时目录或输出目录"))
	}

	tempDir, err := absoluteDir(req.TempDir)
	if err != nil {
		return workDirs{}, ErrorOf(CodeDownloadFailed, err)
	}
	outputDir, err := absoluteDir(req.OutputDir)
	if err != nil {
		return workDirs{}, ErrorOf(CodeDownloadFailed, err)
	}

	planDir := tempDir
	if req.PlanID != "" {
		planDir = filepath.Join(tempDir, req.PlanID)
		if !isWithin(tempDir, planDir) {
			return workDirs{}, ErrorOf(CodeDownloadFailed, errors.New("计划标识不能逃出临时目录"))
		}
	}

	return workDirs{
		planDir:   planDir,
		outputDir: outputDir,
		staging:   filepath.Join(planDir, stagingName),
	}, nil
}

// absoluteDir 返回规范化的绝对目录路径。
func absoluteDir(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", errors.New("目录路径非法")
	}
	abs = strings.TrimRight(abs, `\/`)
	if abs == "" {
		return "", errors.New("目录路径非法")
	}
	return abs, nil
}

// existingBytes 返回已有分片的长度；不存在或不是普通文件时返回 0。
func existingBytes(path string) int64 {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return 0
	}
	return info.Size()
}

// streamTo 把响应体流式写入目标文件，期间按间隔回调进度。
//
// 返回的 `written` 是**写完之后分片应有的总长**（含续传起点），
// 而不是本次读到的字节数——调用方用它算百分比（[05 §4.1]）。
//
// **不整文件驻留内存**：固定大小的缓冲区循环搬运，内存占用与文件大小无关
// （[05 §4.4.2] 对 P6 的"不整文件驻留内存"是同一类约束，P1 同样适用）。
func streamTo(
	body io.Reader, dst io.Writer, resumeAt, total int64, interval time.Duration,
	onProgress func(Progress),
) (int64, error) {
	written := resumeAt
	last := time.Now()
	emit := func() {
		last = time.Now()
		reportProgress(onProgress, Progress{Downloaded: written, Total: total, Phase: PhaseDownloading})
	}
	// 首帧已在 downloadDirect 开头报过（那帧带"已有分片"的初值），
	// 所以这里从"距上次报告已过间隔"开始算，不重复报同一时刻的第二帧。
	throttled := func() {
		if time.Since(last) >= interval {
			emit()
		}
	}

	buf := make([]byte, downloadChunkSize)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, writeErr := dst.Write(buf[:n]); writeErr != nil {
				return written, writeErr
			}
			written += int64(n)
			throttled()
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return written, readErr
		}
	}

	// 末帧必须报：节流只约束中间帧，否则慢速连接上进度会长期停在旧值。
	emit()
	return written, nil
}

// parseContentRange 解析 `Content-Range: bytes <start>-<end>/<total>`。
//
// `total` 为 `*`（未知总长）时返回 0，调用方据此回到"长度未知"的进度语义
// （[05 §4.1]：无总长时不伪造百分比）。
func parseContentRange(value string) (start, total int64, ok bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "bytes ") {
		return 0, 0, false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(value, "bytes "))
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return 0, 0, false
	}
	rangePart, totalPart := strings.TrimSpace(rest[:slash]), strings.TrimSpace(rest[slash+1:])
	dash := strings.Index(rangePart, "-")
	if dash <= 0 {
		return 0, 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(rangePart[:dash]), 10, 64)
	if err != nil || start < 0 {
		return 0, 0, false
	}
	if totalPart == "*" {
		return start, 0, true
	}
	total, err = strconv.ParseInt(totalPart, 10, 64)
	if err != nil || total < 0 {
		return 0, 0, false
	}
	return start, total, true
}

// statusCause 把 HTTP 状态码映射成**不含 URL** 的内部原因，供本地日志排错。
//
// [05 §8] 要求"失败必须显示档位与 HTTP/网络摘要（不含秘密）"——
// 面向用户的摘要在服务层拼装，本包只提供错误码与这条安全的原因。
func statusCause(status int) error {
	switch {
	case status >= 500:
		return errors.New("服务端返回 " + strconv.Itoa(status))
	case status >= 400:
		return errors.New("HTTP " + strconv.Itoa(status))
	default:
		return errors.New("意外的响应状态 " + strconv.Itoa(status))
	}
}

// scrubURLError 把 `net/http` 的错误压成**只剩类型信息**的错误。
//
// 原始文本形如 `Get "https://…?sign=…": dial tcp …`——**含完整签名 URL**，
// 而 B-303/B-722 明令禁止它进日志与用户消息。排错需要的分类信息
// （超时 / 取消 / 连接错误）在这条错误里保留，细节靠调用方的本地日志。
func scrubURLError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.DeadlineExceeded):
		return errors.New("请求超时")
	case errors.Is(err, context.Canceled):
		return errors.New("请求被取消")
	default:
		return errors.New("网络错误")
	}
}

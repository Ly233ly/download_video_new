package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 输出目录布局（[01 §8]）：
//
//	%USERPROFILE%\Downloads\留底下载器\
//	├── 临时\<plan_id>\   程序临时文件（有任务归属）
//	├── 预览\             任务预览
//	└── 已完成\           最终交付副本
//
// 它与数据目录（%LOCALAPPDATA%\LiudiDownloader）**刻意分开**：输出目录是
// 用户资产，卸载不删；数据目录是程序状态。这个分界是 [10 §8] 的 V1/V4 的前提。
const (
	OutputDirName    = "留底下载器"
	TempDirName      = "临时"
	PreviewDirName   = "预览"
	CompletedDirName = "已完成"
)

// DefaultOutputRoot 返回默认输出根目录（[01 §8]）。
//
// 用 os.UserHomeDir 而不是 Windows 已知文件夹 API：规范写死的就是
// `%USERPROFILE%\Downloads\留底下载器\` 这一条路径，且它必须与用户在资源
// 管理器里看到的一致。被重定向过的 Downloads 目录由用户经 settings.output_dir
// 覆盖（[03 §2.4]），不由程序猜测。
func DefaultOutputRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("定位用户主目录失败: %w", err)
	}
	return filepath.Join(home, "Downloads", OutputDirName), nil
}

// OutputDirs 是输出根目录及其三个子目录的绝对路径。
type OutputDirs struct {
	Root      string
	Temp      string
	Preview   string
	Completed string
}

// ResolveOutputDirs 解析输出目录。root 为空表示用默认目录——这正是
// [03 §2.4] 的 `output_dir` 配置语义（"空表示默认目录"）。
func ResolveOutputDirs(root string) (OutputDirs, error) {
	if strings.TrimSpace(root) == "" {
		var err error
		if root, err = DefaultOutputRoot(); err != nil {
			return OutputDirs{}, err
		}
	}
	return OutputDirs{
		Root:      root,
		Temp:      filepath.Join(root, TempDirName),
		Preview:   filepath.Join(root, PreviewDirName),
		Completed: filepath.Join(root, CompletedDirName),
	}, nil
}

// PlanTempDir 返回某个计划的临时目录（[05 §11]：`临时\<plan_id>\`）。
//
// planID 由程序生成，仍然要校验：它会被拼进路径，一旦混入分隔符或 `..`
// 就会写到目录外。这是"防自己出错"，不是防攻击者（设计原则 2）——
// 而写到目录外会直接违反 B-404（程序临时文件必须位于程序自己的临时目录）。
func (d OutputDirs) PlanTempDir(planID string) (string, error) {
	if err := validatePathSegment(planID); err != nil {
		return "", err
	}
	return filepath.Join(d.Temp, planID), nil
}

// EnsurePlanTemp 创建计划的临时目录并返回其路径。
func (d OutputDirs) EnsurePlanTemp(planID string) (string, error) {
	dir, err := d.PlanTempDir(planID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("创建任务临时目录失败: %w", err)
	}
	return dir, nil
}

// EnsureCompleted 创建「已完成」目录（交付前调用）。
func (d OutputDirs) EnsureCompleted() error {
	if err := os.MkdirAll(d.Completed, 0o700); err != nil {
		return fmt.Errorf("创建输出目录失败: %w", err)
	}
	return nil
}

// EnsurePreview 创建「预览」目录。
func (d OutputDirs) EnsurePreview() error {
	if err := os.MkdirAll(d.Preview, 0o700); err != nil {
		return fmt.Errorf("创建预览目录失败: %w", err)
	}
	return nil
}

// validatePathSegment 拒绝会导致路径逃逸的片段。
//
// 错误消息**不含**违规内容本身（[12 §3.3] 的 E3）。
func validatePathSegment(segment string) error {
	if segment == "" || segment == "." || segment == ".." {
		return fmt.Errorf("路径片段非法")
	}
	// Windows 保留字符 + 分隔符；`:` 同时挡住盘符与 NTFS 数据流。
	if strings.ContainsAny(segment, `/\:*?"<>|`) {
		return fmt.Errorf("路径片段含非法字符")
	}
	for _, r := range segment {
		if r < 0x20 {
			return fmt.Errorf("路径片段含控制字符")
		}
	}
	return nil
}

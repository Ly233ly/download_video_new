package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// 打开资源管理器（B-309：completed 的计划要能"打开所在文件夹"）。
//
// 用 explorer.exe 而不是 shell API：它是 Windows 上唯一稳定的入口，
// 且不需要 CGO（[CONTEXT §3.4]：本机没有 C 编译器）。
const (
	explorerExe = "explorer.exe"
	// selectFlag 让资源管理器**选中**文件而不是用默认程序打开它。
	// 注意它必须与路径**连写**（`/select,C:\path`），中间加空格会被当成两个参数。
	selectFlag = "/select,"
)

// openLauncher 负责真正启动进程，抽成变量以便测试替换：
// 单元测试不能弹出资源管理器窗口。
var openLauncher = func(name string, args ...string) error {
	// 用 Run 而不是 Start：Start 之后必须 Wait 才不泄漏句柄，而 Wait 又要放进
	// 一个无主 goroutine（[12 §9] 的 C1 禁止）。explorer.exe 正常路径下立即返回。
	return exec.Command(name, args...).Run()
}

// OpenFolder 在资源管理器里打开一个目录，或在其中选中一个文件（B-309）。
//
// **调用前自己校验路径存在**：explorer.exe 对不存在的路径常常也返回成功，
// 那会把"文件已被删除"变成"点了没反应"——B-403 要求这种情况下保留并如实报告，
// 而不是假装打开成功。
//
// 错误消息**不含路径**（[12 §3.3] 的 E3）。
func OpenFolder(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("没有可打开的路径")
	}

	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("文件不存在或已被移动")
	}

	if info.IsDir() {
		if err := openLauncher(explorerExe, path); err != nil {
			return fmt.Errorf("打开文件夹失败")
		}
		return nil
	}

	// 文件：打开其所在目录并选中它。`/select,` 需要**绝对路径**。
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("打开文件夹失败")
	}
	if err := openLauncher(explorerExe, selectFlag+absolute); err != nil {
		return fmt.Errorf("打开文件夹失败")
	}
	return nil
}

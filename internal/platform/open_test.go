package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// captureLauncher 替换真实启动器，记录被调用的参数。
func captureLauncher(t *testing.T) *[]string {
	t.Helper()
	var calls []string
	original := openLauncher
	openLauncher = func(name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	t.Cleanup(func() { openLauncher = original })
	return &calls
}

func TestOpenFolder_MissingPathDoesNotLaunch(t *testing.T) {
	calls := captureLauncher(t)
	missing := filepath.Join(t.TempDir(), "nope.mp4")

	if err := OpenFolder(missing); err == nil {
		t.Fatal("不存在的路径应当报错")
	}
	// 关键断言：**没有**去启动资源管理器。
	// explorer 对不存在的路径常返回成功，靠它发现"文件没了"是不可靠的。
	if len(*calls) != 0 {
		t.Fatalf("不存在的路径不应启动资源管理器，实际调用了 %v", *calls)
	}
}

func TestOpenFolder_EmptyPathDoesNotLaunch(t *testing.T) {
	calls := captureLauncher(t)
	if err := OpenFolder("   "); err == nil {
		t.Fatal("空路径应当报错")
	}
	if len(*calls) != 0 {
		t.Fatalf("空路径不应启动资源管理器，实际调用了 %v", *calls)
	}
}

func TestOpenFolder_DirectoryOpensFolder(t *testing.T) {
	calls := captureLauncher(t)
	dir := t.TempDir()

	if err := OpenFolder(dir); err != nil {
		t.Fatalf("打开目录失败: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("应当只启动一次，实际 %d 次", len(*calls))
	}
	if !strings.Contains((*calls)[0], dir) {
		t.Fatalf("调用参数里应含目录路径，实际 %q", (*calls)[0])
	}
	if strings.Contains((*calls)[0], selectFlag) {
		t.Fatalf("目录不应带 %s 参数，实际 %q", selectFlag, (*calls)[0])
	}
}

func TestOpenFolder_FileSelectsIt(t *testing.T) {
	calls := captureLauncher(t)
	file := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}

	if err := OpenFolder(file); err != nil {
		t.Fatalf("选中文件失败: %v", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("应当只启动一次，实际 %d 次", len(*calls))
	}
	// `/select,` 必须与**绝对路径连写**，中间不能有空格。
	if !strings.Contains((*calls)[0], selectFlag) {
		t.Fatalf("文件应当带 %s 参数，实际 %q", selectFlag, (*calls)[0])
	}
	if !strings.Contains((*calls)[0], selectFlag+file) {
		t.Fatalf("%s 应与绝对路径连写，实际 %q", selectFlag, (*calls)[0])
	}
}

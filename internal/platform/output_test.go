package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveOutputDirs_EmptyRootUsesDefault(t *testing.T) {
	dirs, err := ResolveOutputDirs("")
	if err != nil {
		t.Fatalf("解析默认输出目录失败: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("取用户主目录失败: %v", err)
	}
	want := filepath.Join(home, "Downloads", OutputDirName)
	if dirs.Root != want {
		t.Fatalf("默认根目录 = %q，期望 %q", dirs.Root, want)
	}
	// 三个子目录都必须挂在根目录下，且名字与 [01 §8] 的布局逐字一致。
	for _, item := range []struct{ got, want string }{
		{dirs.Temp, filepath.Join(want, TempDirName)},
		{dirs.Preview, filepath.Join(want, PreviewDirName)},
		{dirs.Completed, filepath.Join(want, CompletedDirName)},
	} {
		if item.got != item.want {
			t.Errorf("子目录 = %q，期望 %q", item.got, item.want)
		}
	}
}

func TestResolveOutputDirs_KeepsConfiguredRoot(t *testing.T) {
	// settings.output_dir 非空时必须原样使用（[03 §2.4]），不得再拼默认目录。
	custom := filepath.Join(t.TempDir(), "我的下载")
	dirs, err := ResolveOutputDirs(custom)
	if err != nil {
		t.Fatalf("解析自定义输出目录失败: %v", err)
	}
	if dirs.Root != custom {
		t.Fatalf("根目录 = %q，期望 %q", dirs.Root, custom)
	}
}

func TestPlanTempDir_RejectsEscapingSegment(t *testing.T) {
	dirs, err := ResolveOutputDirs(t.TempDir())
	if err != nil {
		t.Fatalf("准备输出目录失败: %v", err)
	}
	// 路径逃逸必须被挡住：写出去会直接违反 B-404。
	for _, bad := range []string{"", ".", "..", `..\..\windows`, "a/b", `a\b`, "c:evil", "a*b"} {
		if _, err := dirs.PlanTempDir(bad); err == nil {
			t.Errorf("planID %q 应当被拒绝，但没有", bad)
		}
	}
}

func TestPlanTempDir_AcceptsPlanID(t *testing.T) {
	dirs, err := ResolveOutputDirs(t.TempDir())
	if err != nil {
		t.Fatalf("准备输出目录失败: %v", err)
	}
	got, err := dirs.PlanTempDir("01J8Z0PLAN")
	if err != nil {
		t.Fatalf("正常 planID 被拒绝: %v", err)
	}
	if !strings.HasPrefix(got, dirs.Temp) {
		t.Fatalf("临时目录 %q 不在 %q 之下", got, dirs.Temp)
	}
}

func TestEnsurePlanTemp_CreatesDirectory(t *testing.T) {
	dirs, err := ResolveOutputDirs(t.TempDir())
	if err != nil {
		t.Fatalf("准备输出目录失败: %v", err)
	}
	dir, err := dirs.EnsurePlanTemp("PLAN1")
	if err != nil {
		t.Fatalf("创建临时目录失败: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("临时目录未创建: %v", err)
	}
}

func TestEnsureCompleted_CreatesDirectory(t *testing.T) {
	dirs, err := ResolveOutputDirs(t.TempDir())
	if err != nil {
		t.Fatalf("准备输出目录失败: %v", err)
	}
	if err := dirs.EnsureCompleted(); err != nil {
		t.Fatalf("创建已完成目录失败: %v", err)
	}
	if info, err := os.Stat(dirs.Completed); err != nil || !info.IsDir() {
		t.Fatalf("已完成目录未创建: %v", err)
	}
}

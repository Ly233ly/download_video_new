package media

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// 同名冲突必须生成唯一名，且**绝不覆盖**已有文件（B-406、[05 §11]）。
func TestDeliverFile_NameCollisionGeneratesUniqueName(t *testing.T) {
	body := mediaBody(512)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-collision")
	occupied := []string{"样本.mp4", "样本 (1).mp4", "样本 (2).mp4"}
	for _, name := range occupied {
		if err := os.WriteFile(filepath.Join(dirs.outputDir, name), []byte("已有内容:"+name), 0o600); err != nil {
			t.Fatalf("写占位文件失败: %v", err)
		}
	}

	d := newDownloader(t, Options{})
	result, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), nil)
	if err != nil {
		t.Fatalf("同名冲突时应生成唯一名而不是失败: %v", err)
	}

	if got := filepath.Base(result.FinalPath); got != "样本 (3).mp4" {
		t.Errorf("唯一名 = %q，期望 样本 (3).mp4", got)
	}
	// 已有的三个文件必须逐字节保持原样。
	for _, name := range occupied {
		raw := readFile(t, filepath.Join(dirs.outputDir, name))
		if string(raw) != "已有内容:"+name {
			t.Errorf("已有文件 %s 被改动了：%q", name, raw)
		}
	}
	// 新交付的文件内容正确。
	if string(readFile(t, result.FinalPath)) != string(body) {
		t.Error("交付的文件内容与源不一致")
	}
}

// 唯一名尝试次数用尽时报 `output_name_exhausted`（[05 §11]），
// 且**不得**在任何已有文件上留下痕迹。
func TestDeliverFile_UniqueNameExhausted(t *testing.T) {
	dirs := newLayout(t, "plan-exhausted")
	for attempt := 0; attempt < MaxUniqueNameAttempts; attempt++ {
		name := uniqueCandidate("样本.mp4", attempt)
		if err := os.WriteFile(filepath.Join(dirs.outputDir, name), []byte("x"), 0o600); err != nil {
			t.Fatalf("写占位文件失败: %v", err)
		}
	}

	staging := dirs.staging()
	dirs.writePartial(t, mediaBody(16))

	_, err := deliverFile(dirs.outputDir, "样本.mp4", staging, 16)
	if err == nil {
		t.Fatal("唯一名用尽时必须报错")
	}
	code, ok := CodeOf(err)
	if !ok || code != CodeOutputNameExhausted {
		t.Fatalf("错误码 = %v，期望 %s", err, CodeOutputNameExhausted)
	}

	entries, readErr := os.ReadDir(dirs.outputDir)
	if readErr != nil {
		t.Fatalf("读输出目录失败: %v", readErr)
	}
	if len(entries) != MaxUniqueNameAttempts {
		t.Errorf("输出目录文件数 = %d，期望 %d（不得多留占位文件）", len(entries), MaxUniqueNameAttempts)
	}
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			t.Fatalf("读 %s 信息失败: %v", entry.Name(), infoErr)
		}
		if info.Size() != 1 {
			t.Errorf("文件 %s 被改动（长度 %d）", entry.Name(), info.Size())
		}
	}
	// 临时产物仍在：它没有被交付，也没有被删除。
	if _, statErr := os.Stat(staging); statErr != nil {
		t.Errorf("交付失败时临时产物应保留，实际 %v", statErr)
	}
}

// 交付失败（「已完成」目录不可写）时**保留**已校验的临时产物，供重试复用。
func TestRunDirect_DeliveryFailureKeepsVerifiedTempFile(t *testing.T) {
	body := mediaBody(256)
	media := &fakeMedia{body: body}
	server := startMedia(t, media)

	dirs := newLayout(t, "plan-deliver-fail")
	if err := os.Chmod(dirs.outputDir, 0o500); err != nil {
		t.Fatalf("收紧输出目录权限失败: %v", err)
	}
	// Windows 上目录权限对"新建文件"生效；无论是否生效，下面的断言都成立：
	// 成功时交付成功，失败时临时产物必须还在。
	d := newDownloader(t, Options{})
	_, err := d.Run(context.Background(), dirs.request(server.URL, int64(len(body))), nil)
	if err != nil {
		if code, ok := CodeOf(err); !ok || code != CodeDownloadFailed {
			t.Fatalf("交付失败的码 = %v，期望 %s", err, CodeDownloadFailed)
		}
		if _, statErr := os.Stat(dirs.staging()); statErr != nil {
			t.Errorf("交付失败时临时产物应保留（字节已校验通过），实际 %v", statErr)
		}
		return
	}
	// 该环境下无法让目录不可写：至少确认交付本身是对的。
	if _, statErr := os.Stat(dirs.staging()); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("交付成功后临时产物应当已搬走")
	}
}

// 输出名带路径分隔符时必须拒绝：它会把文件写到「已完成」之外（[03 §4.4]）。
func TestDeliverFile_RejectsNamesWithSeparators(t *testing.T) {
	dirs := newLayout(t, "plan-sep")
	staging := dirs.staging()
	dirs.writePartial(t, mediaBody(16))

	for _, name := range []string{`..\逃逸.mp4`, "子目录/逃逸.mp4", "", "   "} {
		if _, err := deliverFile(dirs.outputDir, name, staging, 16); err == nil {
			t.Errorf("输出名 %q 必须被拒绝", name)
		}
	}
}

// 预留必须是**原子创建**：目标已存在时 `O_EXCL` 直接失败，绝不覆盖（B-406）。
func TestReservePath_RefusesExistingTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "已存在.mp4")
	if err := os.WriteFile(path, []byte("原内容"), 0o600); err != nil {
		t.Fatalf("写文件失败: %v", err)
	}
	file, err := reservePath(path)
	if err == nil {
		_ = file.Close()
		t.Fatal("目标已存在时预留必须失败")
	}
	if !errors.Is(err, os.ErrExist) {
		t.Errorf("应当是 os.ErrExist，实得 %v", err)
	}
	if string(readFile(t, path)) != "原内容" {
		t.Error("预留失败不得改动已有文件")
	}
}

// 序号插在扩展名之前，保住容器后缀（后缀是播放器识别格式的依据）。
func TestUniqueCandidate_KeepsExtension(t *testing.T) {
	cases := map[string]string{
		"样本.mp4":    "样本 (2).mp4",
		"无扩展名":      "无扩展名 (2)",
		"多点.名字.mkv": "多点.名字 (2).mkv",
		".hidden":   ".hidden (2)",
		"样本.m4a":    "样本 (2).m4a",
		"带 空格.webm": "带 空格 (2).webm",
	}
	for input, want := range cases {
		if got := uniqueCandidate(input, 2); got != want {
			t.Errorf("uniqueCandidate(%q, 2) = %q，期望 %q", input, got, want)
		}
	}
	if got := uniqueCandidate("样本.mp4", 0); got != "样本.mp4" {
		t.Errorf("第 0 个候选就应当是原名，实得 %q", got)
	}
}

// 磁盘满必须映射到**独立**的 `disk_full` 码（[05 §4.1]，且不自动重试）。
func TestMapWriteError_DiskFullHasItsOwnCode(t *testing.T) {
	d := newDownloader(t, Options{})
	req := Request{PlanID: "plan-enospc"}

	full := &os.PathError{Op: "write", Path: "临时\\direct.bin", Err: syscall.ENOSPC}
	err := d.mapWriteError(req, full)
	code, ok := CodeOf(err)
	if !ok || code != CodeDiskFull {
		t.Fatalf("ENOSPC 的码 = %v，期望 %s", err, CodeDiskFull)
	}
	if Retryable(code) {
		t.Error("disk_full 不得自动重试（[05 §7.2]）")
	}
	if strings.Contains(err.Error(), "direct.bin") || strings.Contains(err.Error(), "临时") {
		t.Errorf("用户可见消息泄露了路径：%q", err.Error())
	}

	// 其它 I/O 错误算可重试的下载失败。
	other := d.mapWriteError(req, errors.New("连接被对端重置"))
	if code, ok := CodeOf(other); !ok || code != CodeDownloadFailed {
		t.Fatalf("普通写失败的码 = %v，期望 %s", other, CodeDownloadFailed)
	}
}

// `ENOSPC` 必须能被穿透包装识别（写盘错误通常是 `*os.PathError`）。
func TestIsDiskFull_DetectsWrappedENOSPC(t *testing.T) {
	wrapped := &os.PathError{Op: "write", Path: "x", Err: syscall.ENOSPC}
	if !isDiskFull(wrapped) {
		t.Error("包装后的 ENOSPC 必须被识别")
	}
	if isDiskFull(errors.New("普通错误")) {
		t.Error("普通错误不得被当成磁盘满")
	}
	if isDiskFull(nil) {
		t.Error("nil 不得被当成磁盘满")
	}
}

// 磁盘满的失败路径必须**清理本次临时产物**（[05 §4.1]："清理本次临时产物"）。
func TestCleanupAfterFailure_DiskFullRemovesStaging(t *testing.T) {
	dirs := newLayout(t, "plan-diskfull")
	dirs.writePartial(t, mediaBody(64))

	d := newDownloader(t, Options{})
	d.cleanupAfterFailure(
		Request{PlanID: "plan-diskfull"},
		workDirs{planDir: dirs.planDir, outputDir: dirs.outputDir, staging: dirs.staging()},
		ErrorOf(CodeDiskFull, syscall.ENOSPC),
		false,
	)

	if _, err := os.Stat(dirs.staging()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("磁盘满时必须清掉临时产物，实际 %v", err)
	}
	if _, err := os.Stat(dirs.planDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("空计划目录应被回收，实际 %v", err)
	}
}

// 计划目录**本来就有别人或别的内容**时不得删除它（[05 §11]：只清理自己创建的空目录）。
func TestCleanupAfterFailure_KeepsNonEmptyPlanDir(t *testing.T) {
	dirs := newLayout(t, "plan-keep")
	dirs.writePartial(t, mediaBody(64))
	other := filepath.Join(dirs.planDir, "别的产物.bin")
	if err := os.WriteFile(other, []byte("别的内容"), 0o600); err != nil {
		t.Fatalf("写旁路文件失败: %v", err)
	}

	d := newDownloader(t, Options{})
	d.cleanupAfterFailure(
		Request{PlanID: "plan-keep"},
		workDirs{planDir: dirs.planDir, outputDir: dirs.outputDir, staging: dirs.staging()},
		ErrorOf(CodeDiskFull, syscall.ENOSPC),
		false,
	)

	if _, err := os.Stat(other); err != nil {
		t.Errorf("非空计划目录里的其它文件不得被删（%v）", err)
	}
	if _, err := os.Stat(dirs.planDir); err != nil {
		t.Errorf("非空计划目录不得被删（%v）", err)
	}
}

// 临时根目录**永远不被回收**：它是上层按 [05 §11] 创建的，不属于单次下载。
func TestCleanupAfterFailure_NeverRemovesTempRoot(t *testing.T) {
	dirs := newLayout(t, "plan-root")
	dirs.writePartial(t, mediaBody(64))

	d := newDownloader(t, Options{})
	planDir := dirs.planDir
	d.cleanupAfterFailure(
		Request{PlanID: "plan-root"},
		workDirs{planDir: planDir, outputDir: dirs.outputDir, staging: dirs.staging()},
		ErrorOf(CodeDiskFull, syscall.ENOSPC),
		false,
	)

	if _, err := os.Stat(dirs.tempRoot); err != nil {
		t.Errorf("临时根目录不得被删（%v）", err)
	}
}

// 网络类失败**保留**分片：它正是 [05 §4.1] 断点续传的起点。
func TestCleanupAfterFailure_NetworkErrorKeepsPartialForResume(t *testing.T) {
	dirs := newLayout(t, "plan-network")
	dirs.writePartial(t, mediaBody(64))

	d := newDownloader(t, Options{})
	d.cleanupAfterFailure(
		Request{PlanID: "plan-network"},
		workDirs{planDir: dirs.planDir, outputDir: dirs.outputDir, staging: dirs.staging()},
		ErrorOf(CodeDownloadFailed, errors.New("网络错误")),
		false,
	)

	if _, err := os.Stat(dirs.staging()); err != nil {
		t.Errorf("网络失败应保留分片以便续传，实际 %v", err)
	}
}

// 一次失败**只清自己那个计划**的产物，同级的另一个计划目录不受影响（B-404）。
func TestRun_CancelCleansOnlyItsOwnPlanDir(t *testing.T) {
	handlerStarted := make(chan struct{})
	var once = make(chan struct{}, 1)
	release := make(chan struct{})

	server := newHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "65536")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(mediaBody(64))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		select {
		case once <- struct{}{}:
			close(handlerStarted)
		default:
		}
		<-release
	}))

	dirs := newLayout(t, "plan-mine")
	otherPlan := filepath.Join(dirs.tempRoot, "plan-other")
	if err := os.MkdirAll(otherPlan, 0o700); err != nil {
		t.Fatalf("建另一个计划目录失败: %v", err)
	}
	otherFile := filepath.Join(otherPlan, "direct.bin")
	if err := os.WriteFile(otherFile, []byte("别的计划的字节"), 0o600); err != nil {
		t.Fatalf("写另一个计划的分片失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	d := newDownloader(t, Options{})
	errCh := make(chan error, 1)
	go func() {
		_, err := d.Run(ctx, dirs.request(server.URL, 65536), nil)
		errCh <- err
	}()

	<-handlerStarted
	cancel()
	close(release)
	<-errCh

	if _, err := os.Stat(otherFile); err != nil {
		t.Errorf("不得清理别的计划的产物（%v）", err)
	}
	if _, err := os.Stat(otherPlan); err != nil {
		t.Errorf("不得删除别的计划的目录（%v）", err)
	}
	if _, err := os.Stat(dirs.planDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("本次创建的空计划目录应被回收，实际 %v", err)
	}
}

// 目录归属判定必须按"路径片段"而不是字符串前缀：
// `C:\tmp\ab` 不是 `C:\tmp\a` 的子路径。
func TestIsWithin_UsesPathSegments(t *testing.T) {
	base := filepath.Join("C:", "tmp", "a")
	if !isWithin(base, filepath.Join(base, "b", "c.bin")) {
		t.Error("子孙路径应当判定为在内部")
	}
	if !isWithin(base, base) {
		t.Error("自身应当判定为在内部")
	}
	if isWithin(base, filepath.Join("C:", "tmp", "ab")) {
		t.Error("同前缀的兄弟目录不算内部（字符串前缀比较的经典错误）")
	}
	if isWithin(base, filepath.Join(base, "..", "外.bin")) {
		t.Error("上跳的路径不算内部")
	}
}

// 交付时不得顺手创建「已完成」目录：目录布局由上层负责（[05 §11]），
// 这里静默补建会把"目录配错"掩盖成"下载成功"。
func TestDeliverFile_DoesNotCreateOutputDir(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "不存在的已完成")

	staging := filepath.Join(base, "direct.bin")
	if err := os.WriteFile(staging, mediaBody(8), 0o600); err != nil {
		t.Fatalf("写临时文件失败: %v", err)
	}

	if _, err := deliverFile(missing, "样本.mp4", staging, 8); err == nil {
		t.Fatal("输出目录不存在时应当报错")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Error("不得替上层创建「已完成」目录")
	}
}

// 长度与校验时观测不一致的临时产物**不得**交付（避免搬走一个与已验证内容不同的文件）。
func TestDeliverFile_RejectsLengthMismatch(t *testing.T) {
	dirs := newLayout(t, "plan-size")
	dirs.writePartial(t, mediaBody(32))

	_, err := deliverFile(dirs.outputDir, "样本.mp4", dirs.staging(), 64)
	if err == nil {
		t.Fatal("长度不符必须报错")
	}
	if code, ok := CodeOf(err); !ok || code != CodeDownloadFailed {
		t.Fatalf("错误码 = %v，期望 %s", err, CodeDownloadFailed)
	}
	assertNothingDelivered(t, dirs)
}

// 校验通过的那份字节与搬走的必须是同一份：交付后的长度要等于校验时的观测。
func TestDeliverFile_MovesVerifiedBytesAtomically(t *testing.T) {
	dirs := newLayout(t, "plan-atomic")
	body := mediaBody(4096)
	dirs.writePartial(t, body)

	result, err := deliverFile(dirs.outputDir, "样本.mp4", dirs.staging(), int64(len(body)))
	if err != nil {
		t.Fatalf("交付失败: %v", err)
	}
	if string(readFile(t, result.Path)) != string(body) {
		t.Error("交付后的内容与临时产物不一致")
	}
	if result.Bytes != int64(len(body)) {
		t.Errorf("Bytes = %d，期望 %d", result.Bytes, len(body))
	}
	if _, err := os.Stat(dirs.staging()); !errors.Is(err, os.ErrNotExist) {
		t.Error("交付后临时产物应当已搬走")
	}
}

// 唯一名序号与"已存在文件"的对应关系必须稳定可预测，便于用户识别。
func TestDeliverFile_UniqueNameIsPredictable(t *testing.T) {
	dirs := newLayout(t, "plan-predict")
	if err := os.WriteFile(filepath.Join(dirs.outputDir, "样本.mp4"), []byte("旧"), 0o600); err != nil {
		t.Fatalf("写占位失败: %v", err)
	}
	dirs.writePartial(t, mediaBody(16))

	result, err := deliverFile(dirs.outputDir, "样本.mp4", dirs.staging(), 16)
	if err != nil {
		t.Fatalf("交付失败: %v", err)
	}
	if got := filepath.Base(result.Path); got != "样本 (1).mp4" {
		t.Errorf("唯一名 = %q，期望 样本 (1).mp4", got)
	}
	if _, err := os.Stat(filepath.Join(dirs.outputDir, "样本.mp4")); err != nil {
		t.Errorf("原有文件不得被动过：%v", err)
	}
}

// 占位文件在交付失败后不得残留（否则用户会看到一堆 0 字节文件）。
func TestDeliverFile_NoPlaceholderLeftBehind(t *testing.T) {
	dirs := newLayout(t, "plan-placeholder")
	dirs.writePartial(t, mediaBody(16))

	if _, err := deliverFile(dirs.outputDir, "样本.mp4", dirs.staging(), 999); err == nil {
		t.Fatal("长度不符必须报错")
	}
	entries, err := os.ReadDir(dirs.outputDir)
	if err != nil {
		t.Fatalf("读输出目录失败: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("不得残留占位文件，实得 %v", names)
	}
}

// 结构校验：只有 `output_*` 系列的码才允许在"字节没问题"的情况下拦交付，
// 其余失败必须是 `download_failed`（[05 §6] 与 [05 §7.2] 的对应关系）。
func TestValidateStructure_CodesMatchSpecTable(t *testing.T) {
	cases := []struct {
		name    string
		raw     []byte
		want    Code
		contain string
	}{
		{"空流列表", []byte(`{"streams":[],"format":{"format_name":"mp4"}}`), CodeOutputNoStreams, "mp4"},
		{"只有字幕流", []byte(`{"streams":[{"index":0,"codec_type":"subtitle"}],"format":{"format_name":"mp4"}}`),
			CodeOutputNoStreams, "mp4"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dirs, err := runWithProbe(t, "plan-"+strconv.Itoa(len(tc.name)), tc.contain, func(int64) ([]byte, error) {
				return tc.raw, nil
			})
			if err == nil {
				t.Fatal("必须报错")
			}
			if code, ok := CodeOf(err); !ok || code != tc.want {
				t.Fatalf("错误码 = %v，期望 %s", err, tc.want)
			}
			assertNothingDelivered(t, dirs)
		})
	}
}

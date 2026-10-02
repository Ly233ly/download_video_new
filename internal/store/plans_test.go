package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// 计划仓储的测试。全程使用 t.TempDir() 与临时数据库，不污染用户目录（[12 §5.2] 的 T2）。
// 命名遵循 [12 §2]：Test<被测>_<场景>。

func openPlanTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func samplePlan(id string) Plan {
	return Plan{
		ID:              id,
		SourceURL:       "https://example.com/watch?v=1",
		SourceTitle:     "示例标题",
		MediaKind:       PlanMediaDirect,
		OutputName:      "示例视频",
		OutputContainer: PlanContainerMP4,
		MergeMode:       PlanMergeSingle,
		StreamPlan:      "[]",
		Status:          PlanStatusQueued,
	}
}

func mustInsert(t *testing.T, db *DB, p Plan) {
	t.Helper()
	if err := db.InsertPlan(context.Background(), p); err != nil {
		t.Fatalf("InsertPlan(%s) 失败: %v", p.ID, err)
	}
}

func mustGet(t *testing.T, db *DB, id string) Plan {
	t.Helper()
	p, err := db.GetPlan(context.Background(), id)
	if err != nil {
		t.Fatalf("GetPlan(%s) 失败: %v", id, err)
	}
	return p
}

// explainPlanQuery 返回 EXPLAIN QUERY PLAN 的 detail 行，供索引断言使用（[03 §5] 的 Q1~Q3）。
func explainPlanQuery(t *testing.T, db *DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.SQL().Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN 失败: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("读取查询计划失败: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("读取查询计划失败: %v", err)
	}
	return details
}

// ---------------------------------------------------------------------------
// total_bytes 的三态：[03 §2.1] 明确 NULL = 长度未知，0 = 长度为零，两者语义不同。
// ---------------------------------------------------------------------------

func TestInsertPlan_KeepsNullTotalBytesDistinctFromZero(t *testing.T) {
	db := openPlanTestDB(t)

	unknown := samplePlan("p-unknown")
	mustInsert(t, db, unknown)

	zero := int64(0)
	empty := samplePlan("p-zero")
	empty.TotalBytes = &zero
	mustInsert(t, db, empty)

	gotUnknown := mustGet(t, db, "p-unknown")
	if gotUnknown.TotalBytes != nil {
		t.Errorf("长度未知的计划 total_bytes = %v，期望 NULL（不得用 0 表示未知）", *gotUnknown.TotalBytes)
	}

	gotZero := mustGet(t, db, "p-zero")
	if gotZero.TotalBytes == nil {
		t.Fatal("长度为零的计划 total_bytes = NULL，期望 0——两者被混淆了")
	}
	if *gotZero.TotalBytes != 0 {
		t.Errorf("total_bytes = %d，期望 0", *gotZero.TotalBytes)
	}
}

// ---------------------------------------------------------------------------
// 写入前的数据不变量（[03 §2.1]）
// ---------------------------------------------------------------------------

func TestInsertPlan_RejectsInvariantViolations(t *testing.T) {
	zero := int64(0)
	cases := []struct {
		name   string
		mutate func(*Plan)
	}{
		{"completed 缺 final_path", func(p *Plan) { p.Status = PlanStatusCompleted }},
		{"failed 缺 error_code", func(p *Plan) { p.Status = PlanStatusFailed }},
		{"running 缺 phase", func(p *Plan) { p.Status = PlanStatusRunning }},
		{"非 running 带 phase", func(p *Plan) { p.Phase = PlanPhaseDownloading }},
		{"非 completed 却是 100%", func(p *Plan) { p.Progress = 100 }},
		{"负的总字节数", func(p *Plan) { p.TotalBytes = &[]int64{-1}[0] }},
		{"空输出名", func(p *Plan) { p.OutputName = "" }},
		{"非法容器", func(p *Plan) { p.OutputContainer = "avi" }},
		{"非法合并方式", func(p *Plan) { p.MergeMode = "merge" }},
		{"非法媒体类型", func(p *Plan) { p.MediaKind = "rtmp" }},
		{"进度越界", func(p *Plan) { p.Progress = 101 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openPlanTestDB(t)
			p := samplePlan("p1")
			p.TotalBytes = &zero
			tc.mutate(&p)
			if err := db.InsertPlan(context.Background(), p); err == nil {
				t.Fatal("非法记录被写入了库，应当拒绝")
			}
			if _, err := db.GetPlan(context.Background(), "p1"); !errors.Is(err, ErrPlanNotFound) {
				t.Errorf("拒绝后库里不应留下记录，GetPlan 返回 %v", err)
			}
		})
	}
}

// stream_plan 不得包含签名 URL 或解密键（[03 §2.1]、B-722 的秘密边界）。
func TestInsertPlan_RejectsSecretsInStreamPlan(t *testing.T) {
	cases := []struct {
		name string
		plan string
	}{
		{"含媒体 URL", `[{"kind":"video","url":"https://cdn.example.com/a.mp4?sign=x"}]`},
		{"含解密键", `[{"kind":"video","decode_key":"abcdef"}]`},
		{"含 token", `[{"index":0,"token":"t"}]`},
		{"不是数组", `{"kind":"video"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openPlanTestDB(t)
			p := samplePlan("p1")
			p.StreamPlan = tc.plan
			if err := db.InsertPlan(context.Background(), p); err == nil {
				t.Fatal("含秘密或非数组的 stream_plan 被写入，应当拒绝")
			}
		})
	}
}

func TestInsertPlan_AcceptsTrackOnlyStreamPlan(t *testing.T) {
	db := openPlanTestDB(t)
	p := samplePlan("p1")
	p.StreamPlan = `[{"kind":"video","index":0,"quality":"1080p"}]`
	mustInsert(t, db, p)

	if got := mustGet(t, db, "p1").StreamPlan; got != p.StreamPlan {
		t.Errorf("stream_plan = %q，期望 %q", got, p.StreamPlan)
	}
}

// ---------------------------------------------------------------------------
// 状态转换守卫（[05 §2.1]、B-308）
// ---------------------------------------------------------------------------

func TestMarkPlanRunning_OnlyFromQueued(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))

	if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseDownloading); err != nil {
		t.Fatalf("queued → running 应当成功: %v", err)
	}
	got := mustGet(t, db, "p1")
	if got.Status != PlanStatusRunning || got.Phase != PlanPhaseDownloading {
		t.Fatalf("状态 = %s/%s，期望 running/downloading", got.Status, got.Phase)
	}

	// 再次 running 不合法：running → running 不在 [05 §2.1] 的转换表里。
	if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseDownloading); !errors.Is(err, ErrPlanStateChanged) {
		t.Errorf("重复置 running 返回 %v，期望 ErrPlanStateChanged", err)
	}
}

func TestMarkPlanRunning_RejectsInvalidPhase(t *testing.T) {
	db := openPlanTestDB(t)
	mustInsert(t, db, samplePlan("p1"))

	if err := db.MarkPlanRunning(context.Background(), "p1", PlanPhaseNone); err == nil {
		t.Fatal("空 phase 被接受——[05 §2.2] 要求 running 时 phase 必须有值")
	}
}

func TestMarkPlanRunning_NotFound(t *testing.T) {
	db := openPlanTestDB(t)
	err := db.MarkPlanRunning(context.Background(), "missing", PlanPhaseDownloading)
	if !errors.Is(err, ErrPlanNotFound) {
		t.Errorf("返回 %v，期望 ErrPlanNotFound", err)
	}
}

// B-308：用户在校验阶段停止后，完成状态不得覆盖取消状态。
func TestMarkPlanCompleted_DoesNotOverwriteCanceled(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))

	if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseValidating); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}
	// 用户在 validating 阶段点了停止。
	if err := db.MarkPlanCanceled(ctx, "p1"); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	// 后台线程迟到的完成回调。
	err := db.MarkPlanCompleted(ctx, "p1", `C:\out\video.mp4`, 100, nil)
	if !errors.Is(err, ErrPlanStateChanged) {
		t.Fatalf("完成回调返回 %v，期望 ErrPlanStateChanged", err)
	}

	got := mustGet(t, db, "p1")
	if got.Status != PlanStatusCanceled {
		t.Errorf("状态 = %s，期望 canceled——完成状态覆盖了取消状态", got.Status)
	}
	if got.FinalPath != "" {
		t.Errorf("final_path = %q，取消的计划不得写入路径", got.FinalPath)
	}
}

// B-308 的另一半：迟到的失败回调同样不得覆盖取消。
func TestMarkPlanFailed_DoesNotOverwriteCanceled(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))

	if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseDownloading); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}
	if err := db.MarkPlanCanceled(ctx, "p1"); err != nil {
		t.Fatalf("取消失败: %v", err)
	}
	if err := db.MarkPlanFailed(ctx, "p1", "download_failed", "下载失败"); !errors.Is(err, ErrPlanStateChanged) {
		t.Fatalf("失败回调返回 %v，期望 ErrPlanStateChanged", err)
	}
	if got := mustGet(t, db, "p1"); got.Status != PlanStatusCanceled || got.ErrorCode != "" {
		t.Errorf("状态 = %s/%q，期望 canceled 且无错误码", got.Status, got.ErrorCode)
	}
}

func TestMarkPlanCanceled_DoesNotTouchCompleted(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))

	if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseValidating); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}
	if err := db.MarkPlanCompleted(ctx, "p1", `C:\out\video.mp4`, 10, nil); err != nil {
		t.Fatalf("完成失败: %v", err)
	}
	if err := db.MarkPlanCanceled(ctx, "p1"); !errors.Is(err, ErrPlanStateChanged) {
		t.Fatalf("取消已完成的计划返回 %v，期望 ErrPlanStateChanged（[05 §10]：停止不影响已交付文件）", err)
	}
	if got := mustGet(t, db, "p1"); got.Status != PlanStatusCompleted {
		t.Errorf("状态 = %s，期望 completed", got.Status)
	}
}

func TestMarkPlanFailed_RequiresErrorCode(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))
	if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseDownloading); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}
	if err := db.MarkPlanFailed(ctx, "p1", "", "消息"); err == nil {
		t.Fatal("空 error_code 被接受——[03 §2.1] 要求 failed 时该字段非空")
	}
}

func TestMarkPlanFailed_TruncatesLongMessage(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))
	if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseDownloading); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}
	long := strings.Repeat("错", MaxErrorMessageLen+50)
	if err := db.MarkPlanFailed(ctx, "p1", "download_failed", long); err != nil {
		t.Fatalf("MarkPlanFailed 失败: %v", err)
	}
	got := mustGet(t, db, "p1")
	if n := len([]rune(got.ErrorMessage)); n != MaxErrorMessageLen {
		t.Errorf("错误消息长度 = %d，期望截断到 %d（[03 §4.5]）", n, MaxErrorMessageLen)
	}
}

// B-315：重试计数与下次尝试时间必须落库，重启不重置。
func TestMarkPlanQueuedForRetry_PersistsAttemptState(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))
	if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseDownloading); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}

	const nextAttemptAt = 1_800_000_030.5
	if err := db.MarkPlanQueuedForRetry(ctx, "p1", 2, nextAttemptAt); err != nil {
		t.Fatalf("转回 queued 失败: %v", err)
	}

	got := mustGet(t, db, "p1")
	if got.Status != PlanStatusQueued {
		t.Errorf("状态 = %s，期望 queued", got.Status)
	}
	if got.AttemptCount != 2 {
		t.Errorf("attempt_count = %d，期望 2", got.AttemptCount)
	}
	if got.NextAttemptAt == nil || *got.NextAttemptAt != nextAttemptAt {
		t.Errorf("next_attempt_at = %v，期望 %v", got.NextAttemptAt, nextAttemptAt)
	}
	if got.Phase != PlanPhaseNone {
		t.Errorf("phase = %q，期望空串（[03 §2.1]：phase 只在 running 内有值）", got.Phase)
	}
}

func TestMarkPlanQueuedForRetry_RejectsBadArguments(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))
	if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseDownloading); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}
	if err := db.MarkPlanQueuedForRetry(ctx, "p1", -1, 1); err == nil {
		t.Error("负数重试计数被接受，应当拒绝")
	}
	if err := db.MarkPlanQueuedForRetry(ctx, "p1", 1, 0); err == nil {
		t.Error("非法的下次尝试时间被接受，应当拒绝")
	}
	// attempt = 0 是**合法**的：手动重试用它重置自动重试预算。
	if err := db.MarkPlanQueuedForRetry(ctx, "p1", 0, 1); err != nil {
		t.Errorf("attempt = 0 应当被接受（手动重试重置预算）: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 交付：status 与 final_path 必须同事务写入（[03 §2.1]）
// ---------------------------------------------------------------------------

func TestMarkPlanCompleted_WritesPathWithStatus(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))
	if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseValidating); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}

	total := int64(2048)
	if err := db.MarkPlanCompleted(ctx, "p1", `C:\out\video.mp4`, 2048, &total); err != nil {
		t.Fatalf("完成失败: %v", err)
	}

	got := mustGet(t, db, "p1")
	if got.Status != PlanStatusCompleted {
		t.Errorf("状态 = %s，期望 completed", got.Status)
	}
	if got.FinalPath != `C:\out\video.mp4` {
		t.Errorf("final_path = %q", got.FinalPath)
	}
	if got.Progress != 100 {
		t.Errorf("progress = %v，期望 100（[05 §2.2]：只有 completed 允许 100）", got.Progress)
	}
	if got.CompletedAt == nil {
		t.Error("completed_at 未写入")
	}
	if got.Phase != PlanPhaseNone {
		t.Errorf("phase = %q，期望空串", got.Phase)
	}
}

func TestMarkPlanCompleted_RejectsEmptyPath(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))
	if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseValidating); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}
	if err := db.MarkPlanCompleted(ctx, "p1", "   ", 1, nil); err == nil {
		t.Fatal("空 final_path 被接受——[03 §2.1] 要求 completed 必须有路径")
	}
	// 拒绝必须是整笔回滚：记录仍是 running，没有半成品终态。
	if got := mustGet(t, db, "p1"); got.Status != PlanStatusRunning {
		t.Errorf("状态 = %s，期望仍为 running", got.Status)
	}
}

// 用触发器模拟"状态写进去了但路径没写"的坏情况：事务内的一致性断言必须让整笔写入回滚。
// 这正是 [03 §2.1]"写入与校验必须同事务完成"要防的那种记录。
func TestMarkPlanCompleted_RollsBackWhenPathMissing(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))
	if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseValidating); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}

	_, err := db.SQL().ExecContext(ctx, `CREATE TRIGGER break_final_path AFTER UPDATE OF status ON plans
		WHEN NEW.status = 'completed'
		BEGIN UPDATE plans SET final_path = NULL WHERE id = NEW.id; END`)
	if err != nil {
		t.Fatalf("创建触发器失败: %v", err)
	}

	if err := db.MarkPlanCompleted(ctx, "p1", `C:\out\video.mp4`, 1, nil); err == nil {
		t.Fatal("缺少 final_path 的 completed 记录被提交，应当回滚")
	}

	got := mustGet(t, db, "p1")
	if got.Status != PlanStatusRunning {
		t.Errorf("状态 = %s，期望回滚为 running", got.Status)
	}
	if got.FinalPath != "" {
		t.Errorf("final_path = %q，期望为空", got.FinalPath)
	}
}

// ---------------------------------------------------------------------------
// 进度写入（[05 §4.1]、[05 §2.2]）
// ---------------------------------------------------------------------------

func TestUpdateProgress_KeepsZeroWhenTotalUnknown(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))
	if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseDownloading); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}

	if err := db.UpdateProgress(ctx, "p1", 12345, nil, PlanPhaseDownloading, "正在下载"); err != nil {
		t.Fatalf("UpdateProgress 失败: %v", err)
	}

	got := mustGet(t, db, "p1")
	if got.TotalBytes != nil {
		t.Errorf("total_bytes = %v，期望 NULL（长度未知）", *got.TotalBytes)
	}
	if got.Progress != 0 {
		t.Errorf("progress = %v，期望 0——[05 §4.1] 要求不得伪造百分比", got.Progress)
	}
	if got.DownloadedBytes != 12345 {
		t.Errorf("downloaded_bytes = %d，期望 12345", got.DownloadedBytes)
	}
}

func TestUpdateProgress_CapsBelowFullWhileRunning(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))
	if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseDownloading); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}

	total := int64(1000)
	// 服务端声明的总长度偏小，已下载超过它。
	if err := db.UpdateProgress(ctx, "p1", 1200, &total, PlanPhaseDownloading, "正在下载"); err != nil {
		t.Fatalf("UpdateProgress 失败: %v", err)
	}

	got := mustGet(t, db, "p1")
	if got.Progress >= 100 {
		t.Errorf("progress = %v，running 期间不得达到 100（B-312）", got.Progress)
	}
	if got.Progress <= 0 {
		t.Errorf("progress = %v，期望接近 99.9", got.Progress)
	}
}

func TestUpdateProgress_RejectsPathOrURLInDetail(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))
	if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseDownloading); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}

	total := int64(100)
	for _, detail := range []string{
		`正在下载 https://cdn.example.com/a.mp4`,
		`写入 C:\Users\me\Downloads\a.mp4`,
		"正在下载 /tmp/a.mp4",
	} {
		if err := db.UpdateProgress(ctx, "p1", 1, &total, PlanPhaseDownloading, detail); err == nil {
			t.Errorf("phase_detail = %q 被接受，应当拒绝（[03 §2.1]）", detail)
		}
	}

	if got := mustGet(t, db, "p1"); got.PhaseDetail != "" {
		t.Errorf("phase_detail = %q，非法的文案不应落库", got.PhaseDetail)
	}
}

func TestUpdateProgress_IgnoredAfterCancel(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))
	if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseDownloading); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}
	if err := db.MarkPlanCanceled(ctx, "p1"); err != nil {
		t.Fatalf("取消失败: %v", err)
	}

	total := int64(100)
	err := db.UpdateProgress(ctx, "p1", 50, &total, PlanPhaseDownloading, "正在下载")
	if !errors.Is(err, ErrPlanStateChanged) {
		t.Fatalf("取消后写进度返回 %v，期望 ErrPlanStateChanged", err)
	}
	if got := mustGet(t, db, "p1"); got.DownloadedBytes != 0 {
		t.Errorf("downloaded_bytes = %d，取消后不得再写进度", got.DownloadedBytes)
	}
}

// ---------------------------------------------------------------------------
// 列表与调度查询的索引约束（[03 §5] 的 Q1~Q3）
// ---------------------------------------------------------------------------

// assertUsesIndex 断言查询计划命中指定索引，且**既没有全表扫描也没有临时 B 树**。
//
// 注意 `SCAN plans USING INDEX x` 是"按索引顺序扫描"，正是我们要的计划；
// 裸的 `SCAN plans`（不带 USING INDEX）才是全表扫描（[03 §5] 的 Q2 明令禁止退化成它）。
func assertUsesIndex(t *testing.T, details []string, index string) {
	t.Helper()
	for _, detail := range details {
		line := strings.TrimSpace(detail)
		if strings.HasPrefix(line, "SCAN plans") && !strings.Contains(line, "USING INDEX") {
			t.Errorf("查询退化为全表扫描：%s", line)
		}
		if strings.Contains(line, "TEMP B-TREE") {
			t.Errorf("查询出现临时 B 树：%s", line)
		}
	}
	joined := strings.Join(details, " | ")
	if !strings.Contains(joined, index) {
		t.Errorf("查询计划未命中 %s：%s", index, joined)
	}
}

func TestListPlans_UsesUpdatedIndex(t *testing.T) {
	db := openPlanTestDB(t)

	query, args, err := planListQuery(nil, 0, 0)
	if err != nil {
		t.Fatalf("构造查询失败: %v", err)
	}
	assertUsesIndex(t, explainPlanQuery(t, db, query, args...), "idx_plans_updated")
}

func TestListPlans_BySingleStatusUsesStatusIndex(t *testing.T) {
	db := openPlanTestDB(t)

	query, args, err := planListQuery([]string{PlanStatusFailed}, 0, 0)
	if err != nil {
		t.Fatalf("构造查询失败: %v", err)
	}
	assertUsesIndex(t, explainPlanQuery(t, db, query, args...), "idx_plans_status")
}

func TestListDuePlans_UsesDueIndex(t *testing.T) {
	db := openPlanTestDB(t)
	assertUsesIndex(t, explainPlanQuery(t, db, duePlansQuery(), 1_800_000_000.0, 10), "idx_plans_due")
}

func TestListDuePlans_ReturnsOnlyDueQueued(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()

	fresh := samplePlan("p-fresh") // next_attempt_at = NULL → 首次调度
	mustInsert(t, db, fresh)

	due := samplePlan("p-due")
	past := 1_000.0
	due.NextAttemptAt = &past
	mustInsert(t, db, due)

	future := samplePlan("p-future")
	later := 9_999_999_999.0
	future.NextAttemptAt = &later
	mustInsert(t, db, future)

	running := samplePlan("p-running")
	mustInsert(t, db, running)
	if err := db.MarkPlanRunning(ctx, "p-running", PlanPhaseDownloading); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}

	got, err := db.ListDuePlans(ctx, 2_000.0, 10)
	if err != nil {
		t.Fatalf("ListDuePlans 失败: %v", err)
	}

	ids := make(map[string]bool, len(got))
	for _, p := range got {
		ids[p.ID] = true
	}
	if !ids["p-fresh"] || !ids["p-due"] {
		t.Errorf("到期计划未被返回：%v", ids)
	}
	if ids["p-future"] {
		t.Error("未到期的计划被返回了")
	}
	if ids["p-running"] {
		t.Error("running 计划被调度查询返回了（[03 §5] 的 Q3 只取 queued）")
	}
}

func TestListDuePlans_OrdersByNextAttempt(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()

	for i, at := range []float64{300, 100, 200} {
		p := samplePlan("p" + string(rune('a'+i)))
		value := at
		p.NextAttemptAt = &value
		mustInsert(t, db, p)
	}

	got, err := db.ListDuePlans(ctx, 1_000, 10)
	if err != nil {
		t.Fatalf("ListDuePlans 失败: %v", err)
	}
	for i := 1; i < len(got); i++ {
		prev, cur := *got[i-1].NextAttemptAt, *got[i].NextAttemptAt
		if prev > cur {
			t.Fatalf("返回顺序未按 next_attempt_at 升序：%v", got)
		}
	}
}

func TestListPlans_ClampsLimitAndOrdersByUpdatedDesc(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		p := samplePlan("p" + string(rune('a'+i)))
		p.CreatedAt = 1000 + float64(i)
		mustInsert(t, db, p)
	}

	got, err := db.ListPlans(ctx, nil, 0, 10_000)
	if err != nil {
		t.Fatalf("ListPlans 失败: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("返回 %d 条，期望 3", len(got))
	}
	if got[0].ID != "pc" {
		t.Errorf("首条 = %s，期望最后更新的 pc（updated_at DESC）", got[0].ID)
	}

	// limit 上限必须被夹到 [03 §4.5] 的 200。
	if _, args, err := planListQuery(nil, 0, 10_000); err != nil {
		t.Fatalf("构造查询失败: %v", err)
	} else if args[len(args)-2] != MaxPlanListLimit {
		t.Errorf("limit 参数 = %v，期望被夹到 %d", args[len(args)-2], MaxPlanListLimit)
	}
}

func TestListPlans_RejectsInvalidStatus(t *testing.T) {
	db := openPlanTestDB(t)
	if _, err := db.ListPlans(context.Background(), []string{"bogus"}, 0, 10); err == nil {
		t.Fatal("非法状态筛选值被接受，应当拒绝")
	}
}

func TestCountPlans_FiltersByStatus(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))
	mustInsert(t, db, samplePlan("p2"))

	total, err := db.CountPlans(ctx, nil)
	if err != nil {
		t.Fatalf("CountPlans 失败: %v", err)
	}
	if total != 2 {
		t.Errorf("总数 = %d，期望 2", total)
	}

	failed, err := db.CountPlans(ctx, []string{PlanStatusFailed})
	if err != nil {
		t.Fatalf("CountPlans 失败: %v", err)
	}
	if failed != 0 {
		t.Errorf("failed 数 = %d，期望 0", failed)
	}
}

// ---------------------------------------------------------------------------
// 中断恢复（[05 §9]）
// ---------------------------------------------------------------------------

func TestRecoverInterrupted_RequeuesRebuildableRunning(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()

	mustInsert(t, db, samplePlan("p-direct"))
	if err := db.MarkPlanRunning(ctx, "p-direct", PlanPhaseDownloading); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}

	report, err := db.RecoverInterrupted(ctx, 5_000.0)
	if err != nil {
		t.Fatalf("RecoverInterrupted 失败: %v", err)
	}
	if len(report.RequeuedIDs) != 1 || report.RequeuedIDs[0] != "p-direct" {
		t.Fatalf("重新排队的计划 = %v，期望 [p-direct]", report.RequeuedIDs)
	}

	got := mustGet(t, db, "p-direct")
	if got.Status != PlanStatusQueued || got.Phase != PlanPhaseNone {
		t.Errorf("状态 = %s/%q，期望 queued 且无 phase", got.Status, got.Phase)
	}
	if got.NextAttemptAt == nil || *got.NextAttemptAt != 5_000.0 {
		t.Errorf("next_attempt_at = %v，期望立即调度（now）", got.NextAttemptAt)
	}
}

// 视频号会话与浏览器中转的上下文只在内存（[03 §6]），重启后不可恢复 → failed + context_expired。
func TestRecoverInterrupted_FailsUnrecoverableContext(t *testing.T) {
	cases := []struct {
		name      string
		mediaKind string
		sourceURL string
	}{
		{"视频号会话", PlanMediaWechat, "https://channels.weixin.qq.com/x"},
		{"浏览器中转", PlanMediaBrowser, "https://example.com/watch"},
		{"无来源地址", PlanMediaDirect, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openPlanTestDB(t)
			ctx := context.Background()

			p := samplePlan("p1")
			p.MediaKind = tc.mediaKind
			p.SourceURL = tc.sourceURL
			mustInsert(t, db, p)
			if err := db.MarkPlanRunning(ctx, "p1", PlanPhaseDownloading); err != nil {
				t.Fatalf("进入 running 失败: %v", err)
			}

			report, err := db.RecoverInterrupted(ctx, 5_000.0)
			if err != nil {
				t.Fatalf("RecoverInterrupted 失败: %v", err)
			}
			if len(report.FailedIDs) != 1 || len(report.RequeuedIDs) != 0 {
				t.Fatalf("处置结果 = %+v，期望只有 failed", report)
			}

			got := mustGet(t, db, "p1")
			if got.Status != PlanStatusFailed {
				t.Errorf("状态 = %s，期望 failed", got.Status)
			}
			if got.ErrorCode != CodeContextExpired {
				t.Errorf("error_code = %q，期望 %q（[05 §9]）", got.ErrorCode, CodeContextExpired)
			}
		})
	}
}

// 等待重试的计划同样可能依赖只在内存的上下文：不可恢复的必须转 failed，不得空转重试。
func TestRecoverInterrupted_FailsWaitingUnrecoverable(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()

	p := samplePlan("p-waiting")
	p.MediaKind = PlanMediaBrowser
	next := 9_999_999_999.0
	p.NextAttemptAt = &next
	mustInsert(t, db, p)

	report, err := db.RecoverInterrupted(ctx, 5_000.0)
	if err != nil {
		t.Fatalf("RecoverInterrupted 失败: %v", err)
	}
	if len(report.FailedIDs) != 1 {
		t.Fatalf("处置结果 = %+v，期望 failed", report)
	}
	if got := mustGet(t, db, "p-waiting"); got.ErrorCode != CodeContextExpired {
		t.Errorf("error_code = %q，期望 %q", got.ErrorCode, CodeContextExpired)
	}
}

// 可重建的等待计划必须**原样保留**（[05 §9]：继续调度），不能被打成 failed。
func TestRecoverInterrupted_KeepsRebuildableWaiting(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p-waiting"))

	report, err := db.RecoverInterrupted(ctx, 5_000.0)
	if err != nil {
		t.Fatalf("RecoverInterrupted 失败: %v", err)
	}
	if len(report.FailedIDs) != 0 || len(report.RequeuedIDs) != 0 {
		t.Fatalf("处置结果 = %+v，期望不触碰", report)
	}
	if got := mustGet(t, db, "p-waiting"); got.Status != PlanStatusQueued {
		t.Errorf("状态 = %s，期望 queued", got.Status)
	}
}

// completed 计划在恢复中不得被改写（[05 §9] 第 4 条：不得删除已完成计划的文件与状态）。
func TestRecoverInterrupted_LeavesTerminalRecordsAlone(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()

	mustInsert(t, db, samplePlan("p-done"))
	if err := db.MarkPlanRunning(ctx, "p-done", PlanPhaseValidating); err != nil {
		t.Fatalf("进入 running 失败: %v", err)
	}
	if err := db.MarkPlanCompleted(ctx, "p-done", `C:\out\video.mp4`, 10, nil); err != nil {
		t.Fatalf("完成失败: %v", err)
	}

	mustInsert(t, db, samplePlan("p-canceled"))
	if err := db.MarkPlanCanceled(ctx, "p-canceled"); err != nil {
		t.Fatalf("取消失败: %v", err)
	}

	report, err := db.RecoverInterrupted(ctx, 5_000.0)
	if err != nil {
		t.Fatalf("RecoverInterrupted 失败: %v", err)
	}
	if len(report.FailedIDs) != 0 || len(report.RequeuedIDs) != 0 {
		t.Fatalf("终态记录被改动了：%+v", report)
	}
	if got := mustGet(t, db, "p-done"); got.Status != PlanStatusCompleted || got.FinalPath == "" {
		t.Errorf("已完成计划被改写：%+v", got)
	}
}

// ---------------------------------------------------------------------------
// 删除与查询
// ---------------------------------------------------------------------------

func TestGetPlan_NotFound(t *testing.T) {
	db := openPlanTestDB(t)
	if _, err := db.GetPlan(context.Background(), "missing"); !errors.Is(err, ErrPlanNotFound) {
		t.Errorf("返回 %v，期望 ErrPlanNotFound", err)
	}
}

func TestRemovePlan_NotFound(t *testing.T) {
	db := openPlanTestDB(t)
	if err := db.RemovePlan(context.Background(), "missing"); !errors.Is(err, ErrPlanNotFound) {
		t.Errorf("返回 %v，期望 ErrPlanNotFound", err)
	}
}

func TestRemovePlan_DeletesRow(t *testing.T) {
	db := openPlanTestDB(t)
	ctx := context.Background()
	mustInsert(t, db, samplePlan("p1"))

	if err := db.RemovePlan(ctx, "p1"); err != nil {
		t.Fatalf("RemovePlan 失败: %v", err)
	}
	if _, err := db.GetPlan(ctx, "p1"); !errors.Is(err, ErrPlanNotFound) {
		t.Errorf("删除后仍能读到记录：%v", err)
	}
}

func TestPlanIsTerminal(t *testing.T) {
	cases := map[string]bool{
		PlanStatusQueued:    false,
		PlanStatusRunning:   false,
		PlanStatusCompleted: true,
		PlanStatusFailed:    true,
		PlanStatusCanceled:  true,
	}
	for status, want := range cases {
		if got := (Plan{Status: status}).IsTerminal(); got != want {
			t.Errorf("IsTerminal(%s) = %v，期望 %v", status, got, want)
		}
	}
}

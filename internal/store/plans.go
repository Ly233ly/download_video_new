package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// plans 表的仓储（[03 §2.1]）。
//
// 本文件只负责**持久化与数据不变量**，不含业务决策——[04 §1.2] 规定 Service 持有全部业务规则。
// 具体分工：
//   - "从哪个状态能转到哪个状态"由 SQL 的 WHERE 守卫表达（[05 §2.1]），不满足时返回 ErrPlanStateChanged，
//     由 service 决定是忽略（终态已定）还是报错；
//   - 何时重试、退避多久、进度写库如何节流、何时推送事件，全部在 service 层决定。
//
// 时间一律是 REAL 类型的 Unix 秒（[03 §1] 的 P3），沿用同包的 unixSeconds()。

// plans.status 的 5 个取值（[03 §3.1]）。**刻意只有 5 个**：下载与导入彻底分离，
// 这里不存在 ready_to_import / imported 之类的导入语义状态。
const (
	PlanStatusQueued    = "queued"
	PlanStatusRunning   = "running"
	PlanStatusCompleted = "completed"
	PlanStatusFailed    = "failed"
	PlanStatusCanceled  = "canceled"
)

// plans.phase 的取值（[03 §2.1]）。空串表示"当前不在运行中"——[03 §2.1] 要求
// "phase 只在 running 内有值"，所以队列中与终态记录的 phase 必须是空串。
const (
	PlanPhaseNone        = ""
	PlanPhaseDownloading = "downloading"
	PlanPhaseMerging     = "merging"
	PlanPhaseValidating  = "validating"
)

// plans.media_kind 的取值（[03 §3.3]）。
const (
	PlanMediaDirect  = "direct"
	PlanMediaHLS     = "hls"
	PlanMediaDASH    = "dash"
	PlanMediaPage    = "page"
	PlanMediaWechat  = "wechat"
	PlanMediaBrowser = "browser"
)

// plans.output_container 的取值（[03 §3.3]）。
const (
	PlanContainerMP4  = "mp4"
	PlanContainerMKV  = "mkv"
	PlanContainerWebM = "webm"
	PlanContainerM4A  = "m4a"
	PlanContainerMP3  = "mp3"
	PlanContainerTS   = "ts"
)

// plans.merge_mode 的取值（[03 §3.3]）。
const (
	PlanMergeSingle    = "single"
	PlanMergeAV        = "av"
	PlanMergeSubtitles = "subtitles"
)

const (
	// MaxPlanListLimit 是单次返回计划数的上限（[03 §4.5]：200）。
	// 它是**硬上限**，调用方给再大的值也会被夹到它——[03 §1] 的 P8 要求所有列表查询有 LIMIT。
	MaxPlanListLimit = 200
	// DefaultPlanListLimit 是调用方没给 limit（<= 0）时的默认页大小。
	DefaultPlanListLimit = 50
	// MaxErrorMessageLen 是错误消息长度上限（[03 §4.5]：1000 字符，超出截断）。
	MaxErrorMessageLen = 1000
	// MaxSourceURLLen 是页面 URL 长度上限（[03 §4.5]：2048 字符）。
	MaxSourceURLLen = 2048
	// MaxOutputNameLen 是输出名长度上限（[03 §4.4]：150 字符）。
	MaxOutputNameLen = 150
)

var (
	// ErrPlanNotFound 表示计划不存在。service 映射为错误码 plan_not_found（[05 §7.3]）。
	ErrPlanNotFound = errors.New("计划不存在")
	// ErrPlanStateChanged 表示记录存在但当前状态不满足转换守卫——[05 §2.2] 的终态不可被覆盖，
	// 典型场景是 B-308：用户已取消，完成回调不得再写 completed。
	ErrPlanStateChanged = errors.New("计划状态已变更")
)

// CodeContextExpired 是 [05 §7.2] 的错误码：上下文已失效（仅内存的会话无法重建）。
// 它由本包在中断恢复时写入，因此定义在这里而不是 service——避免两处各写一份字面量。
const CodeContextExpired = "context_expired"

// Plan 是 plans 表的一行，字段与列一一对应（[03 §2.1]）。
type Plan struct {
	ID                string
	SourceURL         string
	SourceTitle       string
	MediaKind         string
	OutputName        string
	OutputContainer   string
	MergeMode         string
	QualityLabel      string
	StreamPlan        string
	ImportToEagle     bool
	DeleteAfterImport bool

	Status   string
	Phase    string
	Progress float64
	// DownloadedBytes 是已落盘字节数。重试时**不重置**——P1 支持 Range 续传（[05 §4.1]）。
	DownloadedBytes int64
	// TotalBytes 为 nil 表示"长度未知"。[03 §2.1] 明确：**不得用 0 表示未知**，
	// 因为 0 是"长度为零"，两者语义不同。所以这里用指针而不是 int64。
	TotalBytes  *int64
	PhaseDetail string

	// FinalPath 为空串表示列为 NULL（尚未交付）。[03 §2.1] 要求
	// status = completed 时它必须非空，且"写入与校验必须同事务完成"。
	FinalPath   string
	PreviewPath string

	AttemptCount  int
	NextAttemptAt *float64
	ErrorCode     string
	ErrorMessage  string

	CreatedAt   float64
	UpdatedAt   float64
	CompletedAt *float64
}

// IsTerminal 报告状态是否终态（[03 §3.1]：completed / failed / canceled）。
// 终态记录不再被自动调度，failed 只能手动重试（[05 §2.2]）。
func (p Plan) IsTerminal() bool {
	switch p.Status {
	case PlanStatusCompleted, PlanStatusFailed, PlanStatusCanceled:
		return true
	default:
		return false
	}
}

// RecoveryReport 是中断恢复的处置结果（[05 §9]），供 service 逐条发事件。
type RecoveryReport struct {
	// RequeuedIDs 是上下文可重建、已转回 queued 并立即调度的计划。
	RequeuedIDs []string
	// FailedIDs 是上下文不可恢复、已转 failed（错误码 context_expired）的计划。
	FailedIDs []string
}

// planColumns 是 plans 表的全部列，顺序与 scanPlan 的扫描顺序一一对应。
// 用显式列名而不是 SELECT *：列顺序变化时扫描会立刻报错，而不是把值静默装错字段（[12 §3] 的 E1）。
const planColumns = `id, source_url, source_title, media_kind, output_name, output_container,
	merge_mode, quality_label, stream_plan, import_to_eagle, delete_after_import,
	status, phase, progress, downloaded_bytes, total_bytes, phase_detail,
	final_path, preview_path, attempt_count, next_attempt_at,
	error_code, error_message, created_at, updated_at, completed_at`

// 取值域白名单，用于写入前的输入合法性校验（[03 §3]，不是业务判断）。
var (
	validPlanMediaKinds = map[string]bool{
		PlanMediaDirect: true, PlanMediaHLS: true, PlanMediaDASH: true,
		PlanMediaPage: true, PlanMediaWechat: true, PlanMediaBrowser: true,
	}
	validPlanContainers = map[string]bool{
		PlanContainerMP4: true, PlanContainerMKV: true, PlanContainerWebM: true,
		PlanContainerM4A: true, PlanContainerMP3: true, PlanContainerTS: true,
	}
	validPlanMergeModes = map[string]bool{
		PlanMergeSingle: true, PlanMergeAV: true, PlanMergeSubtitles: true,
	}
	validPlanStatuses = map[string]bool{
		PlanStatusQueued: true, PlanStatusRunning: true, PlanStatusCompleted: true,
		PlanStatusFailed: true, PlanStatusCanceled: true,
	}
	validPlanPhases = map[string]bool{
		PlanPhaseNone: true, PlanPhaseDownloading: true,
		PlanPhaseMerging: true, PlanPhaseValidating: true,
	}
)

// rowScanner 让 scanPlan 同时适用于 *sql.Row 与 *sql.Rows。
type rowScanner interface {
	Scan(dest ...any) error
}

// InsertPlan 插入一条计划。
//
// 写入前校验 [03 §2.1] 的全部数据不变量（终态必须有路径/错误码、phase 与 status 的搭配、
// total_bytes 的三态），**不合法的记录一律不落库**。
func (db *DB) InsertPlan(ctx context.Context, p Plan) error {
	if p.StreamPlan == "" {
		p.StreamPlan = "[]"
	}
	if err := validatePlanForWrite(p); err != nil {
		return err
	}
	if p.CreatedAt <= 0 {
		p.CreatedAt = unixSeconds()
	}
	p.UpdatedAt = p.CreatedAt

	_, err := db.sql.ExecContext(ctx, `INSERT INTO plans (`+planColumns+`) VALUES (
		?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.SourceURL, p.SourceTitle, p.MediaKind, p.OutputName, p.OutputContainer,
		p.MergeMode, p.QualityLabel, p.StreamPlan, boolToInt(p.ImportToEagle), boolToInt(p.DeleteAfterImport),
		p.Status, p.Phase, p.Progress, p.DownloadedBytes, nullableInt(p.TotalBytes), p.PhaseDetail,
		nullableString(p.FinalPath), nullableString(p.PreviewPath), p.AttemptCount, nullableFloat(p.NextAttemptAt),
		nullableString(p.ErrorCode), nullableString(p.ErrorMessage), p.CreatedAt, p.UpdatedAt, nullableFloat(p.CompletedAt))
	if err != nil {
		return fmt.Errorf("写入计划失败: %w", err)
	}
	return nil
}

// GetPlan 读取一条计划；不存在时返回 ErrPlanNotFound。
func (db *DB) GetPlan(ctx context.Context, id string) (Plan, error) {
	if strings.TrimSpace(id) == "" {
		return Plan{}, ErrPlanNotFound
	}
	row := db.sql.QueryRowContext(ctx, `SELECT `+planColumns+` FROM plans WHERE id = ?`, id)
	p, err := scanPlan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Plan{}, ErrPlanNotFound
	}
	if err != nil {
		return Plan{}, fmt.Errorf("读取计划失败: %w", err)
	}
	return p, nil
}

// ListPlans 按 updated_at 倒序返回计划。
//
// 索引约束（[03 §5] 的 Q1/Q2）：
//   - statuses 为空 → 纯 `ORDER BY updated_at DESC`，命中 `idx_plans_updated`，**不产生临时 B 树**；
//   - 单个状态 → `WHERE status = ?`，由 `idx_plans_status (status, updated_at DESC)` 直接服务；
//   - 多个状态 → SQLite 只能退化为扫描 + 排序。**这是引擎的固有限制，不是这里的取舍**；
//     界面要"多状态"视图时建议按状态分别取（或只取"全部"），不要在一个查询里混合多个状态。
//
// 任何情况下都有 LIMIT（[03 §1] 的 P8），上限 MaxPlanListLimit。
func (db *DB) ListPlans(ctx context.Context, statuses []string, offset, limit int) ([]Plan, error) {
	query, args, err := planListQuery(statuses, offset, limit)
	if err != nil {
		return nil, err
	}
	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("读取计划列表失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	plans, err := collectPlans(rows)
	if err != nil {
		return nil, fmt.Errorf("读取计划列表失败: %w", err)
	}
	return plans, nil
}

// planListQuery 构造列表查询并夹好分页参数。
//
// 抽成独立函数是为了让测试对**真实的 SQL** 跑 EXPLAIN QUERY PLAN：如果测试另写一份
// 形状相似的 SQL，索引断言就会与实现漂移，而 [03 §5] 的 Q2 是本阶段的硬要求。
func planListQuery(statuses []string, offset, limit int) (string, []any, error) {
	where, args, err := planStatusFilter(statuses)
	if err != nil {
		return "", nil, err
	}
	limit, offset = ClampPlanPaging(offset, limit)
	query := `SELECT ` + planColumns + ` FROM plans` + where + ` ORDER BY updated_at DESC LIMIT ? OFFSET ?`
	return query, append(args, limit, offset), nil
}

// CountPlans 返回满足状态筛选的计划总数，供分页信息使用（[04 §3.1] 的 Paged）。
//
// 它不是"列表查询"，不返回行、不受 MaxPlanListLimit 约束；但仍按状态走索引。
func (db *DB) CountPlans(ctx context.Context, statuses []string) (int, error) {
	where, args, err := planStatusFilter(statuses)
	if err != nil {
		return 0, err
	}
	var total int
	if err := db.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM plans`+where, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("统计计划数量失败: %w", err)
	}
	return total, nil
}

// RemovePlan 删除计划记录。
//
// 只删记录，**不碰文件**：`已完成` 目录里的产物是用户资产（[05 §11]、B-402/B-405）。
// 运行中的计划不应走到这里——service 会先拒绝（[05 §10]）。
func (db *DB) RemovePlan(ctx context.Context, id string) error {
	res, err := db.sql.ExecContext(ctx, `DELETE FROM plans WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("删除计划失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("删除计划失败: %w", err)
	}
	if n == 0 {
		return ErrPlanNotFound
	}
	return nil
}

// MarkPlanRunning 把 queued 计划置为 running 并写入 phase（[05 §2.1]）。
//
// 守卫 `status = 'queued'` 保证：已取消/已完成/已失败的计划**不会**被重新拉起来（B-308 的第一道闸）。
// 同时清空 next_attempt_at（本次调度已消费）与上一次的错误字段。
func (db *DB) MarkPlanRunning(ctx context.Context, id, phase string) error {
	if phase == PlanPhaseNone || !validPlanPhases[phase] {
		return fmt.Errorf("运行中的计划必须有合法 phase，收到 %q（[05 §2.2]）", phase)
	}
	now := unixSeconds()
	return db.execPlanTransition(ctx, id, `UPDATE plans
		SET status = ?, phase = ?, next_attempt_at = NULL,
		    error_code = NULL, error_message = NULL, updated_at = ?
		WHERE id = ? AND status = ?`,
		PlanStatusRunning, phase, now, id, PlanStatusQueued)
}

// MarkPlanCompleted 把 running 计划置为 completed，并**在同一条语句里**写入 final_path。
//
// [03 §2.1] 的硬要求：`status = 'completed'` 时 `final_path` 必须非空，且
// "写入与校验必须同事务完成，不允许出现已完成但没有路径的记录"。
// 这里用显式事务 + 事务内一致性断言把这条要求做成机械保证，而不只是约定。
//
// 守卫只接受 `status = 'running'`：这是 B-308 的关键一环——用户在校验阶段停止后记录已是
// canceled（终态），本函数返回 ErrPlanStateChanged，完成回调**不得覆盖取消状态**（[05 §2.2]）。
func (db *DB) MarkPlanCompleted(ctx context.Context, id, finalPath string, downloaded int64, total *int64) error {
	if strings.TrimSpace(finalPath) == "" {
		// 这是编程错误（[12 §3.2]：调用方必须先拿到交付路径），直接拒绝而不是写一条坏记录。
		return errors.New("completed 计划必须有非空的 final_path（[03 §2.1]）")
	}
	if downloaded < 0 {
		return errors.New("已下载字节数不得为负")
	}
	if total != nil && *total < 0 {
		return errors.New("总字节数不得为负")
	}

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("更新计划状态失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := unixSeconds()
	// progress 固定写 100：[05 §2.2] 规定"只有 completed 允许 progress = 100"。
	res, err := tx.ExecContext(ctx, `UPDATE plans
		SET status = ?, final_path = ?, progress = 100, downloaded_bytes = ?, total_bytes = ?,
		    phase = '', phase_detail = '', error_code = NULL, error_message = NULL,
		    next_attempt_at = NULL, completed_at = ?, updated_at = ?
		WHERE id = ? AND status = ?`,
		PlanStatusCompleted, finalPath, downloaded, nullableInt(total), now, now, id, PlanStatusRunning)
	if err != nil {
		return fmt.Errorf("更新计划状态失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("更新计划状态失败: %w", err)
	}
	if n == 0 {
		return db.classifyMissingPlan(ctx, id)
	}

	// 事务内一致性断言：completed 的记录**必须**带非空路径（[03 §2.1]）。
	// 万一将来有人改了上面的 SQL，这里会回滚整笔写入，而不是留下一行坏数据。
	var ok int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM plans
		WHERE id = ? AND status = ? AND final_path IS NOT NULL AND final_path <> ''`,
		id, PlanStatusCompleted).Scan(&ok); err != nil {
		return fmt.Errorf("校验计划完成状态失败: %w", err)
	}
	if ok != 1 {
		return errors.New("completed 计划缺少 final_path，已回滚（[03 §2.1]）")
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("更新计划状态失败: %w", err)
	}
	return nil
}

// MarkPlanFailed 把非终态计划置为 failed，并写入稳定错误码与可安全展示的消息（[05 §7.4]）。
//
// 守卫 `status IN ('queued','running')`：终态（含 canceled）**不可被覆盖**——用户点了停止之后，
// 后台线程迟到的失败回调会拿到 ErrPlanStateChanged，由调用方忽略（B-308）。
func (db *DB) MarkPlanFailed(ctx context.Context, id, code, message string) error {
	if strings.TrimSpace(code) == "" {
		// [03 §2.1]：status = 'failed' 时 error_code 必须非空。
		return errors.New("failed 计划必须有 error_code（[03 §2.1]）")
	}
	now := unixSeconds()
	return db.execPlanTransition(ctx, id, `UPDATE plans
		SET status = ?, phase = '', error_code = ?, error_message = ?,
		    next_attempt_at = NULL, updated_at = ?
		WHERE id = ? AND status IN (?, ?)`,
		PlanStatusFailed, code, truncateRunes(message, MaxErrorMessageLen), now,
		id, PlanStatusQueued, PlanStatusRunning)
}

// MarkPlanCanceled 把非终态计划置为 canceled（[05 §10]）。
//
// 已完成或已失败的计划不会被改写：文件已经交付，停止不该影响它（[05 §10]）。
func (db *DB) MarkPlanCanceled(ctx context.Context, id string) error {
	now := unixSeconds()
	return db.execPlanTransition(ctx, id, `UPDATE plans
		SET status = ?, phase = '', next_attempt_at = NULL, updated_at = ?
		WHERE id = ? AND status IN (?, ?)`,
		PlanStatusCanceled, now, id, PlanStatusQueued, PlanStatusRunning)
}

// MarkPlanQueuedForRetry 把可重试失败转回 queued，并持久化重试计数与下次尝试时间（B-315）。
//
// [05 §8] 要求 attempt_count 与 next_attempt_at **重启不重置**——所以这两个值是写库参数，
// 而不是内存状态。phase 清空（[03 §2.1]：phase 只在 running 内有值）。
//
// attempt 有两种来源，都落在 attempt_count 这一列上：
//   - 自动重试：已发生的尝试次数（>= 1），同时被 [05 §8] 的退避公式消费；
//   - 手动重试：传 0，表示**重置自动重试预算**。否则一次耗尽之后"手动重试"只能再跑一次，
//     用户得反复点；计数仍然落库，B-315 要求的是"重启后不得重置"，不是"永不清零"。
//
// 守卫包含 `failed`：这是手动重试的入口（[05 §2.2]：failed 只能手动重试）。
// canceled 与 completed 不在其中——终态里只有 failed 可以被用户重新排队（B-308）。
func (db *DB) MarkPlanQueuedForRetry(ctx context.Context, id string, attempt int, nextAttemptAt float64) error {
	if attempt < 0 {
		return errors.New("重试计数不得为负")
	}
	if nextAttemptAt <= 0 {
		return errors.New("下次尝试时间必须是有效的 Unix 秒")
	}
	now := unixSeconds()
	return db.execPlanTransition(ctx, id, `UPDATE plans
		SET status = ?, phase = '', attempt_count = ?, next_attempt_at = ?,
		    error_code = NULL, error_message = NULL, updated_at = ?
		WHERE id = ? AND status IN (?, ?, ?)`,
		PlanStatusQueued, attempt, nextAttemptAt, now, id,
		PlanStatusQueued, PlanStatusRunning, PlanStatusFailed)
}

// UpdateProgress 写一次进度快照。**节流由调用方负责**（[05 §1]：高频进度写入做节流）。
//
// 三条派生规则在这里落地，因为它们都是数据完整性的一部分：
//   - total 未知（nil）时 progress 保持 0——[05 §4.1] 要求"不伪造百分比"；
//   - running 期间 progress 封顶 99.9——[05 §2.2] 规定"只有 completed 允许 progress = 100"；
//   - phase_detail 只允许**可直接展示的短文案**，含 URL 或路径的一律拒绝（[03 §2.1]）。
func (db *DB) UpdateProgress(ctx context.Context, id string, downloaded int64, total *int64, phase, phaseDetail string) error {
	if downloaded < 0 {
		return errors.New("已下载字节数不得为负")
	}
	if total != nil && *total < 0 {
		return errors.New("总字节数不得为负")
	}
	if phase == PlanPhaseNone || !validPlanPhases[phase] {
		return fmt.Errorf("运行中的计划必须有合法 phase，收到 %q（[05 §2.2]）", phase)
	}
	if err := validatePhaseDetail(phaseDetail); err != nil {
		return err
	}
	now := unixSeconds()
	return db.execPlanTransition(ctx, id, `UPDATE plans
		SET downloaded_bytes = ?, total_bytes = ?, progress = ?, phase = ?, phase_detail = ?, updated_at = ?
		WHERE id = ? AND status = ?`,
		downloaded, nullableInt(total), runningProgress(downloaded, total), phase, phaseDetail, now,
		id, PlanStatusRunning)
}

// ListDuePlans 返回到期可调度的计划。
//
// SQL 形状**照抄 [03 §5] 的 Q3**，以便命中部分索引 `idx_plans_due`：
//
//	WHERE status = 'queued' AND (next_attempt_at IS NULL OR next_attempt_at <= ?) ORDER BY next_attempt_at
//
// `status = 'queued'` 必须是**字面量**：部分索引的谓词要与查询条件逐字匹配，SQLite 才会用它。
// next_attempt_at IS NULL 表示"刚创建、首次调度"（[05 §2.1]）。
func (db *DB) ListDuePlans(ctx context.Context, now float64, limit int) ([]Plan, error) {
	rows, err := db.sql.QueryContext(ctx, duePlansQuery(), now, clampDueLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("读取待调度计划失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	plans, err := collectPlans(rows)
	if err != nil {
		return nil, fmt.Errorf("读取待调度计划失败: %w", err)
	}
	return plans, nil
}

// duePlansQuery 是调度查询的**唯一**形状（[03 §5] 的 Q3）。
// 抽成函数同样是为了让索引断言跑在真实 SQL 上。
//
// `INDEXED BY idx_plans_due` 是刻意写上的，不是可有可无的提示：Q3 要求这条查询必须命中
// idx_plans_due，而实测（本机 SQLite，见 plans_test.go）在只给 WHERE 形状时优化器会选
// idx_plans_status 并退化为 `USE TEMP B-TREE FOR ORDER BY`。写上索引名把这条性能要求变成
// 机械保证，同时让"索引没了"立刻变成查询错误（快速失败），而不是悄悄退化成扫描 + 排序。
// WHERE 与 ORDER BY 仍然逐字保持 Q3 给定的形状。
func duePlansQuery() string {
	return `SELECT ` + planColumns + ` FROM plans INDEXED BY idx_plans_due
		WHERE status = 'queued' AND (next_attempt_at IS NULL OR next_attempt_at <= ?)
		ORDER BY next_attempt_at
		LIMIT ?`
}

// clampDueLimit 与 ClampPlanPaging 共用同一组上限，避免调度查询出现无界 LIMIT。
func clampDueLimit(limit int) int {
	limit, _ = ClampPlanPaging(0, limit)
	return limit
}

// RecoverInterrupted 执行 [05 §9] 的中断恢复：程序重启后内存里的会话上下文已经全部消失
// （[03 §6]：媒体地址、Cookie、解密键都不落盘），因此必须逐条判断还能不能继续。
//
// 处置规则（[05 §9] 的两张表合一）：
//   - running + 上下文可重建（直链、可重新解析的页面）→ queued，next_attempt_at = now；
//   - running + 上下文不可恢复（视频号会话、浏览器中转、无来源地址）→ failed + context_expired；
//   - queued + 上下文不可恢复 → failed + context_expired（**不得**让任务空转重试）；
//   - queued + 上下文可重建 → 保持不动，继续调度。
//
// 本函数**不碰文件系统**：临时产物的清理必须建立在"明确归属"之上（[05 §9] 第 3 条、B-404），
// 那属于下载与交付链路的职责。completed 计划的文件更不会被这里触碰（[05 §9] 第 4 条）。
func (db *DB) RecoverInterrupted(ctx context.Context, now float64) (RecoveryReport, error) {
	var report RecoveryReport

	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return report, fmt.Errorf("中断恢复失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `SELECT id, media_kind, status, source_url FROM plans
		WHERE status IN (?, ?) ORDER BY updated_at`, PlanStatusRunning, PlanStatusQueued)
	if err != nil {
		return report, fmt.Errorf("中断恢复失败: %w", err)
	}

	type pending struct {
		id      string
		requeue bool
	}
	var todo []pending
	for rows.Next() {
		var id, mediaKind, status, sourceURL string
		if err := rows.Scan(&id, &mediaKind, &status, &sourceURL); err != nil {
			_ = rows.Close()
			return report, fmt.Errorf("中断恢复失败: %w", err)
		}
		recoverable := contextRebuildable(mediaKind, sourceURL)
		switch {
		case status == PlanStatusRunning:
			// running 的计划一律先离开 running：要么继续，要么明确失败。
			todo = append(todo, pending{id: id, requeue: recoverable})
		case !recoverable:
			// queued 但上下文不可恢复：保守置 failed，不让它空转重试（[05 §9]）。
			todo = append(todo, pending{id: id, requeue: false})
		default:
			// queued 且可重建：保持原样，交给调度循环。
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return report, fmt.Errorf("中断恢复失败: %w", err)
	}
	_ = rows.Close()

	for _, item := range todo {
		if item.requeue {
			if _, err := tx.ExecContext(ctx, `UPDATE plans
				SET status = ?, phase = '', next_attempt_at = ?, updated_at = ?
				WHERE id = ? AND status IN (?, ?)`,
				PlanStatusQueued, now, now, item.id, PlanStatusRunning, PlanStatusQueued); err != nil {
				return report, fmt.Errorf("中断恢复失败: %w", err)
			}
			report.RequeuedIDs = append(report.RequeuedIDs, item.id)
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE plans
			SET status = ?, phase = '', error_code = ?, error_message = ?, updated_at = ?
			WHERE id = ? AND status IN (?, ?)`,
			PlanStatusFailed, CodeContextExpired, "任务上下文已失效，请重新创建任务",
			now, item.id, PlanStatusRunning, PlanStatusQueued); err != nil {
			return report, fmt.Errorf("中断恢复失败: %w", err)
		}
		report.FailedIDs = append(report.FailedIDs, item.id)
	}

	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("中断恢复失败: %w", err)
	}
	return report, nil
}

// contextRebuildable 判断重启后能否重建该计划的下载上下文（[05 §9]）。
//
// 判据来自 [03 §6] 的秘密边界：落盘允许的是"归一化后的页面地址"，媒体地址、Cookie、
// 视频号会话与解密键都只驻留内存。因此：
//   - wechat / browser：字节来源与解密上下文只在内存，重启即失去 → 不可恢复；
//   - direct / hls / dash / page：地址可从 source_url 重建 → 可恢复；
//   - source_url 为空（无来源可依）→ 保守判为不可恢复，置 failed 而不是空转重试。
func contextRebuildable(mediaKind, sourceURL string) bool {
	if strings.TrimSpace(sourceURL) == "" {
		return false
	}
	switch mediaKind {
	case PlanMediaDirect, PlanMediaHLS, PlanMediaDASH, PlanMediaPage:
		return true
	default:
		return false
	}
}

// execPlanTransition 执行带状态守卫的状态转换。
//
// 影响 0 行时区分两种原因：记录不存在（ErrPlanNotFound）与状态不满足守卫（ErrPlanStateChanged）。
// [12 §3] 要求两类错误给调用方不同的处置：前者是"用户删掉了记录"，后者是"状态已经变了，别再写"。
// 存在性检查放在更新之后，只用于**给错误分类**，不参与正确性判断。
func (db *DB) execPlanTransition(ctx context.Context, id, query string, args ...any) error {
	res, err := db.sql.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("更新计划状态失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("更新计划状态失败: %w", err)
	}
	if n > 0 {
		return nil
	}
	return db.classifyMissingPlan(ctx, id)
}

// classifyMissingPlan 在更新影响 0 行后判断是"不存在"还是"状态不满足"。
func (db *DB) classifyMissingPlan(ctx context.Context, id string) error {
	var one int
	err := db.sql.QueryRowContext(ctx, `SELECT 1 FROM plans WHERE id = ?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPlanNotFound
	}
	if err != nil {
		return fmt.Errorf("读取计划失败: %w", err)
	}
	return ErrPlanStateChanged
}

// scanPlan 把一行装进 Plan，处理可空列与布尔列。
func scanPlan(row rowScanner) (Plan, error) {
	var (
		p                                Plan
		importToEagle, deleteAfterImport int
		// total_bytes 是 INTEGER，用 NullInt64 而不是 NullFloat64——它承载字节数，不能有浮点误差。
		totalInt                   sql.NullInt64
		nextAttemptAt, completedAt sql.NullFloat64
		finalPath, previewPath     sql.NullString
		errorCode, errorMessage    sql.NullString
	)
	err := row.Scan(&p.ID, &p.SourceURL, &p.SourceTitle, &p.MediaKind, &p.OutputName, &p.OutputContainer,
		&p.MergeMode, &p.QualityLabel, &p.StreamPlan, &importToEagle, &deleteAfterImport,
		&p.Status, &p.Phase, &p.Progress, &p.DownloadedBytes, &totalInt, &p.PhaseDetail,
		&finalPath, &previewPath, &p.AttemptCount, &nextAttemptAt,
		&errorCode, &errorMessage, &p.CreatedAt, &p.UpdatedAt, &completedAt)
	if err != nil {
		return Plan{}, err
	}

	p.ImportToEagle = importToEagle != 0
	p.DeleteAfterImport = deleteAfterImport != 0
	if totalInt.Valid {
		v := totalInt.Int64
		p.TotalBytes = &v
	}
	if nextAttemptAt.Valid {
		v := nextAttemptAt.Float64
		p.NextAttemptAt = &v
	}
	if completedAt.Valid {
		v := completedAt.Float64
		p.CompletedAt = &v
	}
	p.FinalPath = finalPath.String
	p.PreviewPath = previewPath.String
	p.ErrorCode = errorCode.String
	p.ErrorMessage = errorMessage.String
	return p, nil
}

func collectPlans(rows *sql.Rows) ([]Plan, error) {
	var out []Plan
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// planStatusFilter 生成状态筛选片段并校验取值域（[03 §3.1]）。
// 空切片表示"不过滤"——那正是 [03 §5] 的 Q2 场景。
func planStatusFilter(statuses []string) (string, []any, error) {
	if len(statuses) == 0 {
		return "", nil, nil
	}
	holders := make([]string, 0, len(statuses))
	args := make([]any, 0, len(statuses))
	for _, status := range statuses {
		if !validPlanStatuses[status] {
			return "", nil, fmt.Errorf("非法的计划状态筛选值 %q", status)
		}
		holders = append(holders, "?")
		args = append(args, status)
	}
	// 单状态走等值，让 idx_plans_status 的 (status, updated_at DESC) 前缀直接服务排序。
	if len(statuses) == 1 {
		return " WHERE status = ?", args, nil
	}
	return " WHERE status IN (" + strings.Join(holders, ", ") + ")", args, nil
}

// ClampPlanPaging 把分页参数夹进合法范围（[03 §4.5]：单次返回上限 200）。
//
// 导出是因为 service 要把**实际生效**的 limit/offset 回显给前端（[04 §3.1] 的 Paged），
// 两边各写一份夹取逻辑必然漂移。
func ClampPlanPaging(offset, limit int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = DefaultPlanListLimit
	}
	if limit > MaxPlanListLimit {
		limit = MaxPlanListLimit
	}
	return limit, offset
}

// validatePlanForWrite 校验 [03 §2.1] 的数据不变量。只做输入合法性判断，不做业务决策。
func validatePlanForWrite(p Plan) error {
	if strings.TrimSpace(p.ID) == "" {
		return errors.New("计划 ID 不得为空")
	}
	if !validPlanMediaKinds[p.MediaKind] {
		return fmt.Errorf("非法的媒体类型 %q", p.MediaKind)
	}
	if strings.TrimSpace(p.OutputName) == "" {
		return errors.New("输出名不得为空")
	}
	if !validPlanContainers[p.OutputContainer] {
		return fmt.Errorf("非法的输出容器 %q", p.OutputContainer)
	}
	if !validPlanMergeModes[p.MergeMode] {
		return fmt.Errorf("非法的合并方式 %q", p.MergeMode)
	}
	if !validPlanStatuses[p.Status] {
		return fmt.Errorf("非法的计划状态 %q", p.Status)
	}
	if !validPlanPhases[p.Phase] {
		return fmt.Errorf("非法的阶段值 %q", p.Phase)
	}
	// [03 §2.1]：phase 只在 running 内有值。
	if p.Status != PlanStatusRunning && p.Phase != PlanPhaseNone {
		return errors.New("只有 running 计划可以有 phase")
	}
	// [05 §2.2]：status = running 时 phase 必须有值。
	if p.Status == PlanStatusRunning && p.Phase == PlanPhaseNone {
		return errors.New("running 计划必须有 phase")
	}
	// [03 §2.1]：status = completed 时 final_path 必须非空。
	if p.Status == PlanStatusCompleted && strings.TrimSpace(p.FinalPath) == "" {
		return errors.New("completed 计划必须有 final_path")
	}
	// [03 §2.1]：status = failed 时 error_code 必须非空。
	if p.Status == PlanStatusFailed && strings.TrimSpace(p.ErrorCode) == "" {
		return errors.New("failed 计划必须有 error_code")
	}
	if p.Progress < 0 || p.Progress > 100 {
		return errors.New("进度必须在 0–100 之间")
	}
	// [05 §2.2]：只有 completed 允许 progress = 100。
	if p.Status != PlanStatusCompleted && p.Progress >= 100 {
		return errors.New("只有 completed 计划允许 100% 进度")
	}
	if p.DownloadedBytes < 0 {
		return errors.New("已下载字节数不得为负")
	}
	if p.TotalBytes != nil && *p.TotalBytes < 0 {
		return errors.New("总字节数不得为负")
	}
	if p.AttemptCount < 0 {
		return errors.New("重试计数不得为负")
	}
	if err := validatePhaseDetail(p.PhaseDetail); err != nil {
		return err
	}
	return validateStreamPlan(p.StreamPlan)
}

// validatePhaseDetail 拒绝把 URL、路径或换行塞进 phase_detail。
// [03 §2.1]：该字段只允许存放**可直接展示的短文案**，不得包含路径、URL 或秘密。
func validatePhaseDetail(detail string) error {
	if detail == "" {
		return nil
	}
	if strings.ContainsAny(detail, "\r\n") {
		return errors.New("phase_detail 不得包含换行")
	}
	if strings.Contains(detail, "://") || strings.ContainsAny(detail, `\/`) {
		return errors.New("phase_detail 不得包含 URL 或路径（[03 §2.1]）")
	}
	if len([]rune(detail)) > 200 {
		return errors.New("phase_detail 过长")
	}
	return nil
}

// sensitiveStreamKeys 是禁止出现在 stream_plan 里的键名（[03 §2.1]、[03 §6]、B-722）。
var sensitiveStreamKeys = map[string]bool{
	"url": true, "uri": true, "href": true, "src": true,
	"decode_key": true, "decodekey": true, "key": true, "token": true,
	"sign": true, "signature": true, "encfilekey": true,
	"cookie": true, "authorization": true,
}

// validateStreamPlan 校验 stream_plan 是 JSON 数组，且**不含签名 URL 或解密键**。
//
// [03 §2.1] 写明："stream_plan 为 JSON 数组，描述所选轨道与质量档位，不得包含签名 URL 或解密键"。
// 这是一条秘密边界（B-722），所以做成写入前的机械拒绝，而不是靠调用方自觉。
func validateStreamPlan(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var decoded []any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return errors.New("stream_plan 必须是 JSON 数组")
	}
	return checkStreamPlanValue(decoded)
}

func checkStreamPlanValue(value any) error {
	switch typed := value.(type) {
	case []any:
		for _, item := range typed {
			if err := checkStreamPlanValue(item); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, item := range typed {
			if sensitiveStreamKeys[strings.ToLower(key)] {
				return fmt.Errorf("stream_plan 不得包含 %q 字段（[03 §2.1]）", key)
			}
			if err := checkStreamPlanValue(item); err != nil {
				return err
			}
		}
	case string:
		if strings.Contains(typed, "://") {
			return errors.New("stream_plan 不得包含 URL（[03 §2.1]）")
		}
	}
	return nil
}

// runningProgress 计算运行中的进度百分比。
//
// [05 §4.1]：无 Content-Length（total 为 nil）时用阶段语义，**不伪造百分比**——保持 0。
// [05 §2.2]：running 期间封顶 99.9，把 100 留给 completed（B-312）。
func runningProgress(downloaded int64, total *int64) float64 {
	if total == nil || *total <= 0 || downloaded <= 0 {
		return 0
	}
	percent := float64(downloaded) / float64(*total) * 100
	switch {
	case percent < 0:
		return 0
	case percent > 99.9:
		// 服务端声明的总长度可能偏小，此时 downloaded 会超过 total；
		// 既不能违反 CHECK(progress <= 100)，也不能在 running 期间显示 100%。
		return 99.9
	default:
		return percent
	}
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// nullableInt 把可空字节数转成驱动参数：nil → NULL，0 → 0。
// [03 §2.1] 的三态语义全靠这一行守住（NULL = 未知，0 = 长度为零）。
func nullableInt(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableFloat(v *float64) any {
	if v == nil {
		return nil
	}
	return *v
}

// nullableString 把空串写成 NULL：可空文本列里 "" 与 NULL 无业务差别，
// 统一成 NULL 才不会出现"看起来有值其实是空串"的记录。
func nullableString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

// truncateRunes 按字符（而不是字节）截断，避免把中文截成半个字（[03 §4.5]：错误消息 1000 字符）。
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}

package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

// 覆盖 [12 §5.2] 的 T6：数据库测试必须覆盖结构初始化与约束。
// 全程使用 t.TempDir()，不污染用户目录（[12 §5.2] 的 T2）。
func TestOpen_CreatesSchema(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	names, err := db.TableNames(ctx)
	if err != nil {
		t.Fatalf("TableNames 失败: %v", err)
	}
	want := []string{"fingerprints", "jobs", "plans", "settings", "site_rules"} // [03 §2]：共 5 张表
	if len(names) != len(want) {
		t.Fatalf("表数量 = %d (%v)，期望 %d", len(names), names, len(want))
	}
	for i := range want {
		if names[i] != want[i] {
			t.Errorf("表[%d] = %q，期望 %q", i, names[i], want[i])
		}
	}

	on, err := db.ForeignKeysEnabled(ctx)
	if err != nil {
		t.Fatalf("ForeignKeysEnabled 失败: %v", err)
	}
	if !on {
		t.Error("foreign_keys 未开启——外键约束不会生效（[03 §7]）")
	}

	var mode string
	if err := db.SQL().QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatalf("读取 journal_mode 失败: %v", err)
	}
	if mode != "wal" {
		t.Errorf("journal_mode = %q，期望 wal（[03 §7]）", mode)
	}

	var version int
	if err := db.SQL().QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("读取 user_version 失败: %v", err)
	}
	if version != SchemaVersion {
		t.Errorf("user_version = %d，期望 %d", version, SchemaVersion)
	}
}

// plans.status 只有 5 个合法取值（[03 §3.1]），非法值必须被 CHECK 拒绝。
func TestOpen_RejectsInvalidStatus(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.SQL().ExecContext(context.Background(),
		`INSERT INTO plans (id, media_kind, output_name, output_container, merge_mode, status, created_at, updated_at)
		 VALUES ('p1','direct','n','mp4','single','bogus',1,1)`)
	if err == nil {
		t.Fatal("非法 status 未被拒绝——CHECK 约束未生效")
	}
}

// jobs.plan_id 指向不存在的 plans.id 时必须被拒绝（[03 §2] 的表间关系 + 每连接外键）。
func TestOpen_EnforcesForeignKey(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer func() { _ = db.Close() }()

	_, err = db.SQL().ExecContext(context.Background(),
		`INSERT INTO jobs (id, plan_id, file_path, file_name, extension, status, created_at, updated_at)
		 VALUES ('j1','missing-plan','x.mp4','x.mp4','mp4','queued',1,1)`)
	if err == nil {
		t.Fatal("悬空外键未被拒绝——PRAGMA foreign_keys 未生效")
	}
}

// 库版本高于程序支持版本时必须拒绝打开（[01 §5.1] 启动第 3 步的版本校验）。
func TestOpen_RejectsNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	if _, err := db.SQL().ExecContext(context.Background(),
		fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion+1)); err != nil {
		t.Fatalf("提升 user_version 失败: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	if _, err := Open(path); err == nil {
		t.Fatal("版本过新的库被打开了，应当拒绝")
	}
}

// 重复打开同一路径必须幂等：建表语句是 IF NOT EXISTS，user_version 不变。
func TestOpen_IsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")

	for i := 1; i <= 2; i++ {
		db, err := Open(path)
		if err != nil {
			t.Fatalf("第 %d 次 Open 失败: %v", i, err)
		}
		names, err := db.TableNames(context.Background())
		if err != nil {
			t.Fatalf("第 %d 次 TableNames 失败: %v", i, err)
		}
		if len(names) != 5 {
			t.Fatalf("第 %d 次打开后表数量 = %d，期望 5", i, len(names))
		}
		if err := db.Close(); err != nil {
			t.Fatalf("第 %d 次 Close 失败: %v", i, err)
		}
	}
}

// createLegacyDB 造一个"已发布的旧库"：结构与当前 DDL 相同，但把 v2 起新增的列删掉，
// 并把 user_version 退回 from。这样测的是**真实的旧结构**，不是手抄的近似物。
func createLegacyDB(t *testing.T, path string, from int) {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path+pragmaQuery)
	if err != nil {
		t.Fatalf("打开旧库失败: %v", err)
	}
	defer func() { _ = raw.Close() }()

	for _, stmt := range schemaStatements {
		if _, err := raw.Exec(stmt); err != nil {
			t.Fatalf("建旧库失败: %v", err)
		}
	}
	// v2 新增的列（见 migrations[1]）。
	if _, err := raw.Exec(`ALTER TABLE plans DROP COLUMN resolved_kind`); err != nil {
		t.Fatalf("退回旧结构失败: %v", err)
	}
	// 一条存量记录：迁移**不得**动业务数据。
	if _, err := raw.Exec(`INSERT INTO plans
		(id, source_url, media_kind, output_name, output_container, merge_mode, status, created_at, updated_at)
		VALUES ('p-legacy', 'https://example.com/watch', 'direct', '老记录', 'mp4', 'single', 'queued', 1, 1)`); err != nil {
		t.Fatalf("写存量记录失败: %v", err)
	}
	if _, err := raw.Exec(fmt.Sprintf("PRAGMA user_version = %d", from)); err != nil {
		t.Fatalf("写旧版本号失败: %v", err)
	}
}

// [03 §1]「结构版本与迁移」：旧库必须**逐级迁移**上来，而不是被
// `CREATE TABLE IF NOT EXISTS` 悄悄放过——后者对已存在的表什么都不做，只升版本号
// 会留下一个缺列的库（查询新列直接报错）。
func TestOpen_MigratesLegacyV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	createLegacyDB(t, path, 1)

	db, err := Open(path)
	if err != nil {
		t.Fatalf("打开 v1 库失败: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()

	var version int
	if err := db.SQL().QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("读取 user_version 失败: %v", err)
	}
	if version != SchemaVersion {
		t.Errorf("user_version = %d，期望 %d", version, SchemaVersion)
	}

	// 新列必须真的存在：GetPlan 的列表含 resolved_kind，缺列时这里会拿到
	// "no such column" 而不是 ErrPlanNotFound。
	got, err := db.GetPlan(ctx, "p-legacy")
	if err != nil {
		t.Fatalf("迁移后读取存量记录失败: %v", err)
	}
	if got.OutputName != "老记录" {
		t.Errorf("存量记录的 output_name = %q，期望 %q（迁移不得丢数据）", got.OutputName, "老记录")
	}
	// 存量行一律留 NULL——**不能拿 media_kind 回填**，否则"提示"会被伪装成"事实"，
	// 界面上再也看不出哪条计划发生过降级（B-316、[03 §2.1]）。
	if got.ResolvedKind != nil {
		t.Errorf("存量记录的 resolved_kind = %q，期望 NULL", *got.ResolvedKind)
	}
}

// 缺少某一级迁移时必须**拒绝打开**，不能留下一个半旧的库（[03 §1]：没有"静默跳过"这一档）。
func TestOpen_LegacyWithoutMigrationStepFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.db")
	createLegacyDB(t, path, 1)

	saved, had := migrations[1]
	delete(migrations, 1)
	defer func() {
		if had {
			migrations[1] = saved
		}
	}()

	if _, err := Open(path); err == nil {
		t.Fatal("缺少迁移步骤时旧库被打开了，应当拒绝")
	}
}

// 每个已发布版本到当前版本之间都必须登记迁移步骤（[03 §1]）。
func TestMigrations_CoverPublishedVersions(t *testing.T) {
	if SchemaVersion < 2 {
		t.Fatalf("SchemaVersion = %d，v2 起才有 resolved_kind（[03 §2.1]）", SchemaVersion)
	}
	for v := 1; v < SchemaVersion; v++ {
		if _, ok := migrations[v]; !ok {
			t.Errorf("缺少结构版本 %d → %d 的迁移步骤", v, v+1)
		}
	}
}

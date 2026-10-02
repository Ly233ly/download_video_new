package store

import (
	"context"
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

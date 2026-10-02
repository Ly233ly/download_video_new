package store

import (
	"context"
	"path/filepath"
	"testing"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "data.db"))
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// 键不存在**不是错误**——调用方按 [03 §2.4] 回退默认值。
func TestSettings_MissingIsNotAnError(t *testing.T) {
	db := newTestDB(t)

	value, ok, err := db.GetSetting(context.Background(), "theme")
	if err != nil {
		t.Fatalf("读取不存在的键不应报错: %v", err)
	}
	if ok {
		t.Error("不存在的键不应报告存在")
	}
	if value != "" {
		t.Errorf("value = %q，期望空串", value)
	}
}

func TestSettings_RoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.SetSetting(ctx, "theme", `"dark"`); err != nil {
		t.Fatalf("SetSetting 失败: %v", err)
	}
	value, ok, err := db.GetSetting(ctx, "theme")
	if err != nil {
		t.Fatalf("GetSetting 失败: %v", err)
	}
	if !ok || value != `"dark"` {
		t.Errorf("GetSetting = (%q, %v)，期望 (\"dark\", true)", value, ok)
	}
}

// value 必须是合法 JSON（[03 §2.4]）——非法值直接拒绝，不写库。
func TestSettings_RejectsInvalidJSON(t *testing.T) {
	db := newTestDB(t)

	if err := db.SetSetting(context.Background(), "bad", `{not json`); err == nil {
		t.Fatal("非法 JSON 应当被拒绝")
	}
	if _, ok, _ := db.GetSetting(context.Background(), "bad"); ok {
		t.Error("被拒绝的值不应落库")
	}
}

func TestSettings_RejectsEmptyKey(t *testing.T) {
	db := newTestDB(t)

	if err := db.SetSetting(context.Background(), "", `1`); err == nil {
		t.Fatal("空键应当被拒绝")
	}
}

func TestSettings_AllSettings(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	for _, kv := range [][2]string{{"theme", `"light"`}, {"retry_max", `5`}} {
		if err := db.SetSetting(ctx, kv[0], kv[1]); err != nil {
			t.Fatalf("SetSetting(%s) 失败: %v", kv[0], err)
		}
	}

	all, err := db.AllSettings(ctx)
	if err != nil {
		t.Fatalf("AllSettings 失败: %v", err)
	}
	if len(all) != 2 || all["theme"] != `"light"` || all["retry_max"] != `5` {
		t.Errorf("AllSettings = %v", all)
	}
}

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// settings 表是配置的**唯一权威源**（[03 §2.4]）：`value` 一律 JSON 编码。
// 时间按 [03 §1] 的 P3 存 REAL 类型的 Unix 秒（含小数）。

// GetSetting 读取配置的**原始 JSON 字符串**。
//
// 第二个返回值表示键是否存在：**不存在不是错误**，调用方按 [03 §2.4] 回退默认值。
func (db *DB) GetSetting(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := db.sql.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("读取配置失败: %w", err)
	}
	return value, true, nil
}

// SetSetting 写入配置。value 必须是**合法 JSON**——这是输入合法性校验，
// 不是业务判断（[12 §3] 的分类：输入错误直接拒绝，不重试）。
func (db *DB) SetSetting(ctx context.Context, key, value string) error {
	if key == "" {
		return errors.New("配置键不得为空")
	}
	if !json.Valid([]byte(value)) {
		return errors.New("配置值必须是合法 JSON")
	}
	_, err := db.sql.ExecContext(ctx,
		`INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value, unixSeconds())
	if err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	return nil
}

// AllSettings 返回全部配置的原始 JSON 快照。
func (db *DB) AllSettings(ctx context.Context) (map[string]string, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]string)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("读取配置失败: %w", err)
		}
		out[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	return out, nil
}

// unixSeconds 返回 [03 §1] 规定的时间表示：REAL 类型的 Unix 秒（含小数）。
func unixSeconds() float64 {
	return float64(time.Now().UnixMilli()) / 1000
}

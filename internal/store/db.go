// Package store 是唯一持久化层：SQLite 打开、结构初始化与查询（[01 §3]）。
//
// 驱动为 modernc.org/sqlite（纯 Go、无 CGO，[03 §1] 的 P1）——因此本机不需要
// C 编译器（[CONTEXT §3.4]）。
package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

const (
	// SchemaVersion 是新库的结构版本。[03 §1]：全新库 v1，**不做历史迁移**。
	// 存放位置用 SQLite 内置的 `PRAGMA user_version`——不占 5 张业务表（[03 §2] 规定共 5 张）。
	SchemaVersion = 1

	openTimeout  = 10 * time.Second
	maxOpenConns = 4 // [03 §7]：最大读连接 4
)

// 连接级 PRAGMA（[03 §7]）：WAL、busy_timeout 5000 ms、**每连接**开启外键。
// 写在 DSN 里而不是打开后执行一次——外键是连接级开关，连接池里的每条连接都要有。
const pragmaQuery = "?_pragma=journal_mode(WAL)" +
	"&_pragma=busy_timeout(5000)" +
	"&_pragma=foreign_keys(ON)"

// DB 是主库句柄。
type DB struct {
	sql  *sql.DB
	path string
}

// Open 打开（必要时创建）主库，并保证结构就绪。
//
// 对应 [01 §5.1] 启动第 3 步：初始化或版本校验。**调用方必须保证它排在第 2 步
// （代理恢复）之后**——数据库坏掉时也不能让用户带着失效的代理断网。
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录失败: %w", err)
	}

	sqlDB, err := sql.Open("sqlite", "file:"+path+pragmaQuery)
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败: %w", err)
	}
	sqlDB.SetMaxOpenConns(maxOpenConns)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), openTimeout)
	defer cancel()

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}

	db := &DB{sql: sqlDB, path: path}
	if err := db.verifyVersion(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	if err := db.ensureSchema(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return db, nil
}

// verifyVersion 拒绝"库版本比程序新"的情况（[01 §5.1] 第 3 步的版本校验）。
// v1 是第一版，不存在更旧的版本需要迁移（[03 §1]）。
func (db *DB) verifyVersion(ctx context.Context) error {
	var v int
	if err := db.sql.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return fmt.Errorf("读取结构版本失败: %w", err)
	}
	if v > SchemaVersion {
		return fmt.Errorf("数据库结构版本 %d 高于本程序支持的 %d", v, SchemaVersion)
	}
	return nil
}

// ensureSchema 建表并写入结构版本，整体在一个事务里完成。
func (db *DB) ensureSchema(ctx context.Context) error {
	tx, err := db.sql.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("开始建表事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range schemaStatements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("初始化表结构失败: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion)); err != nil {
		return fmt.Errorf("写入结构版本失败: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交表结构失败: %w", err)
	}
	return nil
}

// SQL 暴露底层句柄给同模块的查询实现。
func (db *DB) SQL() *sql.DB { return db.sql }

// Path 返回主库路径（仅用于日志与诊断，不得进入用户可见错误消息，[12 §4.3]）。
func (db *DB) Path() string { return db.path }

// ForeignKeysEnabled 报告当前连接上的外键开关状态。
// 供结构测试断言"每连接都开启了外键"（[12 §5.2] 的 T6）。
func (db *DB) ForeignKeysEnabled(ctx context.Context) (bool, error) {
	var on int
	if err := db.sql.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&on); err != nil {
		return false, fmt.Errorf("读取外键开关失败: %w", err)
	}
	return on == 1, nil
}

// TableNames 返回库中的业务表名，供结构测试断言 5 张表齐备。
func (db *DB) TableNames(ctx context.Context) ([]string, error) {
	rows, err := db.sql.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("读取表清单失败: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("读取表清单失败: %w", err)
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// Close 关闭连接池（[01 §5.2] 关闭第 5 步）。
func (db *DB) Close() error {
	if db.sql == nil {
		return nil
	}
	return db.sql.Close()
}

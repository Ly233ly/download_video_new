package store

// schemaStatements 是新库结构 v1 的全部语句，**逐条对应 [03 §2]**。
// 改这里必须同时改 03 §2（[12 §7] 文档同步规则）——一处事实，一处定义。
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS plans (
    id                  TEXT PRIMARY KEY,
    source_url          TEXT NOT NULL DEFAULT '',
    source_title        TEXT NOT NULL DEFAULT '',
    media_kind          TEXT NOT NULL
                        CHECK (media_kind IN ('direct','hls','dash','page','wechat','browser')),
    output_name         TEXT NOT NULL,
    output_container    TEXT NOT NULL
                        CHECK (output_container IN ('mp4','mkv','webm','m4a','mp3','ts')),
    merge_mode          TEXT NOT NULL
                        CHECK (merge_mode IN ('single','av','subtitles')),
    quality_label       TEXT NOT NULL DEFAULT '',
    stream_plan         TEXT NOT NULL DEFAULT '[]',
    import_to_eagle     INTEGER NOT NULL DEFAULT 0 CHECK (import_to_eagle IN (0,1)),
    delete_after_import INTEGER NOT NULL DEFAULT 0 CHECK (delete_after_import IN (0,1)),
    status              TEXT NOT NULL CHECK (status IN (
                            'queued','running','completed','failed','canceled')),
    phase               TEXT NOT NULL DEFAULT ''
                        CHECK (phase IN ('','downloading','merging','validating')),
    progress            REAL NOT NULL DEFAULT 0 CHECK (progress >= 0 AND progress <= 100),
    downloaded_bytes    INTEGER NOT NULL DEFAULT 0,
    total_bytes         INTEGER,
    phase_detail        TEXT NOT NULL DEFAULT '',
    final_path          TEXT,
    preview_path        TEXT,
    attempt_count       INTEGER NOT NULL DEFAULT 0,
    next_attempt_at     REAL,
    error_code          TEXT,
    error_message       TEXT,
    created_at          REAL NOT NULL,
    updated_at          REAL NOT NULL,
    completed_at        REAL
)`,
	`CREATE INDEX IF NOT EXISTS idx_plans_status   ON plans(status, updated_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_plans_updated  ON plans(updated_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_plans_created  ON plans(created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_plans_due      ON plans(next_attempt_at)
    WHERE status = 'queued'`,

	`CREATE TABLE IF NOT EXISTS jobs (
    id              TEXT PRIMARY KEY,
    plan_id         TEXT UNIQUE,
    file_path       TEXT NOT NULL,
    file_name       TEXT NOT NULL,
    extension       TEXT NOT NULL,
    status          TEXT NOT NULL CHECK (status IN (
                        'queued','waiting','running','imported','failed','skipped')),
    source_url      TEXT,
    source_title    TEXT,
    fingerprint     TEXT,
    eagle_item_id   TEXT,
    attempt_count   INTEGER NOT NULL DEFAULT 0,
    next_attempt_at REAL,
    error_code      TEXT,
    error_message   TEXT,
    created_at      REAL NOT NULL,
    updated_at      REAL NOT NULL,
    completed_at    REAL,
    FOREIGN KEY(plan_id) REFERENCES plans(id)
)`,
	`CREATE INDEX IF NOT EXISTS idx_jobs_due     ON jobs(status, next_attempt_at, created_at)`,
	`CREATE INDEX IF NOT EXISTS idx_jobs_created ON jobs(created_at DESC)`,
	`CREATE INDEX IF NOT EXISTS idx_jobs_file    ON jobs(file_path, status)`,

	`CREATE TABLE IF NOT EXISTS fingerprints (
    fingerprint TEXT PRIMARY KEY,
    job_id      TEXT NOT NULL,
    file_size   INTEGER NOT NULL,
    created_at  REAL NOT NULL
)`,

	`CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at REAL NOT NULL
)`,

	`CREATE TABLE IF NOT EXISTS site_rules (
    domain             TEXT PRIMARY KEY,
    enabled            INTEGER NOT NULL CHECK (enabled IN (0,1)),
    include_subdomains INTEGER NOT NULL DEFAULT 1 CHECK (include_subdomains IN (0,1)),
    updated_at         REAL NOT NULL
)`,
}

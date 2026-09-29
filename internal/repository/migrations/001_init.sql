CREATE TABLE IF NOT EXISTS resources (
  id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  name VARCHAR(512) NOT NULL,
  original_name VARCHAR(512) NOT NULL,
  kind VARCHAR(16) NOT NULL,
  mime VARCHAR(96) NOT NULL,
  size_bytes BIGINT UNSIGNED NOT NULL,
  sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  storage_key CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  favorite BOOLEAN NOT NULL DEFAULT FALSE,
  created_at DATETIME(6) NOT NULL,
  INDEX idx_resources_created (created_at, id),
  INDEX idx_resources_kind_created (kind, created_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS sessions (
  token_hash CHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  expires_at DATETIME(6) NOT NULL,
  INDEX idx_sessions_expires (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

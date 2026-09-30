CREATE TABLE IF NOT EXISTS pending_file_cleanup (
  kind VARCHAR(8) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  storage_key CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_at DATETIME(6) NOT NULL,
  retry_after DATETIME(6) NOT NULL,
  PRIMARY KEY (kind, storage_key),
  INDEX idx_pending_file_cleanup_retry (retry_after, kind, storage_key),
  CONSTRAINT chk_pending_file_cleanup_kind CHECK (kind IN ('original', 'derived'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

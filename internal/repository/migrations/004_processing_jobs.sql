CREATE TABLE IF NOT EXISTS processing_jobs (
  id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  resource_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  type VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  source_sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  attempts TINYINT UNSIGNED NOT NULL DEFAULT 0,
  max_attempts TINYINT UNSIGNED NOT NULL DEFAULT 3,
  available_at DATETIME(6) NOT NULL,
  lease_until DATETIME(6) NULL,
  lease_token CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
  last_error VARCHAR(512) NULL,
  started_at DATETIME(6) NULL,
  finished_at DATETIME(6) NULL,
  created_at DATETIME(6) NOT NULL,
  CONSTRAINT fk_processing_jobs_resource FOREIGN KEY (resource_id) REFERENCES resources(id) ON DELETE CASCADE,
  INDEX idx_processing_jobs_claim (status, available_at, lease_until, created_at),
  INDEX idx_processing_jobs_resource (resource_id, type, created_at),
  INDEX idx_processing_jobs_source (resource_id, type, source_sha256, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS derived_assets (
  id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  job_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  resource_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  kind VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  name VARCHAR(512) NOT NULL,
  storage_key CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  mime VARCHAR(96) NOT NULL,
  size_bytes BIGINT UNSIGNED NOT NULL,
  sha256 CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  content_text LONGTEXT NULL,
  created_at DATETIME(6) NOT NULL,
  CONSTRAINT fk_derived_assets_job FOREIGN KEY (job_id) REFERENCES processing_jobs(id) ON DELETE CASCADE,
  CONSTRAINT fk_derived_assets_resource FOREIGN KEY (resource_id) REFERENCES resources(id) ON DELETE CASCADE,
  UNIQUE KEY uq_derived_assets_job_kind (job_id, kind),
  INDEX idx_derived_assets_resource (resource_id, kind, created_at),
  FULLTEXT KEY ft_derived_assets_content (content_text)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
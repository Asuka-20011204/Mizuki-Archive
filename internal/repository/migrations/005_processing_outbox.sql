CREATE TABLE IF NOT EXISTS processing_outbox (
  id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  job_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  status VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  attempts INT UNSIGNED NOT NULL DEFAULT 0,
  available_at DATETIME(6) NOT NULL,
  lease_until DATETIME(6) NULL,
  lease_token CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
  last_error VARCHAR(512) NULL,
  created_at DATETIME(6) NOT NULL,
  published_at DATETIME(6) NULL,
  CONSTRAINT fk_processing_outbox_job FOREIGN KEY (job_id) REFERENCES processing_jobs(id) ON DELETE CASCADE,
  INDEX idx_processing_outbox_claim (status, available_at, lease_until, created_at),
  INDEX idx_processing_outbox_job (job_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO processing_outbox (id, job_id, status, attempts, available_at, created_at)
SELECT REPLACE(UUID(), '-', ''), job.id, 'pending', 0, UTC_TIMESTAMP(6), UTC_TIMESTAMP(6)
FROM processing_jobs job
WHERE (job.status = 'pending' OR (job.status = 'processing' AND job.lease_until <= UTC_TIMESTAMP(6)))
  AND NOT EXISTS (SELECT 1 FROM processing_outbox existing WHERE existing.job_id = job.id);

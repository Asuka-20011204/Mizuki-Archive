CREATE TABLE IF NOT EXISTS phone_challenges (
  phone VARCHAR(14) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  purpose VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  attempts TINYINT UNSIGNED NOT NULL DEFAULT 0,
  expires_at DATETIME(6) NOT NULL,
  next_request_at DATETIME(6) NOT NULL,
  PRIMARY KEY (phone, purpose),
  INDEX idx_phone_challenges_expiry (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

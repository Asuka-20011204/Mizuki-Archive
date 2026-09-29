CREATE TABLE IF NOT EXISTS users (
  id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  username VARCHAR(128) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  email VARCHAR(254) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL,
  password_hash VARBINARY(255) NULL,
  created_at DATETIME(6) NOT NULL,
  UNIQUE KEY uq_users_email (email),
  UNIQUE KEY uq_users_username (username)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS email_challenges (
  email VARCHAR(254) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  purpose VARCHAR(16) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  digest CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  attempts TINYINT UNSIGNED NOT NULL DEFAULT 0,
  expires_at DATETIME(6) NOT NULL,
  next_request_at DATETIME(6) NOT NULL,
  PRIMARY KEY (email, purpose),
  INDEX idx_email_challenges_expiry (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

ALTER TABLE sessions ADD COLUMN IF NOT EXISTS user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER token_hash;
ALTER TABLE resources ADD COLUMN IF NOT EXISTS user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL AFTER id;

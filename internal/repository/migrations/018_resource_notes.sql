CREATE TABLE IF NOT EXISTS resource_notes (
  id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  resource_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  page_number INT UNSIGNED NULL,
  excerpt TEXT NOT NULL,
  content TEXT NOT NULL,
  source VARCHAR(500) NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  INDEX idx_resource_notes_owner_file (user_id, resource_id, created_at, id),
  CONSTRAINT fk_resource_notes_user FOREIGN KEY (user_id) REFERENCES users(id),
  CONSTRAINT fk_resource_notes_file FOREIGN KEY (resource_id) REFERENCES resources(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

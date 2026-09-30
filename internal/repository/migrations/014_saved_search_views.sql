CREATE TABLE IF NOT EXISTS saved_search_views (
  id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  name VARCHAR(60) NOT NULL,
  query_text VARCHAR(100) NOT NULL DEFAULT '',
  source VARCHAR(8) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  kind VARCHAR(40) NOT NULL DEFAULT '',
  tag VARCHAR(40) NOT NULL DEFAULT '',
  organization_status VARCHAR(12) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
  created_at DATETIME(6) NOT NULL,
  CONSTRAINT fk_saved_search_views_user FOREIGN KEY (user_id) REFERENCES users(id),
  UNIQUE KEY uq_saved_search_views_owner_name (user_id, name),
  INDEX idx_saved_search_views_owner_created (user_id, created_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

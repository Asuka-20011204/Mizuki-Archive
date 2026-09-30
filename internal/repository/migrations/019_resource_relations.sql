CREATE TABLE IF NOT EXISTS resource_relations (
  id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  left_source VARCHAR(8) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  left_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  right_source VARCHAR(8) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  right_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_at DATETIME(6) NOT NULL,
  CONSTRAINT fk_resource_relations_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  CONSTRAINT chk_resource_relations_left CHECK (left_source IN ('file', 'external')),
  CONSTRAINT chk_resource_relations_right CHECK (right_source IN ('file', 'external')),
  UNIQUE KEY uq_resource_relations_pair (user_id, left_source, left_id, right_source, right_id),
  INDEX idx_resource_relations_left (user_id, left_source, left_id),
  INDEX idx_resource_relations_right (user_id, right_source, right_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

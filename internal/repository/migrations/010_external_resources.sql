CREATE TABLE IF NOT EXISTS external_resources (
  id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  title VARCHAR(200) NOT NULL,
  location VARCHAR(2048) NOT NULL,
  resource_type VARCHAR(40) NOT NULL,
  version VARCHAR(80) NOT NULL DEFAULT '',
  note VARCHAR(2000) NOT NULL DEFAULT '',
  status VARCHAR(20) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  CONSTRAINT fk_external_resources_user FOREIGN KEY (user_id) REFERENCES users(id),
  INDEX idx_external_resources_owner (user_id, created_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS external_resource_tags (
  resource_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  name VARCHAR(40) NOT NULL,
  PRIMARY KEY (resource_id, name),
  CONSTRAINT fk_external_resource_tags_resource FOREIGN KEY (resource_id) REFERENCES external_resources(id) ON DELETE CASCADE
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

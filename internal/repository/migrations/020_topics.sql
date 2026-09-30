CREATE TABLE IF NOT EXISTS topics (
  id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  user_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  title VARCHAR(200) NOT NULL,
  intro VARCHAR(2000) NOT NULL DEFAULT '',
  cover_file_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
  created_at DATETIME(6) NOT NULL,
  updated_at DATETIME(6) NOT NULL,
  CONSTRAINT fk_topics_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE CASCADE,
  INDEX idx_topics_owner (user_id, created_at, id),
  INDEX idx_topics_cover (user_id, cover_file_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS topic_sections (
  id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
  topic_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  position SMALLINT UNSIGNED NOT NULL,
  title VARCHAR(200) NOT NULL,
  CONSTRAINT fk_topic_sections_topic FOREIGN KEY (topic_id) REFERENCES topics(id) ON DELETE CASCADE,
  UNIQUE KEY uq_topic_sections_order (topic_id, position)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE IF NOT EXISTS topic_items (
  section_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  position SMALLINT UNSIGNED NOT NULL,
  source VARCHAR(8) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  resource_id CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  PRIMARY KEY (section_id, position),
  CONSTRAINT fk_topic_items_section FOREIGN KEY (section_id) REFERENCES topic_sections(id) ON DELETE CASCADE,
  CONSTRAINT chk_topic_items_source CHECK (source IN ('file', 'external')),
  INDEX idx_topic_items_reference (source, resource_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

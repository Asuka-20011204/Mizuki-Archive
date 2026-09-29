SET @add_deleted_at = (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE resources ADD COLUMN deleted_at DATETIME(6) NULL',
    'SELECT 1'
  )
  FROM information_schema.columns
  WHERE table_schema = DATABASE()
    AND table_name = 'resources'
    AND column_name = 'deleted_at'
);
PREPARE add_deleted_at_statement FROM @add_deleted_at;
EXECUTE add_deleted_at_statement;
DEALLOCATE PREPARE add_deleted_at_statement;

SET @add_deleted_index = (
  SELECT IF(
    COUNT(*) = 0,
    'ALTER TABLE resources ADD INDEX idx_resources_deleted (deleted_at, created_at, id)',
    'SELECT 1'
  )
  FROM information_schema.statistics
  WHERE table_schema = DATABASE()
    AND table_name = 'resources'
    AND index_name = 'idx_resources_deleted'
);
PREPARE add_deleted_index_statement FROM @add_deleted_index;
EXECUTE add_deleted_index_statement;
DEALLOCATE PREPARE add_deleted_index_statement;
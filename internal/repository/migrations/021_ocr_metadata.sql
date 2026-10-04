ALTER TABLE `derived_assets` ADD COLUMN `ocr_pages` INT UNSIGNED NOT NULL DEFAULT 0 AFTER `content_text`;
ALTER TABLE `derived_assets` ADD COLUMN `ocr_confidence` DECIMAL(5,2) NOT NULL DEFAULT 0 AFTER `ocr_pages`;

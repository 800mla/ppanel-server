-- Purpose: Add user promo grants for newcomer first-order promotions
-- Author: Codex
-- Date: 2026-04-02

CREATE TABLE IF NOT EXISTS `user_promo_grants` (
  `id` BIGINT NOT NULL AUTO_INCREMENT,
  `user_id` BIGINT NOT NULL COMMENT 'User ID',
  `campaign_key` VARCHAR(100) NOT NULL DEFAULT '' COMMENT 'Promo campaign key',
  `status` VARCHAR(20) NOT NULL DEFAULT 'pending' COMMENT 'pending, active, dismissed, used, expired',
  `discount_type` VARCHAR(20) NOT NULL DEFAULT 'fixed_amount' COMMENT 'fixed_amount',
  `discount_value` INT NOT NULL DEFAULT 0 COMMENT 'Discount value in cents',
  `expires_at` DATETIME(3) NOT NULL COMMENT 'Promo expiration time',
  `dismissed_at` DATETIME(3) NULL DEFAULT NULL COMMENT 'Popup dismissed time',
  `used_at` DATETIME(3) NULL DEFAULT NULL COMMENT 'Promo used time',
  `order_id` BIGINT NOT NULL DEFAULT 0 COMMENT 'Reserved or used order ID',
  `created_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) COMMENT 'Created at',
  `updated_at` DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3) COMMENT 'Updated at',
  PRIMARY KEY (`id`),
  UNIQUE KEY `uk_user_promo_campaign` (`user_id`, `campaign_key`),
  KEY `idx_user_promo_status` (`user_id`, `status`),
  KEY `idx_user_promo_expires_at` (`expires_at`),
  KEY `idx_user_promo_order_id` (`order_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci COMMENT='User-bound promo grants';

SET @order_promo_campaign_exists = (
  SELECT COUNT(*)
  FROM INFORMATION_SCHEMA.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'order'
    AND COLUMN_NAME = 'promo_campaign_key'
);

SET @order_promo_campaign_sql = IF(
  @order_promo_campaign_exists = 0,
  'ALTER TABLE `order` ADD COLUMN `promo_campaign_key` VARCHAR(100) NOT NULL DEFAULT '''' COMMENT ''Promo campaign key'' AFTER `coupon_discount`',
  'SELECT ''Column promo_campaign_key already exists in order table'''
);

PREPARE stmt FROM @order_promo_campaign_sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

SET @order_promo_discount_exists = (
  SELECT COUNT(*)
  FROM INFORMATION_SCHEMA.COLUMNS
  WHERE TABLE_SCHEMA = DATABASE()
    AND TABLE_NAME = 'order'
    AND COLUMN_NAME = 'promo_discount'
);

SET @order_promo_discount_sql = IF(
  @order_promo_discount_exists = 0,
  'ALTER TABLE `order` ADD COLUMN `promo_discount` INT NOT NULL DEFAULT 0 COMMENT ''Promo discount amount in cents'' AFTER `promo_campaign_key`',
  'SELECT ''Column promo_discount already exists in order table'''
);

PREPARE stmt FROM @order_promo_discount_sql;
EXECUTE stmt;
DEALLOCATE PREPARE stmt;

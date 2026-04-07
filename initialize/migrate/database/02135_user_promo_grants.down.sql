-- Purpose: Rollback user promo grants for newcomer first-order promotions
-- Author: Codex
-- Date: 2026-04-02

DROP TABLE IF EXISTS `user_promo_grants`;

ALTER TABLE `order` DROP COLUMN IF EXISTS `promo_discount`;
ALTER TABLE `order` DROP COLUMN IF EXISTS `promo_campaign_key`;

-- 003_add_snapshot_depth.sql
-- Enrich market_snapshots with per-period volume (delta from cumulative),
-- order book depth (bid/ask), and spread for ML feature extraction.

ALTER TABLE market_snapshots
    ADD COLUMN IF NOT EXISTS volume_per_period DECIMAL(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS bid_depth         DECIMAL(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS ask_depth         DECIMAL(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS spread            DECIMAL(20,8) NOT NULL DEFAULT 0;

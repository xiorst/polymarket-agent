-- 002_add_fill_fields.sql
-- Add realistic fill tracking: actual fill price, filled quantity, and fee cost.
-- These support paper mode simulation (slippage, partial fill, gas cost) and live reconciliation.

ALTER TABLE orders
    ADD COLUMN IF NOT EXISTS fill_price      DECIMAL(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS filled_quantity DECIMAL(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS fee_cost        DECIMAL(20,8) NOT NULL DEFAULT 0;

-- Back-fill existing rows: assume full fill at requested price, zero fee.
UPDATE orders
SET fill_price      = price,
    filled_quantity = quantity
WHERE fill_price = 0;

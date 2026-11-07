-- ETL golden path PostgreSQL source fixture.
--
-- Apply this file to the isolated golden-path PostgreSQL database after
-- `task golden-path-up`. It is idempotent and uses only synthetic demo data.

BEGIN;

DROP TABLE IF EXISTS etl_golden_orders;
DROP TABLE IF EXISTS etl_golden_customers;

CREATE TABLE etl_golden_customers (
    customer_slug TEXT PRIMARY KEY,
    customer_name TEXT NOT NULL,
    customer_segment TEXT NOT NULL,
    region TEXT NOT NULL,
    lifecycle_stage TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE etl_golden_orders (
    order_id TEXT PRIMARY KEY,
    customer_slug TEXT NOT NULL,
    order_status TEXT NOT NULL,
    order_total_cents INTEGER NOT NULL,
    discount_cents INTEGER NOT NULL DEFAULT 0,
    placed_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

INSERT INTO etl_golden_customers (
    customer_slug,
    customer_name,
    customer_segment,
    region,
    lifecycle_stage,
    updated_at
) VALUES
    (
        'atlas-retail',
        'Atlas Retail',
        'retail',
        'emea',
        'active',
        '2026-06-01T08:00:00Z'
    ),
    (
        'harbor-health',
        'Harbor Health',
        'healthcare',
        'na',
        'active',
        '2026-06-01T08:05:00Z'
    ),
    (
        'northstar-logistics',
        'Northstar Logistics',
        'logistics',
        'apac',
        'active',
        '2026-06-01T08:10:00Z'
    ),
    (
        'evergreen-ops',
        'Evergreen Ops',
        'operations',
        'na',
        'active',
        '2026-06-01T08:15:00Z'
    ),
    (
        'summit-labs',
        'Summit Labs',
        'research',
        'emea',
        'paused',
        '2026-06-01T08:20:00Z'
    );

INSERT INTO etl_golden_orders (
    order_id,
    customer_slug,
    order_status,
    order_total_cents,
    discount_cents,
    placed_at,
    updated_at
) VALUES
    (
        'ord-1001',
        'atlas-retail',
        'paid',
        129900,
        10000,
        '2026-06-01T09:15:00Z',
        '2026-06-01T09:20:00Z'
    ),
    (
        'ord-1002',
        'atlas-retail',
        'paid',
        34999,
        0,
        '2026-06-02T11:30:00Z',
        '2026-06-02T11:31:00Z'
    ),
    (
        'ord-1003',
        'harbor-health',
        'refunded',
        78900,
        78900,
        '2026-06-03T15:45:00Z',
        '2026-06-04T08:10:00Z'
    ),
    (
        'ord-1004',
        'northstar-logistics',
        'paid',
        87500,
        7500,
        '2026-06-04T07:05:00Z',
        '2026-06-04T07:12:00Z'
    ),
    (
        'ord-1005',
        'evergreen-ops',
        'pending',
        42500,
        0,
        '2026-06-05T13:40:00Z',
        '2026-06-05T13:40:00Z'
    );

COMMIT;

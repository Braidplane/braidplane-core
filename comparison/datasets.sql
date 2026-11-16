-- Synthetic comparison definition only; loading requires separate authorization.
BEGIN;
CREATE SCHEMA comparison_load;
CREATE TABLE comparison_load.moderate (
    order_id integer PRIMARY KEY,
    amount_cents integer NOT NULL,
    payload text NOT NULL
);
CREATE TABLE comparison_load.near_limit (LIKE comparison_load.moderate INCLUDING ALL);
CREATE TABLE comparison_load.heavy (LIKE comparison_load.moderate INCLUDING ALL);

-- MD5 defines fixture bytes, not an integrity hash. Repetition compresses on disk.
INSERT INTO comparison_load.moderate
SELECT i, i % 100000, repeat(md5(i::text), 32)
FROM generate_series(1, 500) AS i;
INSERT INTO comparison_load.near_limit
SELECT i, i % 100000, repeat(md5(i::text), 256)
FROM generate_series(1, 1000) AS i;
INSERT INTO comparison_load.heavy
SELECT i, i % 100000, repeat(md5(i::text), 32)
FROM generate_series(1, 1000000) AS i;
COMMIT;

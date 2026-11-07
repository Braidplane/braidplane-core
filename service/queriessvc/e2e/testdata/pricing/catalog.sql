CREATE TABLE IF NOT EXISTS qualification_pricing_catalog (
    org_slug text NOT NULL,
    sku text NOT NULL CHECK (sku <> ''),
    title text NOT NULL,
    product_url text NOT NULL,
    price numeric(18,2) NOT NULL,
    effective_from date NOT NULL,
    effective_to date NOT NULL,
    snapshot_date date NOT NULL,
    category text NOT NULL,
    PRIMARY KEY (org_slug, sku, effective_from),
    CHECK (effective_from < effective_to)
);

CREATE OR REPLACE FUNCTION qualification_pricing_guard() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    -- Serialize patches for one tenant so freshness and overlap checks cannot race.
    PERFORM pg_advisory_xact_lock(hashtextextended(NEW.org_slug, 0));
    IF EXISTS (
        SELECT 1 FROM qualification_pricing_catalog
        WHERE org_slug = NEW.org_slug AND snapshot_date > NEW.snapshot_date
    ) THEN
        RAISE EXCEPTION 'pricing patch snapshot is older than the catalog' USING ERRCODE = '23514';
    END IF;
    IF EXISTS (
        SELECT 1 FROM qualification_pricing_catalog
        WHERE org_slug = NEW.org_slug AND sku = NEW.sku
          AND effective_from <> NEW.effective_from
          AND effective_from < NEW.effective_to AND NEW.effective_from < effective_to
    ) THEN
        RAISE EXCEPTION 'pricing effective intervals overlap' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS qualification_pricing_guard ON qualification_pricing_catalog;
CREATE TRIGGER qualification_pricing_guard BEFORE INSERT OR UPDATE ON qualification_pricing_catalog
FOR EACH ROW EXECUTE FUNCTION qualification_pricing_guard();

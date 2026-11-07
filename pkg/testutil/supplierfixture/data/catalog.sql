-- Fixture only: provision in a dedicated synthetic demo database.
CREATE TABLE supplier_price_offers (
    org_slug text NOT NULL,
    provider text NOT NULL,
    product_code text NOT NULL CHECK (length(product_code) > 0),
    title text NOT NULL,
    currency text NOT NULL CHECK (currency = 'EUR'),
    pack_units integer NOT NULL CHECK (pack_units > 0),
    quoted_price numeric(18,2) NOT NULL CHECK (quoted_price >= 0),
    unit_price numeric(18,4) NOT NULL CHECK (unit_price >= 0),
    source_date date NOT NULL,
    PRIMARY KEY (org_slug, provider, product_code)
);

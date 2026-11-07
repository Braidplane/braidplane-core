INSERT INTO supplier_price_offers
    (org_slug, provider, product_code, title, currency, pack_units, quoted_price, unit_price, source_date)
SELECT {{org_slug}}, provider, product_code, title, currency, pack_units,
       quoted_price, unit_price, source_date::date
FROM input
ON CONFLICT (org_slug, provider, product_code) DO UPDATE SET
    title = EXCLUDED.title,
    currency = EXCLUDED.currency,
    pack_units = EXCLUDED.pack_units,
    quoted_price = EXCLUDED.quoted_price,
    unit_price = EXCLUDED.unit_price,
    source_date = EXCLUDED.source_date
WHERE supplier_price_offers.source_date <= EXCLUDED.source_date
  AND (supplier_price_offers.title, supplier_price_offers.currency,
       supplier_price_offers.pack_units, supplier_price_offers.quoted_price,
       supplier_price_offers.unit_price, supplier_price_offers.source_date)
      IS DISTINCT FROM
      (EXCLUDED.title, EXCLUDED.currency, EXCLUDED.pack_units,
       EXCLUDED.quoted_price, EXCLUDED.unit_price, EXCLUDED.source_date);

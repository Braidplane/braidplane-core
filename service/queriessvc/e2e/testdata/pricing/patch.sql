INSERT INTO qualification_pricing_catalog
    (org_slug, sku, title, product_url, price, effective_from, effective_to, snapshot_date, category)
-- A scalar snapshot rejects mixed file dates before any catalog row is written.
SELECT {{org_slug}}, sku, title, product_url, price, effective_from::date, effective_to::date,
       (SELECT DISTINCT snapshot_date::date FROM input), category
FROM input
ORDER BY sku
ON CONFLICT (org_slug, sku, effective_from) DO UPDATE SET
    title = EXCLUDED.title,
    product_url = EXCLUDED.product_url,
    price = EXCLUDED.price,
    effective_to = EXCLUDED.effective_to,
    snapshot_date = EXCLUDED.snapshot_date,
    category = EXCLUDED.category
WHERE (qualification_pricing_catalog.title, qualification_pricing_catalog.product_url,
       qualification_pricing_catalog.price, qualification_pricing_catalog.effective_to,
       qualification_pricing_catalog.snapshot_date, qualification_pricing_catalog.category)
  IS DISTINCT FROM
      (EXCLUDED.title, EXCLUDED.product_url, EXCLUDED.price, EXCLUDED.effective_to,
       EXCLUDED.snapshot_date, EXCLUDED.category)

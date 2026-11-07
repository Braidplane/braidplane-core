SELECT sku, title, product_url, price, effective_from::text, effective_to::text, category
FROM qualification_pricing_catalog
WHERE org_slug = {{org_slug}}
  AND effective_from <= {{as_of}}::date AND {{as_of}}::date < effective_to
ORDER BY sku, effective_from

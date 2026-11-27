SELECT item_code AS sku,
       CAST(price AS DECIMAL(18,2)) AS unit_price
FROM input

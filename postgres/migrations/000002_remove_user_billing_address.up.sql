-- Preserve populated addresses outside Community before removing these fields.
BEGIN;
LOCK TABLE public.users IN ACCESS EXCLUSIVE MODE;
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM public.users
        WHERE billing_line1 IS NOT NULL OR billing_line2 IS NOT NULL
           OR billing_city IS NOT NULL OR billing_region IS NOT NULL
           OR billing_postal_code IS NOT NULL OR billing_country IS NOT NULL
    ) THEN
        RAISE EXCEPTION 'Export and clear existing user billing addresses before applying this migration';
    END IF;
END
$$;
ALTER TABLE public.users
    DROP COLUMN billing_line1,
    DROP COLUMN billing_line2,
    DROP COLUMN billing_city,
    DROP COLUMN billing_region,
    DROP COLUMN billing_postal_code,
    DROP COLUMN billing_country;
COMMIT;

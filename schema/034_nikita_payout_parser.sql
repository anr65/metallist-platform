-- Apply in one transaction after schema 033.
DO $$
DECLARE
    nikita_id uuid;
BEGIN
    IF (SELECT max(version) FROM schema_migrations) IS DISTINCT FROM 33 THEN
        RAISE EXCEPTION 'schema 034 requires version 33';
    END IF;

    BEGIN
        -- Match Cyrillic case variants even under the C database locale.
        SELECT id INTO STRICT nikita_id FROM merchants
        WHERE btrim(name) ~ '^[Нн][Ии][Кк][Ии][Тт][Аа]$' FOR UPDATE;
    EXCEPTION
        WHEN no_data_found THEN
            IF EXISTS (SELECT 1 FROM merchants WHERE upper(btrim(code)) = 'NIKITA') THEN
                RAISE EXCEPTION 'Код NIKITA уже занят мерчантом с другим именем';
            END IF;
            INSERT INTO merchants(id, code, name)
            VALUES ('5c24ced6-f9ac-490c-b44d-04fa77935be9', 'NIKITA', 'Никита')
            RETURNING id INTO nikita_id;
        WHEN too_many_rows THEN
            RAISE EXCEPTION 'Найдено несколько мерчантов с именем Никита';
    END;

    INSERT INTO registry_parser_types(code, name, version, accepted_extensions)
    VALUES ('nikita_payouts_xlsx_v1', 'Никита: выгрузка выплат', 1, ARRAY['.xlsx']);

    INSERT INTO merchant_import_profiles(merchant_id, parser_code, status)
    VALUES (nikita_id, 'nikita_payouts_xlsx_v1', 'configured')
    ON CONFLICT (merchant_id) DO UPDATE
    SET parser_code = EXCLUDED.parser_code, status = EXCLUDED.status, updated_at = now();

    INSERT INTO schema_migrations(version) VALUES (34);
END $$;

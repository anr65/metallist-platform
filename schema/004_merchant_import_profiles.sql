-- Apply once after schema 003, in one transaction and only after the environment gate.
DO $$
BEGIN
    IF (SELECT MAX(version) FROM schema_migrations) <> 3 THEN
        RAISE EXCEPTION 'schema 004 requires version 3';
    END IF;
END $$;

CREATE TABLE registry_parser_types (
    code text PRIMARY KEY,
    name text NOT NULL,
    version integer NOT NULL CHECK (version > 0),
    accepted_extensions text[] NOT NULL,
    active boolean NOT NULL DEFAULT true
);

CREATE TABLE merchant_import_profiles (
    merchant_id uuid PRIMARY KEY REFERENCES merchants(id),
    parser_code text REFERENCES registry_parser_types(code),
    status text NOT NULL CHECK (status IN ('configured','awaiting_sample')),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((status = 'configured' AND parser_code IS NOT NULL) OR
           (status = 'awaiting_sample' AND parser_code IS NULL))
);

ALTER TABLE source_documents
    ADD COLUMN parser_code text REFERENCES registry_parser_types(code),
    ADD COLUMN parser_version integer CHECK (parser_version > 0);

INSERT INTO registry_parser_types(code,name,version,accepted_extensions) VALUES
    ('generic_xlsx_v1','Стандартный XLSX: карта и сумма',1,ARRAY['.xlsx']),
    ('aliten_bank_csv_v1','Алитен: банковский CSV',1,ARRAY['.csv']),
    ('narkoman_avangard_xls_v1','Наркоман: выписка Авангарда',1,ARRAY['.xls']),
    ('sveta_cards_xls_v1','Света: выгрузка оплат по картам',1,ARRAY['.xls']),
    ('tolya_operations_xlsx_v1','Толя: выгрузка операций',1,ARRAY['.xlsx']),
    ('katya_payouts_xlsx_v1','Катя: выгрузка выплат',1,ARRAY['.xlsx']);

INSERT INTO merchants(id,code,name) VALUES
    ('c3d005d9-0415-59b8-811c-d456ee2ab959','NARKOMAN','Наркоман'),
    ('3010d8de-a39e-52c0-afcc-39e5e02c6069','SVETA','Света'),
    ('da1e2447-2611-5abe-8521-3e1ab8c76c18','YURA','Юра'),
    ('1c9e6f0b-4e76-5890-864d-e624b48ea895','TOLYA','Толя'),
    ('f9d03b20-5f6f-5649-b726-0183def097c0','KATYA','Катя'),
    ('b762d4b9-114b-5e6e-ad5d-b1c97d0a1dfb','ALITEN','Алитен'),
    ('38dcc702-da91-5157-9a7a-d779c0fcc5ee','DIMA','Дима'),
    ('fdbac4ff-231d-5f41-b09d-91cdc0ca1c62','SEREZHA','Сережа')
ON CONFLICT (code) DO UPDATE SET name=EXCLUDED.name,active=true;

INSERT INTO merchant_import_profiles(merchant_id,parser_code,status)
SELECT id, CASE code
    WHEN 'NARKOMAN' THEN 'narkoman_avangard_xls_v1'
    WHEN 'SVETA' THEN 'sveta_cards_xls_v1'
    WHEN 'TOLYA' THEN 'tolya_operations_xlsx_v1'
    WHEN 'KATYA' THEN 'katya_payouts_xlsx_v1'
    WHEN 'ALITEN' THEN 'aliten_bank_csv_v1'
END,
CASE WHEN code IN ('YURA','DIMA','SEREZHA') THEN 'awaiting_sample' ELSE 'configured' END
FROM merchants WHERE code IN ('NARKOMAN','SVETA','YURA','TOLYA','KATYA','ALITEN','DIMA','SEREZHA');

-- Preserve the pre-004 behavior of already existing merchants.
INSERT INTO merchant_import_profiles(merchant_id,parser_code,status)
SELECT m.id,'generic_xlsx_v1','configured'
FROM merchants m
WHERE NOT EXISTS (SELECT 1 FROM merchant_import_profiles p WHERE p.merchant_id=m.id);

INSERT INTO schema_migrations(version) VALUES (4);

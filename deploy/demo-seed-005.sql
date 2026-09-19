-- Synthetic demo contacts only. Apply only to metallist_demo after schema 005.
DO $$
BEGIN
    IF current_database() <> 'metallist_demo' THEN
        RAISE EXCEPTION 'unexpected database: %', current_database();
    END IF;
    IF NOT EXISTS (SELECT 1 FROM schema_migrations WHERE version = 5) THEN
        RAISE EXCEPTION 'schema version 5 is required';
    END IF;
END $$;

INSERT INTO payment_contacts(id,full_name,phone) VALUES
    ('59ad624c-0067-50bf-90aa-05633c05f8d1','Тестов Алексей Учебович','+70000000001'),
    ('e8fcd580-a0d1-567f-834e-ec7e80db92d2','Демина Мария Примеровна','+70000000002'),
    ('bfef7345-38e2-5e75-a619-7575c6e2e056','Образцов Илья Тестович','+70000000003')
ON CONFLICT (lower(full_name),phone) DO NOTHING;

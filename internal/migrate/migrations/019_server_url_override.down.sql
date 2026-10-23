-- 回滚「连接地址覆盖」重命名
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'hosts'
          AND column_name = 'server_url_override'
    ) THEN
        ALTER TABLE hosts RENAME COLUMN server_url_override TO server_tunnel_url;
    END IF;
END $$;

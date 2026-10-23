-- 将「平台侧隧道地址」重命名为「连接地址覆盖」
--
-- 该字段不参与隧道建立：隧道由隧道软件（WireGuard/IPSec/frp/专线等）负责，
-- 这里只是覆盖 Agent 主动外连时使用的平台地址（例如某台主机只能走隧道入口）。
-- 改名后语义更准确，避免被误解为平台需要参与建隧道。

DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_schema = current_schema()
          AND table_name = 'hosts'
          AND column_name = 'server_tunnel_url'
    ) THEN
        ALTER TABLE hosts RENAME COLUMN server_tunnel_url TO server_url_override;
    END IF;
END $$;

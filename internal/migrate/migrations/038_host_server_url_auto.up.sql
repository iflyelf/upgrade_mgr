-- 连接地址覆盖来源：区分「手工填写」与「由 server.external_url 自动同步」。
--
-- 开启 server.auto_sync_agent_url 后，保存 server.external_url 会自动写入
-- 所有未手工覆盖连接地址的主机（server_url_auto=true）。之后再次修改
-- external_url 时只更新这些自动同步的主机，手工覆盖不会被覆盖。
-- 运维在主机编辑中手工修改/清空连接地址后，server_url_auto 置 false。

ALTER TABLE hosts
    ADD COLUMN IF NOT EXISTS server_url_auto BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN hosts.server_url_auto IS '连接地址覆盖是否由 server.external_url 自动同步（手工修改后置 false）';

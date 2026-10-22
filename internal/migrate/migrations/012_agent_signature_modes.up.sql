-- Agent 指令签名能力标识
--
-- Agent 注册/心跳时上报可校验的签名方式（逗号分隔的稳定顺序）：
--   id          旧版 signature，Path 仅指令 ID
--   signed_path 旧版 signature，Path 为 ID|类型|过期时间
--   v2          支持 signature_v2 字段
-- 服务端据此选择 legacy signature 的签名路径，不再依赖 Agent 版本推断。
-- 旧 Agent 不上报该字段，默认 id，保持向后兼容。

ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS signature_modes VARCHAR(128) NOT NULL DEFAULT 'id';

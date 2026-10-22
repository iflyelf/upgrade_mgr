-- 回滚 Agent 指令签名能力标识

ALTER TABLE agents
    DROP COLUMN IF EXISTS signature_modes;

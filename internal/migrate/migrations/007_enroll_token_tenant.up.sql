-- 为 enroll_tokens 添加租户隔离字段，并清理旧的重复索引

-- 删除 006 中误建的重复索引（001 迁移已创建 idx_commands_task）
DROP INDEX IF EXISTS idx_commands_task_id;

-- 添加租户字段（默认租户 1）
ALTER TABLE enroll_tokens ADD COLUMN IF NOT EXISTS tenant_id INT NOT NULL DEFAULT 1;

-- 添加租户外键和索引
ALTER TABLE enroll_tokens ADD CONSTRAINT fk_enroll_tokens_tenant 
    FOREIGN KEY (tenant_id) REFERENCES tenants(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_enroll_tokens_tenant ON enroll_tokens(tenant_id);

-- 修复 audit_logs 结构漂移（幂等，向前兼容）
--
-- 背景：某中间版本曾以不同字段定义重复创建 audit_logs（ip/resource/success/message），
-- 与权威定义（迁移 001：ip_address/resource_type/resource_id/status/error_message）不一致。
-- 已部署过该中间版本的环境，表结构会缺列，导致审计写入持续失败。
-- 本迁移按权威定义补齐缺失列，使任意历史状态都能收敛到一致结构。

-- 表不存在时按权威定义创建（全新库已由 001 建好，此处为 no-op）
CREATE TABLE IF NOT EXISTS audit_logs (
    id            BIGSERIAL PRIMARY KEY,
    user_id       VARCHAR(100),
    username      VARCHAR(100),
    action        VARCHAR(64),
    resource_type VARCHAR(64),
    resource_id   VARCHAR(100),
    resource_name VARCHAR(255),
    details       JSONB,
    ip_address    VARCHAR(64),
    status        VARCHAR(24),
    error_message TEXT,
    created_at    TIMESTAMPTZ DEFAULT NOW()
);

-- 补齐权威定义中的列（对已是正确结构的库全部为 no-op）
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS user_id       VARCHAR(100);
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS username      VARCHAR(100);
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS action        VARCHAR(64);
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS resource_type VARCHAR(64);
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS resource_id   VARCHAR(100);
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS resource_name VARCHAR(255);
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS details       JSONB;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS ip_address    VARCHAR(64);
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS status        VARCHAR(24);
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS error_message TEXT;
ALTER TABLE audit_logs ADD COLUMN IF NOT EXISTS created_at    TIMESTAMPTZ DEFAULT NOW();

-- 中间版本遗留列若带 NOT NULL 约束，会阻塞按权威定义的插入，放开为可空
ALTER TABLE audit_logs ALTER COLUMN action DROP NOT NULL;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns
               WHERE table_name = 'audit_logs' AND column_name = 'success') THEN
        ALTER TABLE audit_logs ALTER COLUMN success DROP NOT NULL;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_audit_created ON audit_logs(created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_user    ON audit_logs(user_id);

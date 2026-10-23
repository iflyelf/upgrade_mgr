-- 任务查询索引优化
--
-- 背景：
--   - 组件删除前会按 component_id 检查引用，缺索引时全表扫描；
--   - 任务列表默认按 (tenant_id, status) + created_at 排序；
--   - 任务名搜索使用 ILIKE '%q%'，普通 B-Tree 无法命中。

CREATE INDEX IF NOT EXISTS idx_upgrade_tasks_component
    ON upgrade_tasks(component_id);

CREATE INDEX IF NOT EXISTS idx_upgrade_tasks_tenant_status_created
    ON upgrade_tasks(tenant_id, status, created_at DESC);

-- pg_trgm 为可选扩展：托管数据库无权限安装时跳过，不影响迁移成功。
DO $$
BEGIN
    CREATE EXTENSION IF NOT EXISTS pg_trgm;
    CREATE INDEX IF NOT EXISTS idx_upgrade_tasks_name_trgm
        ON upgrade_tasks USING gin (name gin_trgm_ops);
EXCEPTION WHEN OTHERS THEN
    RAISE NOTICE 'pg_trgm 不可用，跳过任务名模糊搜索索引: %', SQLERRM;
END $$;

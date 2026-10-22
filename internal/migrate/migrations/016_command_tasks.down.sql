-- 回滚一次性命令任务
--
-- 先处理 kind='command' 的任务：component_id 为 NULL 无法满足原来的 NOT NULL 约束。
-- 这些任务及其批次/逐机记录一并删除（命令任务本身没有历史复用价值）。
DELETE FROM upgrade_tasks WHERE kind = 'command';

ALTER TABLE upgrade_tasks
    DROP COLUMN IF EXISTS command,
    DROP COLUMN IF EXISTS kind;

ALTER TABLE upgrade_tasks ALTER COLUMN component_id SET NOT NULL;

ALTER TABLE agents DROP COLUMN IF EXISTS capabilities;

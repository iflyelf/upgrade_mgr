-- 团队级 owner 策略：覆盖全局 agent.task_owner_only
--
-- 背景：全局开关无法满足「某些团队要求更严格的协作边界，另一些团队仍需共享操作」。
-- owner_only 取值：
--   inherit = 跟随全局设置（默认）
--   on      = 该团队成员只能操作自己创建的任务
--   off     = 该团队成员可操作团队内的任务（不受全局开关影响）
--
-- 判定顺序：非管理员 → 取用户所属团队（任一团队 on 即受限；全部 off 则放行）；
-- 未加入任何团队或全部 inherit → 回退全局设置。

ALTER TABLE teams
    ADD COLUMN IF NOT EXISTS owner_only VARCHAR(16) NOT NULL DEFAULT 'inherit';

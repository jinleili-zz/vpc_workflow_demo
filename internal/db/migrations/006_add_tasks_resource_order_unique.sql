-- 006_add_tasks_resource_order_unique.sql
-- 幂等改造（设计文档 7.8 节数据模型问题）：
-- tasks (resource_id, task_order) 唯一约束。同一资源同一工作流步骤最多一条 Task，
-- 重复提交工作流时 BatchCreate 会原子失败，防止产生同序重复 Task 与计数错乱。
CREATE UNIQUE INDEX IF NOT EXISTS uq_tasks_resource_order ON tasks (resource_id, task_order);

ALTER TABLE tasks
    ADD COLUMN priority INT DEFAULT 3 COMMENT '任务优先级: 1=低, 3=普通, 6=高, 9=紧急' AFTER status,
    ADD COLUMN device_type VARCHAR(32) DEFAULT 'switch' COMMENT '设备类型: switch, loadbalancer, firewall' AFTER priority,
    ADD INDEX idx_device_type (device_type),
    ADD INDEX idx_priority (priority);

# AZ NSP Asynq + Worker 交互示意图

```mermaid
flowchart LR
    subgraph AZ_NSP[AZ NSP]
        API[API 处理器<br/>(创建 VPC/子网请求)]
        Orchestrator[编排器<br/>(任务链/流程控制)]
        AsynqClient[asynq.Client<br/>(入队任务)]
    end

    subgraph Redis[Redis / Asynq Broker]
        Queue[(队列: vpc_tasks_{region}_{az})]
    end

    subgraph Workers[Workers]
        SwitchWorker[交换机 Worker<br/>(create_vrf_on_switch)]
        VlanWorker[VLAN Worker<br/>(create_vlan_subinterface)]
        FirewallWorker[防火墙 Worker<br/>(create_firewall_zone)]
    end

    API --> Orchestrator --> AsynqClient -->|enqueue| Queue
    Queue -->|dequeue| SwitchWorker -->|enqueue next| Queue
    Queue -->|dequeue| VlanWorker -->|enqueue next| Queue
    Queue -->|dequeue| FirewallWorker -->|完成| Orchestrator
```

说明：
- AZ NSP 的编排器负责按链式流程将任务依次入队。每个任务完成后会继续把下一个任务入队。
- 队列按 AZ 维度隔离，避免跨 AZ 竞争（示例队列名：`vpc_tasks_{region}_{az}`）。
- Worker 从队列中拉取任务执行，执行完成后把下一步任务写回队列。

# VPC Workflow Architecture (With Hardware Devices & VFW Service)

```
+========================================================================================================+
|                                      Global Control Plane                                              |
+========================================================================================================+
|                                                                                                        |
|  +-------------------------------------------+    +-------------------------------------------+        |
|  |        Top NSP VPC (Port: 8080)           |    |        Top NSP VFW (Port: 8082)           |        |
|  |        [VPC/Subnet Orchestration]         |    |        [Firewall Policy Orchestration]    |        |
|  +-------------------------------------------+    +-------------------------------------------+        |
|  |  Orchestrator          |  AZ Registry     |    |  PolicyService       |  AZ Registry      |        |
|  |  - Region coordination |  - AZ VPC reg    |    |  - IP->Zone lookup   |  - AZ VFW reg     |        |
|  |  - Parallel dispatch   |  - 60s heartbeat |    |  - Cross-AZ policy   |  - 60s heartbeat  |        |
|  |  - Rollback on failure |                  |    |  - Parallel dispatch |                   |        |
|  +-------------------------------------------+    +-------------------------------------------+        |
|  |  API:                                     |    |  API:                                     |        |
|  |  - POST /api/v1/vpc                       |    |  - POST /api/v1/firewall/policy           |        |
|  |  - POST /api/v1/subnet                    |    |  - GET  /api/v1/firewall/policy/:id       |        |
|  |  - GET  /api/v1/regions                   |    |  - GET  /api/v1/firewall/policies         |        |
|  +-------------------------------------------+    +-------------------------------------------+        |
|                     |                                              |                                   |
+=====================|==============================================|===================================+
                      |                                              |
        +-------------+----------------------------------------------+-------------+
        |             |                                              |             |
        v             v                                              v             v
+=============================== cn-beijing Region ================================================+
|                                                                                                  |
|  +------------------------------------------+    +------------------------------------------+    |
|  |         AZ: cn-beijing-1a                |    |         AZ: cn-beijing-1b                |    |
|  +------------------------------------------+    +------------------------------------------+    |
|  |  +----------------+  +----------------+  |    |  +----------------+  +----------------+  |    |
|  |  | AZ NSP VPC     |  | AZ NSP VFW     |  |    |  | AZ NSP VPC     |  | AZ NSP VFW     |  |    |
|  |  | (Auto-register)|  | (Auto-register)|  |    |  | (Auto-register)|  | (Auto-register)|  |    |
|  |  | - VPC workflow |  | - Policy CRUD  |  |    |  | - VPC workflow |  | - Policy CRUD  |  |    |
|  |  | - Subnet mgmt  |  | - VFW Orch     |  |    |  | - Subnet mgmt  |  | - VFW Orch     |  |    |
|  |  +-------+--------+  +-------+--------+  |    |  +-------+--------+  +-------+--------+  |    |
|  |          |                   |           |    |          |                   |           |    |
|  |          +--------+----------+           |    |          +--------+----------+           |    |
|  |                   |                      |    |                   |                      |    |
|  |          +--------+--------+             |    |          +--------+--------+             |    |
|  |          |                 |             |    |          |                 |             |    |
|  |          v                 v             |    |          v                 v             |    |
|  |    +-----------+     +-----------+       |    |    +-----------+     +-----------+       |    |
|  |    | Switch    |     | Firewall  |       |    |    | Switch    |     | Firewall  |       |    |
|  |    | Worker    |     | Worker    |       |    |    | Worker    |     | Worker    |       |    |
|  |    +-----+-----+     +-----+-----+       |    |    +-----+-----+     +-----+-----+       |    |
|  |          |                 |             |    |          |                 |             |    |
|  |          v                 v             |    |          v                 v             |    |
|  |    +-----------+     +-----------+       |    |    +-----------+     +-----------+       |    |
|  |    |  Switch   |     | Firewall  |       |    |    |  Switch   |     | Firewall  |       |    |
|  |    |  Device   |     |  Device   |       |    |    |  Device   |     |  Device   |       |    |
|  |    |   [HW]    |     |   [HW]    |       |    |    |   [HW]    |     |   [HW]    |       |    |
|  |    +-----------+     +-----------+       |    |    +-----------+     +-----------+       |    |
|  +------------------------------------------+    +------------------------------------------+    |
+==================================================================================================+

+=============================== cn-shanghai Region ===============================================+
|                                                                                                  |
|  +------------------------------------------+                                                    |
|  |         AZ: cn-shanghai-1a               |                                                    |
|  +------------------------------------------+                                                    |
|  |  +----------------+  +----------------+  |                                                    |
|  |  | AZ NSP VPC     |  | AZ NSP VFW     |  |                                                    |
|  |  | (Auto-register)|  | (Auto-register)|  |                                                    |
|  |  | - VPC workflow |  | - Policy CRUD  |  |                                                    |
|  |  | - Subnet mgmt  |  | - VFW Orch     |  |                                                    |
|  |  +-------+--------+  +-------+--------+  |                                                    |
|  |          |                   |           |                                                    |
|  |          +--------+----------+           |                                                    |
|  |                   |                      |                                                    |
|  |          +--------+--------+             |                                                    |
|  |          |                 |             |                                                    |
|  |          v                 v             |                                                    |
|  |    +-----------+     +-----------+       |                                                    |
|  |    | Switch    |     | Firewall  |       |                                                    |
|  |    | Worker    |     | Worker    |       |                                                    |
|  |    +-----+-----+     +-----+-----+       |                                                    |
|  |          |                 |             |                                                    |
|  |          v                 v             |                                                    |
|  |    +-----------+     +-----------+       |                                                    |
|  |    |  Switch   |     | Firewall  |       |                                                    |
|  |    |  Device   |     |  Device   |       |                                                    |
|  |    |   [HW]    |     |   [HW]    |       |                                                    |
|  |    +-----------+     +-----------+       |                                                    |
|  +------------------------------------------+                                                    |
+==================================================================================================+

+==================================================================================================+
|                                  Data & Message Layer                                            |
+==================================================================================================+
|  +------------------------------------------+    +------------------------------------------+    |
|  |               MySQL                      |    |               Redis                      |    |
|  +------------------------------------------+    +------------------------------------------+    |
|  |  VPC Database:                           |    |  DB0: Data Store                         |    |
|  |  - vpc_registry (VPC info)               |    |  - workflow:{id}:state                   |    |
|  |  - subnet_registry (Subnet info)         |    |  - AZ registration cache                 |    |
|  |  - tasks (VPC/Subnet tasks)              |    +------------------------------------------+    |
|  +------------------------------------------+    |  DB1: Message Queue (Asynq)              |    |
|  |  VFW Database:                           |    |  - vpc_tasks_{region}_{az}               |    |
|  |  - firewall_policies (Policy registry)   |    |  - subnet_tasks_{region}_{az}            |    |
|  |  - policy_az_records (AZ records)        |    |  - firewall_tasks_{region}_{az}          |    |
|  |  - vfw_tasks (VFW tasks)                 |    |  - callback_queue_{region}_{az}_vfw      |    |
|  +------------------------------------------+    +------------------------------------------+    |
+==================================================================================================+


=== VFW Policy Creation Workflow ===

+---------------+      +--------------------+      +------------------------+
| User Request  | ---> | Top NSP VFW        | ---> | IP -> Zone Lookup      |
| (src/dst IP)  |      | (Port 8082)        |      | (Query VPC MySQL)      |
+---------------+      +--------------------+      +------------------------+
                                                              |
                              +-------------------------------+
                              |
                              v
               +-----------------------------+
               | Determine Target AZ(s)      |
               | - Same AZ: 1 target         |
               | - Cross AZ: 2 targets       |
               +-----------------------------+
                              |
            +-----------------+-----------------+
            |                                   |
            v                                   v
+------------------------+           +------------------------+
| AZ NSP VFW (Source AZ) |           | AZ NSP VFW (Dest AZ)   |
| - VFW Orchestrator     |           | - VFW Orchestrator     |
| - Create policy record |           | - Create policy record |
+------------------------+           +------------------------+
            |                                   |
            v                                   v
+------------------------+           +------------------------+
| Firewall Worker        |           | Firewall Worker        |
| (Asynq consumer)       |           | (Asynq consumer)       |
+------------------------+           +------------------------+
            |                                   |
            v                                   v
+------------------------+           +------------------------+
| Firewall Device [HW]   |           | Firewall Device [HW]   |
| - Configure policy     |           | - Configure policy     |
+------------------------+           +------------------------+


=== VPC Creation Workflow (Chain Mode) ===

+--------+     +----------------+     +---------------------+     +------------------+
| API    | --> | 1. create_vrf  | --> | 2. create_vlan      | --> | 3. create_fw     |
| Request|     |    on_switch   |     |    subinterface     |     |    zone          |
+--------+     +-------+--------+     +----------+----------+     +---------+--------+
                       |                         |                          |
                       v                         v                          v
               +-------+--------+       +--------+---------+       +--------+---------+
               | Switch Worker  |       | Switch Worker    |       | Firewall Worker  |
               +-------+--------+       +--------+---------+       +--------+---------+
                       |                         |                          |
                       v                         v                          v
               +-------+--------+       +--------+---------+       +--------+---------+
               |  Switch Device |       |  Switch Device   |       | Firewall Device  |
               |  - VRF Config  |       |  - VLAN Config   |       |  - Zone Config   |
               +----------------+       +------------------+       +------------------+
                    [HW]                      [HW]                       [HW]


=== Legend ===

  [HW]         = Physical Hardware Device
  Worker       = Software process handling device configuration via Asynq queue
  AZ NSP VPC   = Availability Zone VPC Service (VPC/Subnet management)
  AZ NSP VFW   = Availability Zone VFW Service (Firewall Policy management)
  Top NSP VPC  = Global VPC orchestrator (Port 8080)
  Top NSP VFW  = Global Firewall Policy orchestrator (Port 8082)
```

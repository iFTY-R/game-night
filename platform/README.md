# Platform

平台目录承载身份、资料、房间、大厅、治理、复盘、主题目录、游戏运行时和持久化适配器。

平台模块不能导入具体游戏。跨模块协作通过明确接口、事务端口和领域事件完成，领域模块不得反向导入 `platform/persistence` 实现。

基础设施依赖按适配器隔离：

- PostgreSQL 和 `database/sql` 只允许出现在 `platform/persistence/postgres`。
- Redis 客户端只允许出现在 `platform/persistence/redis`。
- AWS SDK 和对象存储 HTTP 客户端只允许出现在 `platform/persistence/objectstorage`。
- 领域模块不导入 `net/http`；HTTP 和 ConnectRPC 传输由 `apps` 持有。

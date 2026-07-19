# 吹牛骰子模块

本目录维护 `liars-dice` 的独立协议、纯规则引擎、投影、SDK 适配、客户端、主题和测试。规则来源为 `docs/superpowers/specs/2026-07-20-liars-dice-rules-design.md`。

`engine` 不得依赖时间、随机、网络、数据库或平台实现；随机种子和时限由 GameSession 运行时注入。

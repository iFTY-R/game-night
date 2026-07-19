# Contracts

`platform/` 保存平台级 Protocol Buffers；每款游戏的协议保存在对应 `games/<game>/proto/`。

平台生成代码写入 `contracts/gen/go` 和 `contracts/gen/ts`；游戏 Go 生成代码写入各自 `games/<game>/gen/go`，游戏 TypeScript 生成代码写入各自 `games/<game>/client/src/generated`。这些目录只由 `pnpm run generate` 更新。禁止手工编辑生成文件，也禁止将所有游戏消息合并进平台级 `oneof`。

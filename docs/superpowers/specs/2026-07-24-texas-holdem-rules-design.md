# 德州扑克独立规则与活动架构规范

> 日期：2026-07-24
>
> 状态：已封存（2026-07-24）；独立规范审查已通过，暂不实施
>
> 游戏 ID：`texas-holdem`
>
> 支持模式：`cash`、`tournament`

> 封存决定：本规范作为已审查的只读设计依据保留，不进入当前实施计划、平台迁移、游戏注册、开发任务或发布范围。重新启用必须由用户明确确认，并先按届时平台现状复核本规范与实施依赖；不得仅因本文档已完成而自动恢复开发。

## 1. 目标与边界

本规范冻结德州扑克的完整产品范围、服务端权威规则、现金桌与锦标赛活动模型、单手状态机、移动端交互、私牌投影、复盘、安全边界和测试不变量。它不是 MVP 规则裁剪：现金桌、单桌锦标赛、多桌锦标赛、延迟报名、重购、重新参赛、加购、自动调桌、发两次、观战和完整复盘都属于正式范围。

默认产品入口是现金桌。德州筹码只在当前活动内作为虚拟计分，不对应真实货币，不提供购买、提现、赠送、转账、抽水或跨房间钱包。跨活动只保留统计结果，不保留可消费筹码余额。

旧参考目录 `E:/WorkPro/Project_20260305/nuxt-games/docx/texas-holdem` 只用于理解玩法概念，不复用其技术方案、状态对象、接口或页面。旧资料同时描述无限注、固定限注和底池限注，而本规范只支持无限注德州扑克；任何与本规范冲突的旧规则都不是权威来源。

## 2. 核心架构决策

当前平台的 `GameSession` 以冻结参与者、单一状态版本、单一实时路由和单一复盘资源为核心不变量。现金桌中途入座、锦标赛延迟报名和跨桌调度要求参与者只在两手之间变化，因此德州不得把整场现金桌或多桌赛事塞入一个长生命周期 `GameSession`。

德州运行时固定为三层：

```text
PartyRoom
  -> GameActivity (一场现金桌或锦标赛)
      -> Table (长期稳定的牌桌容器)
          -> Hand GameSession (一手牌，冻结参与者)
```

- `PartyRoom` 管理持续社交关系、邀请、成员、房主和房间解散。
- `GameActivity` 管理整场活动的配置、筹码账本、报名、赛事时钟、排名和活动终态。
- `Table` 管理稳定的 `table_id`、座位、按钮、等待队列、当前手和上一手。
- `Hand GameSession` 只管理一手牌的强制注、发牌、下注、边池、摊牌和结算。
- 每手结束后创建新的子会话；页面与牌桌不跳转，只在后台切换当前手订阅。

房间需要引入 `ActiveRuntimeRef`，其 `kind` 为 `session` 或 `activity`。现有四款游戏继续使用 `session`，德州使用 `activity`；任一时刻房间最多只有一个活跃运行引用。客户端适配层把两种运行类型统一为游戏入口，但持久化层不伪造不存在的德州长会话。

### 2.1 强制平台迁移

`ActiveRuntimeRef` 不是德州模块内部别名，而是 Room、Game、Realtime、Replay 和 Web 共同采用的平台契约。规范形态为：

```text
ActiveRuntimeRef {
  kind: session | activity
  runtime_id: UUID
  game_id: string
}
```

三个字段必须同时存在并指向同一 `room_id` 下的权威对象；未知 `kind`、空 ID、目标不存在、目标游戏不一致或目标属于其他房间都按完整性错误 fail closed。Room 在 `playing` 状态必须且只能有一个 `ActiveRuntimeRef`，在 `post_game/closed` 状态不得有活跃引用。`last_finished_runtime_ref` 使用同一结构，但只允许指向终态对象。

迁移范围是强制且不可拆分的：

- `contracts/platform/room/v1/room.proto`、Room domain 和 PostgreSQL 约束改为表达顶层 runtime，而不是把 `active_session_id` 当成唯一运行类型。
- `GameService` 的现有 `StartSession/FinishSession` 继续只服务 session 型游戏；新增 `ActivityService` 的 `StartActivity/FinishActivity/CreateHand/FinishHand` 负责活动命令、活动投影、活动订阅授权和活动复盘。`CreateHand/FinishHand` 必须走 activity/table 事务，不能调用旧 Room `StartSession/FinishSession`。
- `contracts/platform/game/v1/game.proto` 与 Realtime grant 增加 `activity_id/table_id/session_id` 血缘和授权代际。德州 hand grant 不再仅凭 `room_id + session_id + PartyRoom seat` 授权。
- API 和 Realtime 的路由、租约、撤销、outbox/inbox 消费同时识别 activity 与 hand session；任何内部 hand session 都不能占用 Room 的顶层 `active_session_id`。
- Web Room store 保存 `activeRuntimeRef`；router、运行页分派器和 live composable 按 `runtimeKind` 进入 session 页或 activity/table 页。德州换手只切换 hand 订阅，不改稳定路由。
- Replay ACL 分成旧 session ACL 与 activity 派生 ACL。德州子会话复盘从父 activity 授权，不从手牌结束时的 PartyRoom 成员快照推导。

在当前仓库中的最小迁移落点必须显式登记为：`contracts/platform/room/v1/room.proto`、`platform/room/model.go`、`infra/migrations/00013_party_rooms.sql` 及其后续 Room/session 约束迁移；`contracts/platform/game/v1/game.proto`、`contracts/platform/realtime/v1/realtime.proto`；`apps/api/internal/transport/room/service.go` 与 Game/Activity transport；`apps/realtime/internal/subscription/authorizer.go`、`apps/realtime/internal/subscription/hub.go`；`apps/web/src/stores/room.ts`、`apps/web/src/router.ts`、`apps/web/src/composables/use-live-game-table.ts`、运行页分派器；以及 `platform/persistence/postgres/room_game_session_repository.go`、Replay transport/`apps/web/src/views/ReplaySessionView.vue`。这些面必须在同一 runtime-kind 迁移中完成双读双写和回归，不能只新增德州目录。

顶层生命周期固定为：现有游戏 `Room.StartSession -> session -> Room.FinishSession -> post_game`；德州 `Room.StartActivity -> activity -> Room.FinishActivity -> post_game`。`Hand GameSession` 的创建者是持有活动 lease 与 fencing token 的系统协调器，不能要求 PartyRoom 房主在该桌入座，也不能复用“房主必须是本局参与者”的旧校验。单手终局只回写 Table 与 GameActivity，绝不调用 Room 的 session finish 路径。

## 3. 术语与身份

- `user_id`：平台稳定用户身份，同一房间内任何客户端都必须显示相同用户名。
- `entry_id`：一次活动参与记录，现金桌入场和锦标赛参赛都必须拥有。现金桌完全离场后重新加入、锦标赛重新参赛都会为同一用户创建新的 `entry_id`，旧记录只读保留。
- `activity_id`：一场现金桌或锦标赛活动的稳定 ID。
- `table_id`：活动内一张长期牌桌的稳定 ID；调桌不会改变活动 ID。
- `session_id`：一手牌的子会话 ID；每手都会变化。
- `hand_no`：活动内按牌桌递增的手号，与 `table_id` 共同唯一定位一手。
- `seat_index`：牌桌内 `0..8` 的稳定座位位置，不等于 PartyRoom 的成员排列索引。
- `stack`：玩家或参赛记录当前可用于后续手牌的筹码。
- `committed`：当前手已投入且尚未结算的筹码。
- `eligible`：没有弃牌且对某个底池具有获奖资格的玩家。

同一用户在同一活动中最多有一个正在使用的 `entry_id`。历史重新参赛记录保留，但同一个 `entry_id` 不能同时占据两个座位、参与两手牌或被两个结算事务消费。

## 4. 人数、模式与默认配置

### 4.1 公共默认值

- 每张桌支持 2-9 个座位。
- 基础行动时间默认 20 秒，房主可配置 10-60 秒。
- 每名玩家初始时间银行 60 秒，参与并完成一手后补回 5 秒，上限 90 秒。
- 每手结果展示与下一手准备窗口默认 8 秒。
- 观战默认开放且实时；房主可关闭或设置 15、30、60 秒延迟。
- 主动暂停冻结行动时间、时间银行、下一手倒计时和锦标赛级别时钟。
- 断线不暂停任何计时；重连恢复权威状态。

活动人数上限与单手参与者上限是两个独立约束：一个锦标赛 activity 最多有 90 个有效参赛记录，一张 Table 和一个 Hand GameSession 只能冻结 2-9 名参与者。任何平台通用 session 上限适配都只能把德州 hand 上限设为 9，不能因活动支持 90 人而把 90 名玩家放进同一个 GameSession。

### 4.2 现金桌默认值

- 默认桌型：6 人桌，可配置 2-9 人。
- 小盲/大盲：`1/2`。
- 最小买入：40 BB；最大买入：100 BB。
- 前注：关闭。
- UTG live Straddle：关闭。
- 发两次：关闭。
- 抽水：永远为 0，不可配置。

### 4.3 锦标赛默认值

- 默认桌型：9 人桌，可配置 2-9 人。
- 活动参赛上限默认 90 人，部署级上限可进一步收紧，但不得在活动进行中改变。
- 默认赛制：单桌快速冻结赛。
- 延迟报名、重购、重新参赛和加购默认关闭。
- 前注默认使用大盲前注，并从预设第 4 级开始。
- 多桌赛事自动分桌、平衡、拆桌、合桌并生成决赛桌。
- 支持 hand-for-hand 同步发牌批次；默认在决赛桌泡沫阶段启用，房主可在开赛前增加其他名次检查点。

### 4.4 锦标赛预设

| 预设 | 起始筹码 | 级别时长 | 计划休息 | 适用场景 |
| --- | ---: | ---: | --- | --- |
| 快速 | 10,000 | 5 分钟 | 每 6 级后 3 分钟 | 聚会快速赛，默认 |
| 标准 | 15,000 | 10 分钟 | 每 6 级后 5 分钟 | 常规线上牌局 |
| 深筹 | 20,000 | 15 分钟 | 每 6 级后 5 分钟 | 长局与更深决策 |

预设的基础大盲序列为 `100, 150, 200, 300, 400, 600, 800, 1000, 1500, 2000, ...`。它按尾数循环 `1, 1.5, 2, 3, 4, 6, 8` 后进入下一个十进数量级；小盲固定为大盲的一半。第 1-3 级前注为 0，第 4 级起默认大盲前注等于大盲。服务端保存展开后的完整级别表，运行时不依赖上述生成描述或预设名称。

快速、标准和深筹都是可编辑起点。房主可以逐级配置小盲、大盲、前注模式、前注金额、持续时间和休息，但必须在开赛前通过服务端完整校验。

## 5. 牌组、牌 ID 与洗牌

### 5.1 规范牌组

使用 52 张标准扑克牌，不含大小王。规范花色后缀沿用仓库现有约定：`D=方块`、`C=梅花`、`H=红桃`、`S=黑桃`。点数为 `2-10,J,Q,K,A`。

规范牌组顺序固定为：

```text
[2D, 2C, 2H, 2S,
 3D, 3C, 3H, 3S,
 4D, 4C, 4H, 4S,
 5D, 5C, 5H, 5S,
 6D, 6C, 6H, 6S,
 7D, 7C, 7H, 7S,
 8D, 8C, 8H, 8S,
 9D, 9C, 9H, 9S,
 10D, 10C, 10H, 10S,
 JD, JC, JH, JS,
 QD, QC, QH, QS,
 KD, KC, KH, KS,
 AD, AC, AH, AS]
```

协议必须拒绝未知牌 ID、别名、小写 ID、前后空白和同一手内重复真实牌。花色没有强弱顺序，不能用于打破牌型并列。

### 5.2 确定性洗牌

平台使用密码学安全随机源为每手生成恰好 32 字节的 `shuffle_seed`，并以字段级加密保存。客户端、日志、指标、Redis 通知、普通事件投影和复盘不得获得种子。

手牌引擎按第 5.1 节规范顺序构造牌组，再使用 SHA-256 计数器字节流执行无偏 Fisher-Yates。常量 `shuffle_domain` 的精确 ASCII 字节为 `game-night/texas-holdem/shuffle/v1`。第 `counter` 个 32 字节块为：

```text
SHA-256(shuffle_domain || 0x00 || shuffle_seed || uint64_be(counter))
```

`next_u64` 每次消费 8 个连续字节并按无符号大端整数解释。对 `i = 51..1`，令 `n=i+1`、`limit=2^64-(2^64 mod n)`，持续取值直到 `x < limit`，然后交换 `deck[i]` 与 `deck[x mod n]`。禁止直接取模、浮点缩放、本机随机数或 map 遍历顺序。

当种子为 32 个 `0x00` 字节时，固定洗牌结果为：

```text
[7S, 7D, QC, 9C, 3D, 5S, 4C, 4D, 3C, 8S, 9H, 2H, 8H,
 6S, 8D, 6H, KS, 6C, 7C, AS, 10S, 6D, 10C, 5C, AH, 4H,
 KH, QD, 2D, 2C, 3H, KC, 9D, 10D, 9S, QS, JC, AC, 4S, 5H,
 2S, JD, 8C, AD, 3S, JS, 10H, JH, 7H, KD, QH, 5D]
```

以上数组用 ASCII 逗号连接且不含空格时，SHA-256 十六进制摘要必须为 `dbb3b2c1e5fa66be6c391761f385eabb05337cbeb23d6032e4bfc58ad94d2dbe`。

## 6. 牌型与比较

每位玩家使用自己的两张底牌与五张公共牌中的任意组合，取七张牌可组成的最佳五张牌。玩家可以使用 0、1 或 2 张底牌。

牌型从大到小固定为：

1. 同花顺。
2. 四条。
3. 葫芦。
4. 同花。
5. 顺子。
6. 三条。
7. 两对。
8. 一对。
9. 高牌。

皇家同花顺只是 A 高同花顺的显示名称，不是独立牌型等级。`A-2-3-4-5` 是最小顺子，高牌按 5 计算；`10-J-Q-K-A` 是最大顺子。`K-A-2-3-4` 等其他跨越组合不是顺子。

同牌型比较键固定为：

- 同花顺、顺子：顺子高牌。
- 四条：四条点数，再比较踢脚牌。
- 葫芦：三条点数，再比较对子点数。
- 同花、高牌：五张点数降序逐项比较。
- 三条：三条点数，再比较两张踢脚牌降序。
- 两对：较大对子、较小对子、踢脚牌。
- 一对：对子点数，再比较三张踢脚牌降序。

比较键完全相同即并列，花色、座位、行动速度和先亮牌顺序都不能打破并列。

牌型求值必须封装在独立适配器后。实施阶段优先选择经过维护性、许可证和正确性审查的成熟求值器，并用独立公开测试向量与穷举/属性测试交叉验证；不得把未验证的手写比较器作为唯一真相。

## 7. 按钮、盲注、前注与发牌

### 7.1 按钮与盲注

活动第一手通过权威随机源在符合资格的座位中选择按钮。此后每手结束，按钮顺时针移动到下一位符合参赛资格的玩家；小盲和大盲依次为按钮左侧最近的两位符合资格玩家。

两人局中按钮位同时是小盲：按钮位翻牌前先行动、翻牌后最后行动；另一位是大盲。三人桌变为两人桌时必须重新应用两人规则，不能机械沿用三人桌的盲注索引。

强制注最多投入玩家现有筹码，筹码不足时自动成为全押。即使大盲玩家不足额全押，翻牌前的完整 bring-in 仍等于配置的大盲金额，其他有足够筹码的玩家不能只跟到不足额大盲。三人桌转为两人桌时允许调整按钮，必须避免同一玩家连续两手支付大盲。

### 7.2 前注

前注支持三种模式：关闭、每人前注、大盲前注。每人前注由所有获发底牌玩家分别支付，并在普通盲注前扣除；大盲前注只由大盲位支付。大盲位筹码不足以同时支付完整大盲和大盲前注时固定使用 `BB-first`：先支付大盲，剩余筹码再支付前注。Straddle 最后扣除。任何强制注导致 stack 为 0 的玩家仍获发底牌并以全押状态参加本手。

### 7.3 现金桌 Straddle

现金桌可启用经典 UTG live Straddle，默认关闭。只有大盲左侧第一位符合资格且 stack 至少为 2 BB 的玩家可以在发牌前预设支付恰好 2 BB；不足 2 BB 时不产生 Straddle。该投入是 live blind，翻牌前行动顺序从 Straddle 左侧开始，Straddle 玩家最后拥有未加注底池的过牌/加注选择。锦标赛不支持 Straddle。

### 7.4 发底牌与烧牌

从按钮左侧第一位获发资格玩家开始，按顺时针发两轮，每轮每人一张。翻牌、转牌和河牌前各烧一张：翻牌发三张公共牌，转牌和河牌各发一张。烧牌只存在于权威状态和审计记录，不进入普通投影。

## 8. 单手状态机

单手阶段及其条件分支固定为：

1. `posting_forced_bets`：锁定按钮、盲注、前注和可选 Straddle。
2. `dealing`：确定性洗牌并发两张私有底牌。
3. `preflop`：第一轮下注。
4. `flop`：烧一张，发三张公共牌并下注。
5. `turn`：烧一张，发第四张公共牌并下注。
6. `river`：烧一张，发第五张公共牌并下注。
7. `runout_decision`：从 preflop、flop 或 turn 在“无人还能行动且公共牌未发完”时条件进入，收集发两次选择；未启用发两次时直接跳过。
8. `runout`：按已确定的一次或两次方案发完剩余公共牌。
9. `showdown`：按公开顺序亮牌、允许合法 muck，并计算各底池。
10. `settled`：生成不可变终局结果，子会话终止。
11. `cancelled`：平台紧急取消，不生成本手赢家。

任意下注阶段只剩一名未弃牌玩家时立即结束下注，该玩家不亮牌获得所有无人争议底池。仍有至少两名未弃牌玩家、但所有人都已全押或没有可继续下注者时，停止等待行动并进入发完公共牌/摊牌流程。

## 9. 行动顺序与合法操作

翻牌前从当前最大 live blind 左侧第一位可行动玩家开始，按顺时针行动；若无 Straddle，则从大盲左侧开始。翻牌、转牌和河牌从按钮左侧第一位可行动玩家开始。

服务端按当前权威状态计算并投影以下合法动作及金额范围：

- `fold`：玩家面对任何状态都可弃牌，但无下注时客户端应把过牌作为主操作。
- `check`：当前轮投入已经等于最高需跟金额时可用。
- `call`：补足到当前最高需跟金额；筹码不足时投入剩余全部筹码。
- `bet`：当前轮尚无主动下注时可用，完整下注至少为一个大盲。
- `raise`：当前轮已有下注时可用，必须满足重新开放与最小加注规则。
- `all_in`：投入全部剩余筹码；可能是跟注、不足额下注、不足额加注或完整加注。

客户端提交目标总投入金额，不提交由客户端计算的增量。服务端必须重新计算 `to_call`、`minimum_raise_to`、`maximum_raise_to`、玩家剩余筹码和行动资格。

## 10. 无限注、最小加注与重新开放

- 首次完整下注至少为一个大盲；筹码少于一个大盲时允许不足额全押下注。
- 完整加注的增量不得小于本轮最近一次完整下注或完整加注的增量。
- 例如当前下注从 20 加到 60，完整加注增量为 40，下一次完整加注至少加到 100。
- 不足完整增量的全押仍会提高当前跟注额，但单次不足额全押不会自动为已经行动过的玩家重新开放加注。
- 对已行动玩家，从其上一次完成行动时面对的金额起，后续一个或多个不足额全押累计达到一个完整加注增量时，重新开放加注权。
- 尚未在当前完整下注后行动的玩家始终保留其正常弃牌、跟注或合法加注选择。
- 大盲在未被加注的翻牌前底池中保留过牌或加注选择。

一轮下注结束的条件是：除已弃牌和已全押玩家外，所有仍可行动玩家都已在最近一次完整加注后行动，且其当前轮投入相同；或者只剩一名未弃牌玩家。

任何无人跟注的超额投入在构造底池前原额退还最后投入者，不进入主池或边池。

## 11. 自动行动、超时与时间银行

玩家可预设：

- `auto_check`：轮到自己且仍可过牌时自动过牌，否则取消。
- `check_or_fold`：可过牌则过牌，否则弃牌。
- `call_exact`：只有所需跟注额仍等于预设时的精确金额时才跟注；金额增加即取消。

不提供自动下注、自动加注、自动全押或默认跟任意金额。全押和投入超过当前 stack 50% 的下注/加注必须二次确认。

基础行动时间耗尽后自动开始消耗个人时间银行，按实际经过时间扣减。基础时间和时间银行都耗尽时，可过牌则自动过牌，否则自动弃牌。断线使用同一规则，不获得额外时间。参与并完成一手后补回 5 秒；坐出或未获发底牌不补回。

客户端最后 5 秒提供明显但可关闭的声音、振动和视觉提醒。客户端倒计时只负责显示，服务端截止时间才是权威。

## 12. 底池、边池与奇数筹码

底池按每名玩家本手总投入构造。先按升序取得全部不同的正投入层级；对每个层级区间，把仍达到该层级的所有玩家投入计入一个底池层。已弃牌玩家的筹码仍计入金额，但不进入资格集合。

每个主池或边池独立在其资格玩家中比较最佳五张牌。一个底池只有一名合资格玩家时直接归该玩家，不需要为该底池亮出额外牌。

并列赢家先平均分配。不能均分的最小筹码从按钮左侧第一位属于该底池赢家集合的玩家开始，按顺时针每人一枚循环分配。花色不能决定奇数筹码。

结算结果必须包含每个底池的来源层级、总额、合资格玩家、赢家、牌型、平均份额和奇数筹码接收者。活动层只消费服务端生成的权威结果，不在客户端重新计算。

## 13. 摊牌、muck 与主动亮牌

如果其他玩家全部弃牌，赢家底牌默认保密；结果展示期间可以主动亮一张或两张，公开选择一旦提交不可撤回。

正常摊牌顺序为：

1. 河牌轮最后一个主动下注或加注者先亮牌。
2. 河牌轮无人下注时，从按钮左侧第一位仍在局玩家开始。
3. 之后按顺时针处理其余仍在局玩家。

当所有仍在局玩家已经全押且无人还能行动时，所有相关底牌在继续发公共牌前自动公开。最终赢得任一底池的牌、构成权威获胜判定所需的牌，以及已经自动公开的全押牌不能 muck。其余确定无法赢得任何底池的玩家可以 muck；muck 后底牌不会进入公共投影或普通复盘。普通摊牌和全员弃牌后的主动展示窗口固定为 5 秒；超时后服务端自动公开必须公开的赢家牌，并自动 muck 其余可 muck 牌。

## 14. 现金桌活动规则

### 14.1 买入与补码

首次入座时玩家选择房主配置范围内的 stack。补码只能在两手之间进行，补码后的 stack 不得超过最大买入；因赢取底池自然超过最大买入时不强制减码。stack 为 0 的玩家自动坐出，完成合法补码后从下一手重新获得发牌资格。

补码没有真实货币成本，也不从跨房间钱包扣除。活动历史仍记录每次买入、补码、离桌 stack 和净变化。

### 14.2 新入座与返回

新玩家和坐出返回玩家可选择：

- 等待按钮轮转到其大盲位置后免费进入；或
- 在两手之间补交一个 live 大盲，从下一手立即进入。

玩家主动坐出后不获发底牌，也不支付盲注或前注。断线玩家完成当前手的超时托管后自动坐出下一手；重连后必须明确返回。

### 14.3 连续开手与离桌

一手结算后进入默认 8 秒准备窗口。符合资格、未坐出且 stack 足够支付至少一枚筹码的玩家自动参加下一手，无需逐人点击准备。补码、坐出、返回、入座和房主正常结束在此窗口生效。

房主开赛后只能在两手之间移除现金桌玩家；系统按当前 stack 完成离桌结算并记录原因。玩家自己离桌需要确认，当前手内申请离桌在手牌完成后生效。

### 14.4 发两次

发两次仅适用于现金桌并由房主预先启用。所有仍有资格争夺任一底池的玩家已经全押、无人还能行动且公共牌尚未发完时，系统询问相关玩家；必须全体在限时内同意，超时或任一拒绝都只发一次。

同意后，已存在的公共牌由两个 runout 共享，剩余街分别发两套，所有牌从同一牌堆不放回取出。先完整发第一套剩余公共牌，再发第二套；每个 runout 的每条街仍正常烧牌。投票窗口固定为 10 秒；未在截止前明确同意等同于拒绝。

每个主池和边池先拆为两份，无法均分的一枚筹码归第一套公共牌；两份再分别按第 12 节结算。锦标赛始终只发一次。

## 15. 锦标赛活动规则

### 15.1 报名与开赛

锦标赛至少 2 人才能开赛。默认冻结赛在开赛时关闭报名；启用延迟报名时，新玩家可在配置的截止级别前进入活动，获得配置的完整起始筹码，并在两手之间分配座位。

房间进房许可与赛事报名资格必须同时满足。玩家通过邀请链接完成用户名设置后可以进入房间，但报名关闭、满员或待房主审批时只能观战或等待，不能直接进入子会话。

### 15.2 重购、重新参赛与加购

- 重购：在配置的重购期内，当 stack 不高于配置阈值时，为同一个 `entry_id` 增加固定筹码；次数受上限约束。
- 重新参赛：玩家淘汰后在配置期限内创建新的 `entry_id`，重新获得起始筹码并重新分桌。
- 加购：仅在指定计划休息阶段开放，每个活跃 `entry_id` 最多一次，增加配置的固定筹码。

三项能力默认关闭。所有筹码变化必须写入活动账本，不能直接修改当前手已锁定的 stack。

### 15.3 盲注时钟与休息

盲注时钟属于整个赛事，不属于某张桌。级别到时后，正在进行的手继续使用其创建时冻结的旧级别；下一手使用新级别。计划休息到达后不再创建新手，正在进行的手正常完成；所有桌到达手边界后进入休息。

房主暂停冻结赛事时钟和所有牌桌行动时钟。恢复后按实际暂停时长整体平移截止时间。暂停不能改变当前手牌、底池或级别序号。

### 15.4 自动分桌与平衡

系统初始分桌、调桌、拆桌和决赛桌重抽都必须生成可审计的确定性计划。任意两张可继续牌桌的人数差超过 1 时触发平衡，但玩家只在当前手结束后移动。

来源桌优先选择人数最多的桌；移动候选优先选择最接近下一次大盲且最近没有被移动的玩家。目标桌选择能最小化重复盲注或长期逃避盲注的空位。同一玩家不得连续两次被移动，除非没有其他合法候选。牌桌拆分时一次性生成全部迁移；进入决赛桌时统一重抽座位。

涉及移动的来源桌和目标桌进入 `awaiting_boundary`，直到相关当前手都结算完成。调桌计划一次提交后才允许创建下一手，避免玩家跨桌双占位。

hand-for-hand 启用时，所有仍活动的牌桌共享 `deal_epoch`：每桌在同一 epoch 最多开始一手，并在结算后等待其他桌。所有桌完成该 epoch 后，活动统一处理淘汰、排名、调桌和下一 epoch。默认在“剩余人数比决赛桌容量多 1 人”时启用，开赛前可配置其他排名检查点；运行中不能临时删除正在生效的检查点。

### 15.5 坐出、断线与淘汰

锦标赛玩家坐出或断线后仍持续获发底牌、支付盲注和前注，并按“能过牌则过牌，否则弃牌”托管，不能通过离线规避筹码损耗。stack 归零且所有相关底池结算完成后才正式淘汰。

最后一个仍有筹码的 `entry_id` 为冠军。同一桌同一手淘汰多名玩家时，按该手开始时 stack 较多者排名更高；起始 stack 完全相同则共享同一名次。hand-for-hand 的同一 `deal_epoch` 在不同桌淘汰的玩家共享同一名次。未处于 hand-for-hand 时，不同桌按活动持久化的终局接收序列确定先后。

锦标赛只产生房间内名次和统计，不产生跨房间奖励余额。

## 16. 房主治理、暂停、结束与解散

开赛前房主可直接移除玩家。现金桌开赛后只能在两手之间移除并结算离桌 stack。锦标赛开赛后不提供普通踢出，只提供带二次确认和原因的取消资格：当前手结束后生效，相关 stack 进入 `removed_chips` 审计账本，不再流通，全房可见。

活动维护单调递增的 `activity_fence` 与 `pause_epoch`。所有玩家命令、自动行动、计时器、普通系统事件、viewer grant 和新手创建都必须同时校验当前活动 fence；只校验 Hand GameSession 的 state version 不足以提交。每次暂停和恢复都递增 `pause_epoch`，每次所有权转移、暂停、恢复、进入 `finishing` 或紧急取消都递增 `activity_fence`，旧代际永不复用。

任何递增 `activity_fence` 的事务都必须按稳定顺序锁定并更新全部活跃 child session 的 `current_activity_fence`，撤销旧 grant 后再签发新 grant；不能只推进父 activity。普通所有权切换和进入 `finishing` 不冻结当前手计时，暂停/恢复才同时推进 `pause_epoch` 并重算计时。

房主暂停活动时，平台在一个 PostgreSQL 事务中按稳定 `table_id` 顺序锁定 activity 和全部活跃 child session，校验房主命令幂等键，写入唯一 `pause_effective_at`，递增两个代际，并把活动置为 `pausing`。同一事务为每个未终止 child session 冻结行动时间、时间银行、runout/摊牌窗口和计时器剩余时长，写入新的 fence 后才把活动置为 `paused` 并提交。所有玩法事务必须先锁 activity 再锁 child session，因此并发动作只有两种合法结果：在暂停事务之前完整提交，或因旧 fence 被拒绝；不得出现只更新 hand 未更新 activity 的半暂停状态。

恢复使用相同锁顺序和事务边界，递增 `activity_fence/pause_epoch`，按冻结的剩余时长重建截止时间后进入 `running`。暂停期间可以幂等消费暂停生效前已经持久化的终局结果并更新活动账本，但不得创建下一手、推进赛事时钟或执行调桌；该类终局消费校验结果 envelope 中的终局 fence 与子会话终局快照一致，而不是把旧 outbox 误判成当前玩法命令。恢复后才根据最新活动状态继续边界编排。

结束方式分为：

- 正常结束：活动进入 `finishing`，停止创建新手，当前各桌手牌正常完成并结算，然后活动终止。提前结束的锦标赛保留截至终止时的排名快照，但结束原因为 `host_ended`，不授予正常冠军。
- 紧急解散：立即取消所有未完成子会话，使用每手锁定的起始账本恢复 stack，不生成本手赢家或赛事冠军，记录原因并通知所有客户端返回大厅。

正常结束与紧急解散都复用 activity-first 锁顺序。正常结束把新 fence 写入所有当前 hand 后继续其动作与计时；紧急解散则在同一事务向每个未终止 hand 应用带新 fence 的 `cancelled` 系统终局，恢复锁定账本并禁止任何后续玩法动作。

活动终止后，PartyRoom 进入 `post_game`。房主可以重新开放进房许可、切换游戏并开始新活动；上一活动的复盘与统计继续保留。

## 17. 观战与隐私投影

未入座房间成员是观战者。房主可关闭观战或设置 0、15、30、60 秒延迟；好友房默认 0 秒。锦标赛淘汰玩家自动转为观战者并沿用房间策略。

玩家投影可以包含：

- 自己的两张底牌、当前 stack、当前投入和时间银行。
- 公共牌、底池、公开行动、座位状态和服务端允许动作。
- 活动级盲注、排名、桌位、报名和暂停状态。

观战投影只能包含公共牌、公开行动、底池、公开底牌和活动公共状态。它永远不能包含未公开底牌、烧牌、剩余牌堆、洗牌种子、内部牌型候选或其他桌的私有状态。

延迟观战通过持久化公共事件游标实现，不通过延迟私有快照或在 Redis 缓存权威秘密实现。

## 18. 单手复盘与活动复盘

每个终止的 Hand GameSession 都是独立手牌历史。活动复盘按活动事件顺序组合报名、买入、补码、座位、盲注、调桌、淘汰和手牌索引，并允许进入每一手的详细复盘。

复盘可见性固定为：

- 玩家始终可以回看自己当时收到的底牌。
- 自动全押公开、正常亮牌和主动展示的牌可向有权访问复盘的房间成员公开。
- muck、全员弃牌后未主动展示、未发牌堆和烧牌保持隐藏。
- 管理员完整审计使用独立权限、字段解密和审计日志，不等同于普通复盘。
- 紧急取消手只显示取消前已经公开的事实，不公开权威私有牌。

活动复盘必须能从持久化活动事件、子会话索引和各手事件唯一重建；不得依赖当前用户名、当前座位、客户端缓存或运行中内存对象。

活动是德州复盘权限的所有者。活动创建时冻结复盘策略，成员进入、离开、淘汰、取消资格和房间解散只通过版本化 activity ACL 事件改变后续访问资格。每个 child session 保存 `activity_id/table_id/hand_no` 与参与者私牌 entitlement，但不得在单手结束时复制当时 PartyRoom 成员列表作为独立授权真相。读取单手复盘时必须先通过 activity ACL，再按该手冻结的 `user_id + entry_id + hand_participant_id` 和公开牌掩码生成 viewer-safe 投影；当前房间成员身份本身既不授予历史私牌，也不能抹除已经合法取得的本人私牌复盘权。

## 19. 活动与单手权威状态

### 19.1 GameActivity 快照

活动权威快照至少包含：

- `activity_id`、`room_id`、游戏版本、规则版本、模式和配置摘要。
- 状态：`starting/running/pausing/paused/finishing/finished/cancelled`。
- 活动所有权 epoch、单调递增的 `activity_fence/pause_epoch`、可空 `pause_effective_at`、活动修订号和最近更新时间。
- 现金桌玩家或锦标赛 entries、当前 stack、锁定 stack、坐出/淘汰/取消资格状态。
- 长期 tables、座位、按钮、当前/上一子会话、等待队列和桌状态。
- 锦标赛级别、级别截止时间、休息、报名窗口和排名。
- 已消费手牌结果键、筹码审计账本、待调桌计划和活动终局摘要。

### 19.2 Hand GameSession 快照

单手权威快照至少包含：

- `activity_id/table_id/hand_no/previous_session_id` 血缘。
- 冻结参与者、牌桌 seat、起始 stack、授权代际和起始账本摘要。
- 不可变 `participant_set_digest`：对按 `seat_index, hand_participant_id` 稳定排序的全部冻结参与者身份、起始 stack、时间银行和强制注快照做规范 protobuf 编码后计算 SHA-256；它是整手摘要，不属于任一参与者行。
- 当前 `activity_fence/pause_epoch`；父活动推进代际时必须通过跨聚合事务同步更新，不能修改冻结参与者身份。
- 按钮、小盲、大盲、前注、Straddle 和实际规则级别。
- 加密洗牌种子、完整牌堆、牌堆游标、底牌、烧牌和公共牌。
- 当前阶段、当前行动者、轮次投入、总投入、当前最高注、最后完整加注增量。
- 玩家 fold/all-in/acted 状态、行动截止时间和剩余时间银行。
- 主池/边池、runout 决策、摊牌状态和不可变终局结果。

客户端不得上传或覆盖任何权威牌、牌堆、按钮、盲注、底池、牌型或赢家字段。

### 19.3 冻结参与者与行动授权

Hand GameSession 创建时必须持久化不可变 `FrozenParticipant` 列表，每项至少包含：

- `hand_participant_id`：该 `session_id` 内唯一且永不复用的主体 ID，命令、计时器、投票、摊牌选择、支付和审计优先引用它，不能只引用 `user_id`。
- `user_id`、`entry_id`、`activity_id`、`table_id` 和 `seat_index`；`entry_id` 在现金桌与锦标赛都必填，后三项必须与 session 血缘和活动当前座位锁一致。
- `starting_stack`、`time_bank_ms_at_freeze`、强制注快照和可选 `participant_row_digest`；行摘要只用于行级损坏诊断，不参与终局幂等键或父层消费校验。
- `viewer_grant_generation_at_freeze`，用于证明该参与者在开手时取得的 table seat 与私有投影代际；当前 activity fence 与 pause epoch 属于可推进的 hand 运行快照，不属于此不可变身份项。

玩家 hand 命令授权必须同时满足：已认证 `user_id` 与冻结项一致；活动中的 `entry_id` 仍是该用户的当前有效记录；当前 table seat、session 和 hand participant 均与 signed grant 一致；grant generation、activity fence 和 pause epoch 未过期。PartyRoom 的 participant role 只证明基础房间访问，不代表德州牌桌座位，PartyRoom 成员排列索引也不得参与 hand 授权。房主可以不在任何德州桌入座；其治理权限不能伪造成玩家行动权限。

换桌、离座后重入、淘汰后转观战、取消资格、房间撤权、下一手切换或 player/spectator 身份变化都必须递增对应 viewer grant generation。旧 grant 必须同时停止接收私有投影和提交命令。当前手已冻结的历史身份仍保留用于结算与复盘，但不得命中新 `entry_id` 或新座位。

## 20. 父子一致性与事务边界

创建一手时，活动在同一事务中：

1. 校验活动、桌和座位修订号。
2. 锁定该手所有起始 stack，并生成起始账本摘要。
3. 更新 table 的 `active_session_id` 和 `hand_no`。
4. 创建冻结参与者的 Hand GameSession、创建批次、计时器和 outbox。

单手终局先原子持久化终局快照、事件批次、唯一不可变的 `terminal_result` 和 `hand.finished` outbox。终局 envelope 至少包含 `terminal_result_id/activity_id/table_id/hand_no/session_id/terminal_state/terminal_state_version/activity_fence/pause_epoch/participant_set_digest/result_digest`。`terminal_result_id` 在终局状态转移时一次分配并随 outbox 重放保持不变；同一 session 不允许同时产生 `settled` 与 `cancelled` 两个合法终局。

活动消费表以 `session_id + terminal_state_version` 为稳定唯一业务键，并对 `session_id` 与 `terminal_result_id` 分别另设唯一约束。`result_digest` 只做规范编码后的完整性校验，不能进入允许产生第二条消费记录的去重键。相同稳定键、terminal result ID、终态和 digest 全部一致时返回原 `consumption_receipt_id`，不重复入账；任一稳定身份被不同 digest、不同终态或不同 lineage 复用时必须 fail closed，将活动置为可恢复的完整性暂停并报警，不得二次消费或自动选择一个结果。

首次消费终局结果时必须验证：

- 子会话确实属于当前 activity/table/hand。
- 起始 stack 摘要与活动锁定账本一致。
- 终局 fence、pause epoch 和 hand-level participant set digest 与子会话持久化终局快照一致。
- 下面定义的支付前后筹码等式全部成立。
- 每位参与者只出现一次，所有底池金额和支付金额一致。

设 `S0 = sum(starting_stack_i)`，`R_i` 为支付前玩家尚未投入的剩余 stack，`C_i` 为本手总投入，`P_j` 为规范化后的各主池/边池，`U` 为尚待退还的无人匹配超额，`W_i` 为最终 stack，则每个终局必须满足：

```text
S0 = sum(R_i) + sum(C_i)
sum(C_i) = sum(P_j) + U
S0 = sum(R_i) + sum(P_j) + U
sum(P_j) = sum(payout_i)
S0 = sum(W_i)                    // 应用 U 退还和全部 payout 后
```

活动账本另满足 `issued_chips = live_chips + retired_chips + removed_chips`。买入、补码、重购、重新参赛和加购只增加 `issued_chips` 与对应 live stack；现金桌离场或活动结束把 live 移到 `retired_chips`；取消资格只把 live 移到 `removed_chips`。锁定进 child hand 的筹码只是在 live 内改变归属，不能重复计入两侧。所有等式都使用检查过的整数加减法，在写入前验证。

活动消费结果时更新 stack、时间银行、淘汰和 table 状态，并清空 Table 的 `active_session_id`、更新 `last_finished_session_id`。无需调桌且活动仍为 `running` 时，应用结算与创建下一手可以在同一数据库事务完成；活动处于 `pausing/paused/finishing/cancelled` 时只允许结算入账，不得创建下一手。需要跨桌协调时，相关桌停在 `settling/awaiting_boundary`，由带 activity fence 的确定性调桌计划推进。

子会话完成事件属于 Activity 内部事实，不能调用 Room `FinishSession`、不能更新 Room `last_finished_session_id`、不能把 PartyRoom 推入 `post_game`。只有顶层 GameActivity 进入 `finished/cancelled` 且全部子会话和账本完成收敛后，Activity 协调器才能原子调用 Room `FinishActivity`。

任何崩溃点都必须通过未消费 outbox、持久化 inbox 和所有权 epoch 恢复。不得通过“如果页面看起来结束了就补写账本”之类推测性修复。

## 21. 协议、命令、事件与错误

协议继续使用 Protocol Buffers 与 Buf 生成 Go/TypeScript 代码。活动层和单手层使用不同服务面。

活动命令至少包括：

- `activity.register`
- `activity.buy_in`
- `activity.top_up`
- `activity.sit_out`
- `activity.return`
- `activity.leave`
- `activity.rebuy`
- `activity.reenter`
- `activity.add_on`
- `activity.pause`
- `activity.resume`
- `activity.finish`
- `activity.disqualify`
- `activity.cancel`

单手玩家命令至少包括：

- `hand.fold`
- `hand.check`
- `hand.call`
- `hand.bet`
- `hand.raise`
- `hand.all_in`
- `hand.set_auto_action`
- `hand.runout_vote`
- `hand.show_cards`
- `hand.muck`

活动事件至少包括报名、买入、补码、入座、坐出、返回、盲注升级、休息、手牌创建、手牌结算、调桌、淘汰、取消资格、暂停、恢复和活动终止。单手事件至少包括强制注、发底牌、行动、街推进、公共牌、超额退还、底池构造、全押公开、runout 选择、亮牌、muck、底池支付和手牌终止。

### 21.1 通用命令 envelope

所有活动与 hand 命令必须携带 `operation_id + request_digest`。activity 回执键为 `(activity_id, actor_user_id, operation_id)`，hand 回执键为 `(session_id, actor_user_id, operation_id)`。同一回执键与相同 digest 的重试返回原持久化回执；同一回执键出现不同 digest 返回 `IDEMPOTENCY_CONFLICT`，不能重新执行。

`request_digest` 对完整命令语义计算，而不是只对 action payload 计算：

```text
request_digest = SHA-256(
  domain_separator
  || canonical_protobuf(
       service_method_or_command_verb,
       runtime_lineage,
       expected revisions/fences/generations,
       command-specific payload
     )
)
```

规范输入排除 `operation_id`、`request_digest`、认证 token、grant 签名、HTTP/Connect headers 和传输时间戳，保留 envelope 中所有影响命令含义或并发门禁的字段。编码前拒绝未知字段并使用固定 schema version 与 deterministic protobuf serialization，不能依赖 JSON 字段顺序。即使 payload 都为空，`hand.fold`、`hand.check`、`hand.call`、`hand.all_in` 以及不同 activity verb 也必须因为 command verb 不同而产生不同 digest；同一 operation ID 被复用于其中任何两个命令时必须冲突。

活动命令 envelope 至少包含 `activity_id/expected_activity_revision/expected_activity_fence/expected_pause_epoch/operation_id/request_digest`。hand 玩家命令 envelope 至少包含：

```text
activity_id
table_id
session_id
expected_state_version
expected_turn_ordinal
expected_activity_fence
expected_pause_epoch
viewer_grant_generation
operation_id
request_digest
action payload
```

协议不定义也不接受含义重复的 `hand_id`。`session_id` 是 hand 命令的唯一目标；`table_id + hand_no` 只作为服务端校验的血缘与审计字段。`expected_turn_ordinal` 是当前 session 内从 1 开始单调递增、永不复用的行动机会序号：当前行动者改变，或同一行动者的合法动作集合因权威状态变化而改变时都必须递增。下注阶段命令必须精确匹配它；runout 与摊牌阶段命令使用各自 decision generation，并将 projected `expected_turn_ordinal` 原样回传。

认证主体、`entry_id` 和 `hand_participant_id` 从 signed grant 与服务端活动状态解析，不能采用客户端自报值。服务端依次校验 runtime 血缘、grant、activity fence、pause epoch、session state version、turn/decision generation 和动作合法性；任何一步失败都不得部分扣筹码或更新计时器。

### 21.2 动作 payload 与金额语义

- `hand.fold`、`hand.check` 和 `hand.call` 使用空 payload；跟注金额由服务端从权威状态计算。
- `hand.bet` 与 `hand.raise` 只接受 `target_total`，表示动作完成后玩家在当前 betting street 的总投入，不是增量、整手累计投入或最终 stack。服务端计算 `cost = target_total - current_street_committed`，并验证 projected `min_raise_to/max_raise_to`。
- `hand.all_in` 使用空 payload，不接受客户端金额。服务端精确投入玩家剩余 stack，并根据当前下注状态判定为 call、bet、完整 raise 或不足额 raise。
- `hand.set_auto_action` 携带 `auto_action_generation` 与 `clear/auto_check/check_or_fold/call_exact` 之一。只有 `call_exact` 携带非负 `required_call_cost`，表示设置时权威状态计算出的额外跟注筹码；轮到玩家时仅当重新计算的 `to_call` 与其完全相等才执行，否则清除。协议不提供 `call_any`、`call_up_to`、自动下注、自动加注或自动全押。
- `hand.runout_vote` 携带 `decision_id + decision_generation + accept_twice`。只允许仍有对应底池权益的冻结参与者投票；超时等同 `accept_twice=false`。
- `hand.show_cards` 与 `hand.muck` 携带 `showdown_id + showdown_generation`。仅在全员弃牌后的主动展示窗口，`show_cards` 额外接受 2-bit `reveal_mask`：`01` 公开第一张、`10` 公开第二张、`11` 公开两张，`00` 非法；正常摊牌中要保留争池资格必须公开两张，服务端忽略任何试图少亮的掩码。强制 all-in 公开不等待此命令。过期 decision、已完成选择或无权选择都 fail closed。

所有筹码字段都是不可分割的整筹码，wire/storage 使用有符号 `int64` 的非负子域，禁止小数和负数哨兵。全平台固定 `MAX_CHIPS = 9_007_199_254_740_991` (`2^53-1`)；单个 stack、强制注、投入、底池、支付、买入、补码、`removed_chips` 和 `target_total` 均在 `[0, MAX_CHIPS]`，活动总发行量也不得超过该上限。Go 使用 checked int64 算术，PostgreSQL 使用 `BIGINT + CHECK`，TypeScript 权威层使用 `bigint` 或规范十进制字符串，禁止经普通 `number` 往返后再生成 digest。任何加、减、乘、求和越界在状态变更前返回 `CHIP_OVERFLOW`。

### 21.3 稳定错误码

稳定错误码至少区分：

- `STALE_ACTIVITY`
- `STALE_ACTIVITY_FENCE`
- `STALE_TURN`
- `STALE_DECISION`
- `STALE_GRANT`
- `ILLEGAL_ACTION`
- `AMOUNT_OUT_OF_RANGE`
- `CHIP_OVERFLOW`
- `IDEMPOTENCY_CONFLICT`
- `TERMINAL_RESULT_CONFLICT`
- `ACTIVITY_PAUSED`
- `REGISTRATION_CLOSED`
- `TABLE_CHANGED`
- `NOT_SEATED`
- `AUTHORIZATION_REVOKED`
- `SESSION_TERMINAL`

客户端遇到过期状态时刷新权威投影；不得自动重放下注、加注或全押。只有同一 `operation_id + request_digest` 的传输重试可以读取原持久化回执。

## 22. 实时订阅与重连

德州页面稳定路由为：

```text
/room/:roomId/activity/:activityId/table/:tableId
```

客户端同时订阅活动公共流和当前手 viewer-scoped 流。Activity grant 至少绑定 `room_id/activity_id/user_id/viewer_kind/authorization_generation/activity_fence/pause_epoch/expires_at`；Hand grant 在此基础上绑定 `table_id/session_id`，玩家 grant 还必须绑定 `entry_id/hand_participant_id/seat_index/viewer_grant_generation`。grant 由服务端签发且不可由客户端拼接，目标或代际任一不匹配都 fail closed。

`authorization_generation` 是按 `(activity_id, user_id)` 持久化的活动级单调授权代际，`viewer_kind` 是该代际下 grant 的不可变绑定。房间访问撤销、成员移除或封禁、观战开关/延迟/ACL 策略变化、player 与 spectator 身份切换、取消资格、活动终止或房间关闭，都必须在同一权威事务中先递增受影响主体的 generation，再写撤销 outbox。旧 Activity grant 随即停止实时订阅、重连和历史补拉，Realtime 主动断开已连接订阅且每次 refresh 都重新校验 generation，不能只等待 `expires_at`。`viewer_grant_generation` 是 table/hand 私有授权的更细粒度代际；玩家 hand grant 必须同时匹配活动级和 hand 级两个 generation。

活动流发布每张 table 当前 `session_id`、hand lineage 与授权代际。下一手创建后，服务端先持久化新 hand 与新 generation，再撤销旧 hand grant；客户端保留同一路由和牌桌组件，清除旧私牌后使用新 grant 切换子会话订阅。两手之间的 table-scoped 观战 grant 可以保持，但任何 session-scoped 私有 grant 不得跨手复用。

重连顺序固定为：

1. 获取 PartyRoom 与活跃运行引用。
2. 获取活动 viewer-safe 快照。
3. 解析自己的当前桌位或观战目标桌。
4. 获取绑定当前 activity/table/session 和最新 generation/fence 的订阅授权与完整投影。
5. 从活动游标和单手游标分别续订增量。

两手之间没有 active child session 时显示 table 的结算、补码、调桌或下一手倒计时，不返回房间页。换桌、取消资格、活动终止或房间解散会撤销旧 grant、清理本地私牌并导航到新的权威位置。

## 23. 移动端牌桌与操作交互

竖屏默认由顶部紧凑状态栏、中央共同牌桌和底部一体化操作轨组成。共同牌桌始终是视觉主体，公共牌、底池、当前下注和按钮固定在桌心安全区。

玩家使用按 `2-9` 人数预设的桌边锚点。座位组件是自适应玩家牌，包含头像、用户名、stack、当前投入和状态，不使用固定尺寸的大圆圈。最长用户名必须截断或换行，不能推动牌桌重排。

操作轨规则：

- 收起高度约 64-72px，保留拖动柄和当前最关键操作。
- 玩家行动时进入标准高度，稳定展示弃牌、过牌/跟注、下注/加注。
- 向上滑动展开金额预设、滑杆、精确输入、时间银行和自动行动。
- 展开后的操作轨占用高度不得超过 `floor(visualViewport.height * 0.34)`；键盘弹出时使用更新后的 visual viewport 重新计算。自动化验收允许最多 1 CSS px 的取整误差，并要求通过重新分配桌面空间保证操作轨与底牌、公共牌的相交面积为 0。
- 弃牌与全押保持空间隔离；按钮尺寸不因金额或文案变化而跳动。
- 金额预设至少包含常用底池比例、底池、最小加注和全押，但最终值由服务端合法范围裁剪。

横屏时共同牌桌占主要宽度，操作轨移动到右侧或底部窄栏。浏览器允许时可请求方向锁定；不允许时只提示用户旋转并自动响应方向变化。

排名、手牌历史、规则、观战桌切换和设置放入抽屉，不长期挤占牌桌。发牌、筹码入池、底池支付和摊牌使用有业务意义的动画；支持减少动态、声音、振动和高对比度设置。

## 24. 主题系统

德州主题只改变表现，不改变规则、金额、牌 ID、可见性或操作权限。主题至少提供以下语义 token：

- 桌布与桌边。
- 公共牌与底牌正面、卡背。
- 筹码面值、底池和投入强调。
- 当前行动、获胜、警告、危险和禁用状态。
- 操作轨、抽屉、遮罩、阴影和动效时长。

房主可设置房间默认主题；玩家可保存个人主题覆盖。主题缺失、版本不兼容或资源加载失败时回退到内置默认主题。私牌不能因为主题资源加载失败而以原始协议文本暴露。

## 25. 仓库结构与依赖方向

新增目录建议：

```text
contracts/platform/activity/v1/
platform/game-activity/
sdk/go/game/activity_contract.go
sdk/ts/game-client/activity.ts

games/texas-holdem/
  proto/
  gen/
  engine/
    cards/
    ranking/
    betting/
    pots/
    hand/
  activity/
    cash/
    tournament/
    tables/
    settlement/
  module/
    hand.go
    activity.go
  projection/
    hand.go
    activity.go
    replay.go
  client/
  themes/
```

依赖规则固定为：

- `platform/game-activity` 只依赖平台基础设施和 Game SDK，不导入德州包。
- 德州纯引擎不导入 Room、API、数据库、Redis、Vue 或生成的传输服务。
- 德州 module 只负责 Proto/SDK 与纯引擎之间的严格适配。
- API、Realtime 和 Worker 只做鉴权、路由、持久化协调与投影分发，不包含扑克规则。
- 通用牌桌、操作轨与主题 token 留在 `packages/game-ui-kit` 和 `packages/theme-system`。
- 扑克牌、筹码、下注控件和赛事榜单留在德州目录；只有出现第二个明确消费者后才提升为公共组件。

前端 SDK 增加 `GameClientAdapter`，每款游戏导出 catalog、规则编辑器、运行页面、复盘页面、主题和 `runtimeKind`。工具链生成懒加载 registry，首页、房间、运行页和复盘页不得继续增加分散的 `gameId` 条件分支。

## 26. 持久化与迁移

逻辑持久化至少需要：

- 活动主记录、权威快照、状态版本、所有权 epoch 和终态。
- 活动事件批次、动作回执、系统 inbox/outbox 和持久化计时器。
- 活动成员/entries、版本化 activity replay ACL、hand 私牌 entitlement 和审计账本。
- 长期 tables、座位、当前/上一 child session 和调桌 fencing 状态。
- 活动到 Hand GameSession 的血缘索引。
- `game_sessions` 的可选 `activity_id/table_id/hand_no/previous_session_id` 字段。
- 不可变 terminal result、按稳定业务键唯一的消费 inbox、result digest 和消费回执。

数据库不使用单个无外键的多态 `active_runtime_id`。`party_rooms` 保留现有可空 `active_session_id/active_game_id`，新增可空 `active_activity_id` 与 `active_runtime_kind`；完成历史保留 `last_finished_session_id/last_finished_game_id`，并新增 `last_finished_activity_id/last_finished_runtime_kind`。约束必须保证：

- `active_runtime_kind=session` 时 `active_session_id + active_game_id` 非空且 `active_activity_id` 为空；`active_runtime_kind=activity` 时 `active_activity_id + active_game_id` 非空且 `active_session_id` 为空；无活跃 runtime 时 kind、两个目标 ID 与 `active_game_id` 四列全部为空。
- `playing` 房间恰有一个合法活跃 runtime，`post_game/closed` 房间没有活跃 runtime。`active_game_id` 必须等于目标记录的 `game_id`，不能成为第三份独立真相。
- `last_finished_runtime_kind=session|activity` 时，匹配类型的 finished ID 与 `last_finished_game_id` 必须同时非空并指向同房间、同游戏的终态对象，另一类型 ID 必须为空；Room API 直接用这组已校验列物化完整 `last_finished_runtime_ref`。
- session 与 activity 表分别提供 `(id, room_id)` 唯一键，Room 使用复合外键保证目标属于本房间；Table 到 child session 也使用 `(session_id, activity_id, table_id)` 血缘外键。
- 一个 table 最多一个活跃 child session，一个 entry 最多一个活跃座位和一个 hand lock，一个 `hand_participant_id` 只属于一个 session。
- terminal result/consumption 同时唯一约束 `terminal_result_id`、`session_id`、`(session_id, terminal_state_version)` 和 `(activity_id, table_id, hand_no)`，digest 冲突不能通过插入新行绕过。

迁移必须按下面顺序执行，不能在单次部署里先写 activity 再补兼容：

1. 先创建 activity/table/entry/ACL/terminal consumption 表、game session 血缘列，以及 Room 的可空 activity/runtime-kind 列。新增约束先以不可阻断旧流量的形式部署，旧 session 行为不变。
2. 对 `active_session_id IS NOT NULL` 的房间回填 `active_runtime_kind=session`。对历史 `last_finished_session_id` 回填 `last_finished_runtime_kind=session`，并验证现有 `last_finished_game_id` 等于目标 session 的 `game_id`；字段缺失时只允许通过目标 session 确定性回填，禁止采用当前选中游戏或客户端值。逐行验证 runtime 的 `room_id/game_id/status`，任何不一致先隔离修复，绝不推断成 activity。
3. 部署双读双写 Room/API。新字段存在时 `ActiveRuntimeRef` 是权威；仅在 kind 尚未回填且 legacy session ID 存在时允许 session fallback。新字段与 legacy 字段不一致时返回完整性错误。session 型游戏同时写新 kind 与旧 session 列；activity 型游戏只写 activity 列，legacy `active_session_id` 必须为空。
4. 部署 ActivityService、Game/Realtime 血缘 grant、activity-aware replay、Web `activeRuntimeRef` store、稳定 activity/table 路由和双流 live composable。服务端在所有生产消费者均识别 activity 前不得启用德州创建入口。
5. 对全量数据运行一致性查询，验证并启用 Room 一选一、复合外键、terminal consumption 和 entry/seat/hand-lock 唯一约束，再把新 runtime kind 纳入 domain 必填不变量。
6. 指标确认不再发生 legacy fallback 后才能在后续 breaking 版本删除旧读取路径。Proto 旧字段先标记 deprecated；真正删除时保留 field number。历史 session replay 与 ACL 永久按旧 session 身份读取，不批量伪造成 activity。

启用 activity 写入之前可以回滚到旧服务。已经存在 activity 型房间之后，旧版本必须拒绝启动而不是把 activity ID 填入 `active_session_id`；回滚方案必须先正常终止/迁出 activity 或部署兼容读取补丁。任何回填、降级或重试都不得改变已有 runtime ID、hand lineage 或 replay entitlement。

## 27. 故障恢复与安全

PostgreSQL 是唯一权威源。Redis 只保存无秘密通知、租约和短期协调数据。PostgreSQL 事务失败时不发布成功；Redis 通知丢失时客户端和消费者通过持久化版本游标补拉。

活动和子会话分别使用所有权 epoch；activity fence 是业务提交代际，不能用进程内 lease 到期时间替代。所有 hand action、timer、auto-action、普通 system command 和下一手创建都在同一事务中读取并匹配当前 activity fence/pause epoch。失去所有权或代际过期的进程不能提交动作、计时器或结算。终局 outbox 只有在其 source fence 与持久化 terminal snapshot 一致时可按第 20 节规则跨暂停代际消费。模块版本缺失、密钥不可用、权威状态无法解码或账本校验失败时活动必须 fail closed 并进入可恢复暂停，不得自动猜测赢家或重发牌。

随机种子、完整牌堆和未公开底牌使用字段级加密。日志、trace、错误文本、指标标签、outbox 和 Redis payload 必须经过秘密扫描。管理员读取完整审计需要独立授权、短期 capability 和不可删除的审计记录。

## 28. 可执行示例

### 28.1 最小加注

盲注 `10/20`。A 加注到 60，完整增量为 40；B 若要完整加注，至少加到 100。B 只有 85 时可以全押到 85，但这次 25 的增量不是完整加注。

### 28.2 累计不足额全押

A 已跟到 100，最近完整加注增量为 50。B 全押到 120，C 全押到 150。A 再次面对的累计增加为 50，因此 A 的加注权重新开放；如果 C 只到 140，累计增加 40，A 只能跟注或弃牌。

### 28.3 多重边池

A 总投入 30，B 投入 100，C 与 D 各投入 250。主池为 `30*4=120`，A/B/C/D 有资格；边池一为 `(100-30)*3=210`，B/C/D 有资格；边池二为 `(250-100)*2=300`，C/D 有资格。每个底池独立比较并支付。

### 28.4 无人跟注退还

A 河牌下注 200，B 只有 80 并全押跟注，其他人已弃牌。A 超过 B 可匹配额度的 120 先退还；只有双方各 80 的部分进入该层底池。

### 28.5 两人局

A 是按钮并支付小盲，B 支付大盲。翻牌前 A 先行动；翻牌后 B 先行动。下一手按钮交换。

### 28.6 发两次

翻牌后 A、B 全押且一致同意发两次。现有翻牌由两套公共牌共享；服务端依次为第一套发转牌/河牌，再为第二套发转牌/河牌，每条街各烧一张。每个底池先拆成两份，再分别结算。

### 28.7 紧急解散

一手开始时 A/B/C stack 分别为 100、100、100。进行中已投入 20、60、60 时房主紧急解散。该手取消，活动恢复三人的锁定起始 stack，不生成底池赢家；已发生公开动作仅作为取消审计保留。

## 29. 测试要求与不变量

### 29.1 单元测试

必须覆盖：

- 52 张牌规范 ID、零种子固定向量、拒绝采样、发牌顺序和烧牌。
- 九类牌型、七取五、所有踢脚牌、A2345、完全并列和花色不破同分。
- 2-9 人按钮/盲注、两人局、短筹盲注、前注和 Straddle。
- 所有合法动作、最小下注、最小加注、不足额全押、累计重开和超额退还。
- 一层与多层边池、弃牌贡献、奇数筹码、全押公开、muck 和发两次。
- 现金桌买入、补码、坐出、返回、断线和连续开手。
- 锦标赛报名、盲注、休息、重购、重新参赛、加购、调桌、淘汰、并列和冠军。
- `target_total` 的 street-total 语义、空 payload all-in、turn/decision generation、operation digest 冲突和全部稳定错误码；同一 operation 下的空 payload `fold/check/call/all_in` 必须生成不同 digest 并互相冲突。
- 自动行动只允许 `auto_check/check_or_fold/call_exact`；`call_exact` 金额不再精确匹配时清除且不投入筹码，协议不存在 `call_any/call_up_to`。
- FrozenParticipant 的完整 identity/lineage；同一用户离场重入或重新参赛后，旧 grant、旧 entry 和旧 hand participant 都不能命中新座位。
- `participant_set_digest` 的稳定排序与规范编码；行顺序变化不改变摘要，身份或起始账本任一变化必须改变摘要。

### 29.2 属性与模糊测试

必须证明：

- 一手内真实牌不重复，任何投影都不能制造不存在的牌。
- 除明确的取消资格 `removed_chips` 外，活动筹码账本守恒。
- 每个底池金额等于其贡献层之和，支付总额等于底池总额。
- 已弃牌玩家永远不能成为任何底池赢家。
- 玩家 stack、投入、底池和时间银行永远不为负数。
- 所有筹码值与活动总发行量不超过 `MAX_CHIPS`；边界值、求和、乘法和 side-pot 构造不溢出，TypeScript 往返不丢精度。
- 任意合法状态最多有一个当前行动者，每个 `entry_id` 最多参与一手。
- 同一快照、命令、时间和随机输入产生字节一致的下一状态、事件和计时器。
- 相同终局 outbox 重放任意次数，活动账本只变化一次。
- 未公开底牌、牌堆和种子不出现在其他玩家、观众、普通复盘或基础设施消息中。

### 29.3 集成与端到端测试

必须覆盖：

- PostgreSQL 事务回滚、outbox/inbox 重放、所有权切换、进程中断恢复和加密密钥失败。
- Room runtime 双轨迁移、legacy session 回填、双读双写、一选一约束、activity 启用门禁、升级前回滚与启用后的拒绝降级。
- 房主不在目标桌时由 activity 系统主体正常创建 child hand；child hand 完成只更新 Table/Activity，Room 保持 `playing`，只有 Activity 终态才进入 `post_game`。
- pause 与玩家动作、timer、runout/showdown decision、终局消费同时竞争；验证旧 fence 永不生效、终局可在暂停中幂等入账且不会创建下一手。
- 同一 terminal identity 同 digest 重放返回原回执；不同 digest、不同终态或第二个 terminal result ID 必须停止活动且账本不变。
- 换桌、换手、淘汰、取消资格和离场重入都会撤销旧私有 grant；活动 ACL 正确派生 child replay，PartyRoom 当前成员身份不能越权读取私牌。
- 房间撤权、观战策略或 viewer kind 变化会立即递增活动级 `authorization_generation`，已连接公共流和补拉请求都拒绝旧 grant，不能等待 TTL。
- 真实双浏览器现金桌：创建、邀请链接、设置用户名、入座、完整一手、补码、下一手、重连和解散通知。
- 至少 4 人预置牌面的全押边池与发两次流程。
- 至少两桌赛事：延迟报名、盲注升级、同时进行、淘汰、调桌、合桌、决赛桌和冠军。
- 观战延迟、换桌授权撤销、取消资格、正常结束和紧急退款。
- 现有四款游戏的房间、会话、复盘和 registry 全量回归。

### 29.4 视觉与负载验收

Playwright 视觉矩阵至少包含 `360x800`、`390x844`、`430x932` 及对应横屏，覆盖 2、6、9 人桌、键盘弹出、操作轨展开、最长用户名和大额筹码。关键玩家牌、公共牌、底牌、抽屉和按钮的边界框相交面积必须为 0；允许的阴影/动画溢出需从测量盒中排除。展开操作轨不得超过第 23 节的 34% 上限与 1 CSS px 取整误差，所有按钮文本必须完全落在自身边界框内。

负载验收使用 90 名参赛者、10 张并行牌桌，稳定阶段持续 15 分钟并以每桌每秒 1 条合法或预期拒绝命令驱动；随后在 10 秒内让 90 个客户端完成活动/单手双层重连。测试环境、服务实例数、数据库规格和注入网络 RTT 必须随报告保存，基准 RTT 不得超过 50ms。以服务端持久化时间戳计算，门禁为：命令提交 p95 <= 300ms、p99 <= 750ms；非延迟实时投影 p95 <= 500ms、p99 <= 1s；换手订阅 p95 <= 1s、p99 <= 2s；计时器触发绝对漂移 p99 <= 500ms；终局结果消费 p95 <= 500ms、p99 <= 1s。全程允许的非预期 5xx/连接失败率低于 0.1%，筹码不一致、重复结算、跨桌双占位、私牌泄露和持久化事件丢失必须为 0。

## 30. 完成门禁

交付前必须通过：

- Buf lint、breaking check 和生成物一致性检查。
- Go 全量测试、德州/活动运行时竞态检查、属性测试和 fuzz 种子回归。
- TypeScript 类型检查、Web 单测、生产构建和 Playwright 全量回归。
- PostgreSQL/Redis 集成测试、迁移前进/回滚验证和故障注入。
- ActiveRuntimeRef、授权 grant、暂停 fence、terminal result envelope 和筹码 int64/TypeScript bigint 的跨语言契约测试。
- 私牌/种子/牌堆泄露审计。
- 桌面与移动端真人双设备流程。

任何已知筹码不一致、重复结算、跨桌双占位、私牌泄露、无法恢复的 `settling` 状态或错误冠军都属于发布阻断。

## 31. 明确拒绝的方案

- 拒绝把多张牌桌放入一个全局状态版本的 GameSession；它会造成无关牌桌版本冲突和单点故障半径。
- 拒绝让长生命周期 Table GameSession 动态修改冻结参与者；座位变化必须进入下一手子会话。
- 拒绝每手结束都把 PartyRoom 推回 `post_game`；Room 生命周期跟随 GameActivity。
- 拒绝客户端计算权威合法操作、边池、牌型或赢家。
- 拒绝为了复盘公开 muck、未展示底牌、烧牌、剩余牌堆或随机种子。
- 拒绝真实货币、抽水、充值、提现、转账和跨房间筹码钱包。
- 拒绝把尚无第二消费者的扑克组件提前抽成通用扑克框架。

## 32. 决策摘要

- 完整支持现金桌、单桌锦标赛和多桌锦标赛，默认入口为现金桌。
- 采用 `GameActivity -> Table -> Hand GameSession`，每手冻结参与者。
- 现金桌默认 6 人、盲注 `1/2`、买入 40-100 BB；锦标赛默认 9 人快速冻结赛。
- 无限注、标准九类牌型、标准全押/边池/奇数筹码与服务端权威合法操作。
- 超时可过则过，否则弃牌；断线不暂停；房主可暂停整场活动。
- 现金桌可选发两次，必须相关玩家一致同意；锦标赛只发一次。
- 筹码只在当前活动内记分，不对应真实货币。
- 竖屏共同牌桌优先，操作轨可滑动展开且不遮挡游戏，主题完全 token 化。
- PostgreSQL 为唯一权威源，所有父子结算幂等、可恢复、可审计。

## 33. 标准规则参考

本规范的无限注重开、按钮局奇数筹码、两人盲注、全押亮牌和锦标赛程序优先参考以下公开规则；现金桌买盲与发两次属于本平台明确冻结的房规：

- Poker TDA 2024 Rules，Rule 43、Rule 47 与 RP-11：<https://www.pokertda.com/view-poker-tda-rules/>
- WSOP 2025 Tournament Rules，Rule 70-73、87、126(b)：<https://www.wsop.com/2025/2025-WSOP-Tournament-Rules.pdf>
- PokerStars Live Cash Game Rules，Rule 39-40、45：<https://www.pokerstarslive.net/poker/cashgamerules/>

外部规则只用于解释本规范未歧义化的标准背景。发布后的权威执行仍以本规范的版本化规则和服务端代码为准，不能在运行中远程跟随外部网页变化。

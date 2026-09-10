<p align="center">
  <img src="assets/logo.webp" alt="Goldilocks logo" width="192">
</p>

<p align="center">
  <a href="README.md">English</a> · 简体中文 · <a href="README.zh-hant.md">正體中文</a>
</p>

# Goldilocks

**无需改变工作流，只选择恰到好处的模型。**

Goldilocks 是一个提供模型路由和 PR 监听的轻量级 Codex 插件。只有当现有工作流已经决定创建子代理时，模型路由 skill 才会介入，帮助选择合适的模型和思考强度（reasoning effort）。

`model-routing` skill 不决定是否创建子代理，也不改变任务或工作流，只负责选择模型和思考强度。`pr-watch` skill 则委派子代理持续监听 PR 并投递证据。

## 为什么做 Goldilocks

Goldilocks 的灵感来自 [oh-my-openagent](https://github.com/code-yeongyu/oh-my-openagent)。我很喜欢它们“按需分配模型和算力”的设计，但 [LazyCodex](https://github.com/code-yeongyu/lazycodex) 的整套方案对我的使用场景来说偏重。

我目前主要使用 [Superpowers](https://github.com/obra/superpowers) 工作流，且希望保持原有节奏。Goldilocks 仅仅从中提取了一项核心能力：在工作流决定创建子代理时，为其分配最匹配的计算资源。这样既不破坏现有流程，又能避免让简单的任务占用昂贵的大模型，减少不必要的高成本子代理调用，帮助节省 Codex 订阅额度。

## 安装

在终端中运行：

```bash
codex plugin marketplace add baranwang/goldilocks
codex plugin add goldilocks@goldilocks
```

审阅并信任已安装 hooks，再使用加载了该版本的任务。通过 `/hooks` 检查 hook 定义。

启动脚本在首次运行时从 GitHub Releases 下载与插件版本、系统和架构匹配的 Go 二进制，使用随插件保存的 `scripts/SHA256SUMS` 校验后缓存到 `PLUGIN_DATA`。后续直接执行已校验的缓存。普通 CLI 调用未提供该变量时，macOS/Linux 使用 `${XDG_CACHE_HOME:-$HOME/.cache}/goldilocks`，Windows 使用 `%LOCALAPPDATA%/goldilocks`。

无需安装 Node、Python 或 Go。首次运行需要联网访问 GitHub Releases 和 curl（现代 Windows 使用 curl.exe），macOS/Linux 还使用 sha256sum 或 shasum。下载或校验失败会明确报错，可在版本发布、网络恢复后重试。Hook 超时调整为 150 秒以容纳首次下载；修改后的 hooks 需要重新审阅和信任。

## 工作原理

`SessionStart` 和 `SubagentStart` 钩子会注入一段来自 `skills/model-routing/SKILL.md` 的精简策略。在执行已经计划好的 `spawn_agent` 调用前，当前代理会保留用户的显式指定，检查工具的 schema，判断子任务类型，最后仅修改受支持的 `model` 和 `reasoning_effort` 字段。

一个 Go CLI 提供 hooks、PR 监听和 watcher 登记。
在线 PR 读取使用已登录的 gh。
新变化默认在观察到 30 秒静默后合批投递。
本版 PR 监听支持 macOS/Linux；Windows 支持 hooks。

| 路由级别 | 适用场景 | 默认行为 |
|---|---|---|
| `quick` | 机械性、局部性、低风险任务 | 优先 Luna/low；不可用时使用 Terra/low；否则继承父模型 |
| `explore` | 只读的代码搜索与信息整理 | 优先 Luna/low；不可用时使用 Terra/low；否则继承父模型。使用 Terra 时，仅较大范围的综合分析使用 medium |
| `build` | 遵循现有模式的常规代码实现 | 继承父代理模型，使用 medium 强度 |
| `reason` | 调试、代码审查、安全检查及疑难边界情况 | 优先 Sol/high；否则继承父代理模型，使用 high 强度 |
| `deep` | 架构设计、重构迁移、并发及跨模块复杂问题 | 优先 Sol/xhigh；否则继承父代理模型，使用 xhigh 强度 |

Goldilocks 仅使用当前 `spawn_agent` schema 暴露的值，不预设固定的模型列表。用户的显式配置与原工作流的设定始终具有最高优先级。
如果 `fork_turns` 未提供或设为了 `"all"`（导致引入完整历史上下文）且当前接口不支持覆盖计算参数，Goldilocks 会保持 `fork_turns` 不变并直接继承原配置。它绝不会为了强制路由模型而篡改上下文的切分逻辑。

## PR 监听

要求监听一个 PR，或使用 `$pr-watch`。主任务接收 CI、评论和审查证据；子代理独占轮询、投递、确认和清理，直到合并、关闭或明确停止。默认轮询间隔为 60 秒。监听不授权发帖、推送或合并。

单独 skill 仅含指令，需要已有兼容 CLI。
没有已加载且受信任的 hooks 时，子代理跳过 watcher 登记并明确告知缺少生命周期保障。缺少兼容 CLI 时无法启动监听。
机器休眠、应用退出、硬中断或额度耗尽时不保证继续。工具等待可能消耗 token，不是零成本守护进程。

## 许可证

MIT

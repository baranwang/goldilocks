<p align="center">
  <img src="assets/logo.webp" alt="Goldilocks logo" width="192">
</p>

<p align="center">
  <a href="README.md">English</a> · 简体中文 · <a href="README.zh-hant.md">正體中文</a>
</p>

# Goldilocks

**无需改变工作流，只选择恰到好处的模型。**

Goldilocks 是一个轻量级的 Codex 插件。只有当现有工作流已经决定创建子代理时，Goldilocks 才会介入，帮助选择合适的模型和思考强度（reasoning effort）。

它不决定是否创建子代理，也不改变任务或工作流，只负责选择模型和思考强度。

## 为什么做 Goldilocks

Goldilocks 的灵感来自 [oh-my-openagent](https://github.com/code-yeongyu/oh-my-openagent)。我很喜欢它们“按需分配模型和算力”的设计，但 [LazyCodex](https://github.com/code-yeongyu/lazycodex) 的整套方案对我的使用场景来说偏重。

我目前主要使用 [Superpowers](https://github.com/obra/superpowers) 工作流，且希望保持原有节奏。Goldilocks 仅仅从中提取了一项核心能力：在工作流决定创建子代理时，为其分配最匹配的计算资源。这样既不破坏现有流程，又能避免让简单的任务占用昂贵的大模型，减少不必要的高成本子代理调用，帮助节省 Codex 订阅额度。

## 工作原理

`SessionStart` 和 `SubagentStart` 钩子会注入一段来自 `skills/goldilocks/SKILL.md` 的精简策略。在执行已经计划好的 `spawn_agent` 调用前，当前代理会保留用户的显式指定，检查工具的 schema，判断子任务类型，最后仅修改受支持的 `model` 和 `reasoning_effort` 字段。

运行时依赖极简：macOS 和 Linux 环境下直接使用原生的 POSIX `sh`/`awk`，Windows 下使用 PowerShell，完全不需要安装 Node.js 或 Python。

| 路由级别 | 适用场景 | 默认行为 |
|---|---|---|
| `quick` | 机械性、局部性、低风险任务 | 优先 Luna/low；不可用时使用 Terra/low；否则继承父模型 |
| `explore` | 只读的代码搜索与信息整理 | 优先 Luna/low；不可用时使用 Terra/low；否则继承父模型。使用 Terra 时，仅较大范围的综合分析使用 medium |
| `build` | 遵循现有模式的常规代码实现 | 继承父代理模型，使用 medium 强度 |
| `reason` | 调试、代码审查、安全检查及疑难边界情况 | 优先 Sol/high；否则继承父代理模型，使用 high 强度 |
| `deep` | 架构设计、重构迁移、并发及跨模块复杂问题 | 优先 Sol/xhigh；否则继承父代理模型，使用 xhigh 强度 |

Goldilocks 仅使用当前 `spawn_agent` schema 暴露的值，不预设固定的模型列表。用户的显式配置与原工作流的设定始终具有最高优先级。
如果 `fork_turns` 未提供或设为了 `"all"`（导致引入完整历史上下文）且当前接口不支持覆盖计算参数，Goldilocks 会保持 `fork_turns` 不变并直接继承原配置。它绝不会为了强制路由模型而篡改上下文的切分逻辑。

## 安装

在终端中运行：

```bash
codex plugin marketplace add baranwang/goldilocks
codex plugin add goldilocks@goldilocks
```

通过 `/hooks` 命令检查并信任 Goldilocks 的 hook 脚本，然后新建一个 Codex 任务即可生效。

## 许可证

MIT

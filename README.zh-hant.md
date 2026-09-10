<p align="center">
  <img src="assets/logo.webp" alt="Goldilocks logo" width="192">
</p>

<p align="center">
  <a href="README.md">English</a> · <a href="README.zh-hans.md">简体中文</a> · 正體中文
</p>

# Goldilocks

**無需改變工作流程，只選擇恰到好處的模型。**

Goldilocks 是一個提供模型路由和 PR 監聽的輕量級 Codex 外掛。只有當現有工作流程已經決定建立子代理時，模型路由 skill 才會介入，協助選擇合適的模型與思考強度（reasoning effort）。

`model-routing` skill 不決定是否建立子代理，也不改變任務或工作流程，只負責選擇模型與思考強度。`pr-watch` skill 則委派子代理持續監聽 PR 並投遞證據。

## 為什麼做 Goldilocks

Goldilocks 的靈感來自 [oh-my-openagent](https://github.com/code-yeongyu/oh-my-openagent)。我很喜歡它「按需分配模型與運算資源」的設計，但 [LazyCodex](https://github.com/code-yeongyu/lazycodex) 的完整方案對我的使用情境來說偏重。

我目前主要使用 [Superpowers](https://github.com/obra/superpowers) 工作流程，並希望維持原有節奏。Goldilocks 只從中提取一項核心能力：當工作流程決定建立子代理時，為它分配最符合任務需求的運算資源。這樣既不破壞現有流程，也能避免讓簡單任務使用昂貴的大型模型，減少不必要的高成本子代理呼叫，協助節省 Codex 訂閱額度。

## 安裝

在終端機中執行：

```bash
codex plugin marketplace add baranwang/goldilocks
codex plugin add goldilocks@goldilocks
```

審閱並信任已安裝 hooks，再使用載入了該版本的任務。透過 `/hooks` 檢查 hook 定義。

## 運作方式

`SessionStart` 和 `SubagentStart` 鉤子會注入一段來自 `skills/model-routing/SKILL.md` 的精簡策略。在執行已經規劃好的 `spawn_agent` 呼叫前，目前的代理會保留使用者的明確設定、檢查工具的 schema、判斷子任務類型，最後只修改受支援的 `model` 和 `reasoning_effort` 欄位。

一個隨套件 Go CLI 提供 hooks、PR 監聽和 watcher 登記。
無需安裝 Python 或 Go；線上 PR 讀取使用已登入的 gh。
新變化預設在觀察到 30 秒靜默後合併投遞。
本版 PR 監聽支援 macOS/Linux；Windows 支援 hooks。

| 路由級別 | 適用情境 | 預設行為 |
|---|---|---|
| `quick` | 機械式、局部、低風險任務 | 優先 Luna/low；不可用時使用 Terra/low；否則繼承父代理模型 |
| `explore` | 唯讀的程式碼搜尋與資訊整理 | 優先 Luna/low；不可用時使用 Terra/low；否則繼承父代理模型。使用 Terra 時，只有較大範圍的綜合分析才使用 medium |
| `build` | 遵循現有模式的一般程式碼實作 | 繼承父代理模型，使用 medium 強度 |
| `reason` | 除錯、程式碼審查、安全檢查及困難的邊界情況 | 優先 Sol/high；否則繼承父代理模型，使用 high 強度 |
| `deep` | 架構設計、重構與遷移、並行處理及跨模組複雜問題 | 優先 Sol/xhigh；否則繼承父代理模型，使用 xhigh 強度 |

Goldilocks 只使用目前 `spawn_agent` schema 公開的值，不預設固定的模型清單。使用者的明確設定與原有工作流程的設定始終具有最高優先順序。
如果沒有提供 `fork_turns`，或將它設為 `"all"` 而帶入完整的對話歷史，且目前的介面不支援覆寫運算參數，Goldilocks 會保持 `fork_turns` 不變並直接繼承原有設定。它絕不會為了強制模型路由而改變上下文的分支方式。

## PR 監聽

要求監聽一個 PR，或使用 `$pr-watch`。主任務接收 CI、留言和審查證據；子代理獨占輪詢、投遞、確認和清理，直到合併、關閉或明確停止。預設輪詢間隔為 60 秒。監聽不授權發文、推送或合併。

單獨 skill 僅含指令，需要已有相容 CLI。
沒有已載入且受信任的 hooks 時，子代理跳過 watcher 登記並明確告知缺少生命週期保障。缺少相容 CLI 時無法啟動監聽。
機器休眠、應用程式退出、硬中斷或額度耗盡時不保證繼續。工具等待可能消耗 token，並非零成本常駐程式。

## 授權條款

MIT

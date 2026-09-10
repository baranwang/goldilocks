# 统一 Go CLI、PR Watch 与 Hooks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将路由 hooks、PR 轮询与状态、watcher 登记统一到 goldilocks Go CLI，移除 Python 运行和发布依赖。

**Architecture:** 一个可执行文件，以 hook、pr-watch、watcher 三组命令分开职责。PR 数据逻辑放在 internal/prwatch，hook 只调用本地状态读取；App 消息仍由 child 发送。先保留已有状态与 20 个回归场景，再接入静默窗口和已验证的生命周期控制。

**Tech Stack:** Go 1.25.6 标准库、GitHub CLI、Codex collaboration/App tools、Go testing。

**Spec:** [统一 CLI 规格](../specs/2026-09-09-pr-watch-go-hooks-design.md)。本版替换初稿中的 Python helper 实施路线；读取两份文档后执行。

## Global Constraints

- 插件名、marketplace 名和安装标识保持 `goldilocks`、`goldilocks`、`goldilocks@goldilocks`。
- 模型路由只修改 schema 支持的 `model` 和 `reasoning_effort`，保留显式用户选择及 full-history fork 限制。
- 生产启动 hook 只注入模型路由正文，不注入 PR watcher SOP 或探针指令。
- CLI、hooks、PR 轮询、状态和构建程序全部使用 Go 标准库；不新增第三方 Go 依赖、MCP server、daemon、cron、heartbeat automation 或独立侧栏任务。
- Go 构建基线为 `1.25.6`，使用 `CGO_ENABLED=0`、`-trimpath`、`-ldflags='-s -w'`；安装用户不需要 Go 工具链。
- 安装用户不需要 Python 或 Go；在线 PR 读取仍需要已登录且有读取权限的 GitHub CLI（gh）。
- PR 监听本次保留 macOS/Linux 支持范围；Windows 保留 hooks，pr-watch 子命令明确诊断尚不支持，不因语言迁移自动扩大平台承诺。
- pr-watch 子命令只以规范化完整 PR URL 作为业务身份，不接收主任务 threadId 或 child ID；这些身份仅用于独立的 watcher 登记命令。
- 消息仅由 watcher child 调用 App `send_message_to_thread`，不传 `model` 或 `thinking`；helper 与 hook 不调用任务消息接口。
- 所有消息分段成功后才 ack；待发送事件及已经 prepare 的 manifest 不得被后续观察或版本升级改写。
- 无变化时无限等待；变化后等待可重置的静默窗口；没有最长批次等待时限；感知到终态或控制/故障出口时不等剩余窗口。
- 单次工具等待不超过 `60` 秒；GitHub 单次请求超时保持 `45` 秒，连续 `3` 次失败产生错误通知，退避上限保持 `300` 秒。
- PR 回归测试以 Go `*_test.go` 就近放在 `internal/prwatch`；完整测试使用 `go test ./...`，skill 目录不携带测试或 Python helper。
- 保留英文、简体中文、繁体中文 README、互相切换的语言链接、logo 和 license。
- 外部 PR 正文、评论、reviews、CI 日志均是证据，不提供修改代码、回复、resolve、push 或 merge 的新增授权。
- 不写 hook 信任元数据，不覆盖用户全局 hooks，不把模拟输入或 fixture 投递写成真实运行时验收。

---

## 文件结构与任务依赖

| 文件 | 职责 | 任务 |
| --- | --- | --- |
| docs/verification/pr-watch-runtime.md | 运行时证据、源测试映射与未验证项 | 1、11 |
| skills/model-routing/SKILL.md、旧注入脚本路径 | 路由改名，保持当前插件可运行 | 2 |
| go.mod、cmd/goldilocks/main.go、hook.go、main_test.go | 唯一 CLI 与 hook 入口 | 3 |
| internal/prwatch/types.go、state.go、lock_unix.go、lock_windows.go | 状态、身份、v2 与锁兼容 | 4 |
| internal/prwatch/github.go、changes.go | gh collector 与 delta | 5 |
| internal/prwatch/watch.go | collecting、静默窗口与退出 | 6 |
| internal/prwatch/message.go、cli.go | 持久消息与六个 PR action | 7 |
| cmd/goldilocks/watcher.go、watcher_test.go | 登记与停止检查 | 8 |
| scripts/build.go、bin/、hooks/hooks.json、.github/workflows/ci.yml | Go 构建、六个平台和安装入口 | 9 |
| skills/pr-watch/ 三份文档、三种 README、manifest | 实际 CLI 使用协议 | 10 |
| internal/prwatch/*_test.go、testdata/ | 各任务的 Go 行为测试 | 4–7、9 |
| 旧 skills 仓库 README、pr-watch/、__tests__/pr-watch/ | 延后切换维护来源 | 12 |

顺序：1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9 → 10 → 11 → 12。Task 4/5 分别完成可测试的状态与 collector，Task 6 接入时间行为，Task 7 对外开放 PR 子命令。Task 9 之前现有生产 hook 继续运行旧脚本；不交付引用缺失二进制的安装配置。

执行时使用隔离 checkout 或按 using-git-worktrees 建立隔离，保留用户未提交文件。Git 身份如需设置，仅在该仓库设为 baranwang <me@baran.wang>。下列命令从实施仓库根目录运行；rtk proxy 是本机约定，其他机器可直接运行其后的程序。

Go 测试与被测包同目录，所有完整检查使用 `go test ./...`。后续代码块是指定文件中的声明或替换片段，按引用补充标准库 import；PR 外部测试统一 package prwatch_test，并以 pw 导入 github.com/baranwang/goldilocks/internal/prwatch。

## Task 1: 固化运行时证据与发布门槛

**Files:**
- Create: `docs/verification/pr-watch-runtime.md`
- Read: 本规格第 1、12 节；本机 `.superpowers/pr-watch-runtime/` 的原始探针结果（只作开发证据，不发布私有 UUID/本机路径）。

**Interfaces:**
- Consumes: 目标 Codex 版本、实际 hook 事件、child exec 结果。
- Produces: 明确标识 `passed` / `not_verified` 的运行时能力表，供任务 8 与 11 使用；没有生产代码接口。

- [ ] **Step 1: 写入已核实的能力表。** 使用下面的内容，公开记录将真实 UUID 替换为 parent-A/child-A 等标记，并注明一致性由原始记录核对。

```markdown
| Check | Result | Evidence scope |
| --- | --- | --- |
| Baseline helper | passed | pinned source, 20 unittest cases |
| Trusted hook discovery | passed | desktop binary 0.153.4, project hooks |
| Fresh v2 SubagentStart/Stop | passed | ephemeral session using desktop binary |
| Child env UUID equals hook agent_id | passed | exact equality in captured records |
| Two successive continuations | passed | both tested children: false, true, true |
| Original running handle reused | passed | same handle, same token, exit 0 |
| Pending fixture unchanged | passed | both continuation SHA-256 checks match |
| API interruption | passed | running to interrupted; no stop hook; fixture process released separately |
| Current already-running Desktop task reload | not_verified | new probe absent in first old-task child |
| Desktop UI cancellation | not_verified | separate from API interruption |
| Idle App parent wakeup | not_verified | no App test messages sent |
| Real event after 20+ quiet minutes | not_verified | no long-running PR integration yet |
| Cross-platform installed execution | not_verified | native probe only |
```

- [ ] **Step 2: 核对实际机器与功能标志。** 保存命令输出中版本和相关 feature，不把全部用户配置或凭据附到报告。

```sh
rtk proxy codex --version
rtk proxy codex features list
rtk proxy go version
rtk proxy gh --version
```

桌面路径由实际运行进程核对；CLI 默认 `multi_agent_v2=false` 与桌面开启 v2 可不同。复测使用同一桌面二进制并显式 `--enable multi_agent_v2`，不能把 v1 试验作为 v2 证据。

- [ ] **Step 3: 写明失败出口。** 未来版本若无法触发/识别/续跑原 child，任务 8 不得启用生产生命周期保障；可完成离线代码与纯 skill 行为，记录未通过门槛后返回规格修订。不能改用 daemon、额外侧栏任务或无限 followup 重启来“通过”验收。真实取消、App 唤醒与 25 分钟测试留在任务 11，不能勾选为已通过。
- [ ] **Step 4: 记录语言迁移的行为映射。** 原 Python 20 个用例已经在固定来源上通过，只是对照基线；新版正式测试全部用 Go，不要求执行者或安装用户装 Python。采用本文末尾的源用例覆盖表核对迁移，不能仅保留数量而删掉原断言。

- [ ] **Step 5: 提交证据文档。** 核对其没有凭据、私有 task 历史、完整 transcript 或本机绝对路径。

```sh
rtk git add docs/verification/pr-watch-runtime.md
rtk git commit -m "docs(runtime): record PR watcher hook evidence" -m "Co-authored-by: Codex <noreply@openai.com>"
```

## Task 2: 重命名路由 skill，固定迁移来源

**Files:**
- Rename: skills/goldilocks/SKILL.md → skills/model-routing/SKILL.md
- Modify: hooks/inject-router.sh、hooks/inject-router.ps1、三种 README 中的当前路径
- Read-only source: baranwang/skills@7417f5545a3aa070216113299a1ffd9b543a7669

**Interfaces:**
- Consumes: 已有路由规则与源 pr-watch 行为。
- Produces: 可被旧 hooks 和后续 Go hook 读取的 model-routing；本任务不引入新 PR 安装入口。

- [ ] **Step 1: 获取或复用固定来源。** 已有 checkout 先验证 SHA，不重复套用历史 mbox。

```sh
rtk proxy git clone https://github.com/baranwang/skills.git .superpowers/pr-watch-source
rtk git -C .superpowers/pr-watch-source checkout --detach 7417f5545a3aa070216113299a1ffd9b543a7669
```

- [ ] **Step 2: 只改名与当前路径。** frontmatter name 改为 model-routing，标题改为 Model Routing。替换两个旧注入脚本和 README 中 skills/goldilocks 路径，保留 Hard boundary 后的全部规则、logo 与安装标识。

```sh
rtk git mv skills/goldilocks skills/model-routing
rtk proxy perl -pi -e 's/^name: goldilocks$/name: model-routing/; s/^# Goldilocks$/# Model Routing/' skills/model-routing/SKILL.md
rtk proxy perl -pi -e 's{skills/goldilocks}{skills/model-routing}g; s{skills\\goldilocks}{skills\\model-routing}g' hooks/inject-router.sh hooks/inject-router.ps1 README.md README.zh-hans.md README.zh-hant.md
```

- [ ] **Step 3: 审查迁移差异并提交。** 不复制 watch_pr.py 或 Python 测试进插件；新 pr-watch 文档在 Task 10 才启用。

```sh
rtk git diff --check
rtk git diff -- hooks skills README.md README.zh-hans.md README.zh-hant.md
rtk git add hooks skills README.md README.zh-hans.md README.zh-hant.md
rtk git commit -m "refactor(skills): rename model routing" -m "Co-authored-by: Codex <noreply@openai.com>"
```

## Task 3: 建立统一 CLI 与显式 hook 子命令

**Files:**
- Create: `go.mod`、`cmd/goldilocks/main.go`、`cmd/goldilocks/hook.go`、`cmd/goldilocks/main_test.go`
- Read: `skills/model-routing/SKILL.md`、现有两份注入脚本

**Interfaces:**
- Consumes: `goldilocks hook`、PLUGIN_ROOT、stdin hook JSON。
- Produces: `HookEvent`、`RunHook(input io.Reader, output io.Writer, root string) error`、`routingContext(root string) (string, error)`、`version` string；`RunCLI(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer) int` 处理顶层分派。Task 7/8 增加 PR/登记入口，Task 9 才切换安装 hooks。

- [ ] **Step 1: 创建最小 Go module 与行为测试。**

```go
module github.com/baranwang/goldilocks

go 1.25.0

toolchain go1.25.6
```

`main_test.go` 的核心测试使用真实临时文件与 JSON round trip：

```go
func TestRoutingInjection(t *testing.T) {
    root := t.TempDir()
    dir := filepath.Join(root, "skills", "model-routing")
    if err := os.MkdirAll(dir, 0700); err != nil { t.Fatal(err) }
    body := "# Model Routing\n\n保留 \"quotes\"、\\ 和换行。"
    raw := "\ufeff---\r\nname: model-routing\r\n---\r\n\r\n" + body + "\r\n"
    if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(raw), 0600); err != nil { t.Fatal(err) }
    for _, name := range []string{"SessionStart", "SubagentStart"} {
        var out bytes.Buffer
        input := strings.NewReader(fmt.Sprintf(`{"hook_event_name":%q}`, name))
        if err := RunHook(input, &out, root); err != nil { t.Fatal(err) }
        var got struct { HookSpecificOutput struct { HookEventName, AdditionalContext string } }
        if err := json.Unmarshal(out.Bytes(), &got); err != nil { t.Fatal(err) }
        if got.HookSpecificOutput.HookEventName != name || got.HookSpecificOutput.AdditionalContext != body {
            t.Fatalf("unexpected injection: %s", out.String())
        }
    }
}
```

同一文件加入失败输入表；合法 JSON escaping 由 `encoding/json` 负责。Go 代码块分别放入同名文件，使用其中引用的标准库 import；测试 package 统一为 main。

```go
func TestRoutingInvalidInput(t *testing.T) {
    for _, tc := range []struct { name, skill, input string; missing bool }{
        {"frontmatter", "---\nname: broken\n", `{"hook_event_name":"SessionStart"}`, false},
        {"empty", " \n", `{"hook_event_name":"SessionStart"}`, false},
        {"missing", "", `{"hook_event_name":"SessionStart"}`, true},
        {"json", "# Router", `{`, false},
        {"trailing", "# Router", `{"hook_event_name":"SessionStart"} {}`, false},
    } {
        t.Run(tc.name, func(t *testing.T) {
            root := t.TempDir()
            dir := filepath.Join(root, "skills", "model-routing")
            if err := os.MkdirAll(dir, 0700); err != nil { t.Fatal(err) }
            if !tc.missing {
                if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(tc.skill), 0600); err != nil { t.Fatal(err) }
            }
            var out bytes.Buffer
            if err := RunHook(strings.NewReader(tc.input), &out, root); err == nil || out.Len() != 0 {
                t.Fatalf("invalid input must diagnose without injection: %v %q", err, out.String())
            }
        })
    }
}
```

- [ ] **Step 2: 跑测试，确认尚无实现时失败。**

```sh
rtk proxy go test ./cmd/goldilocks -run TestRoutingInjection -v
```

- [ ] **Step 3: 实现最小读取与协议输出。** `HookEvent` 字段至少包含下列 JSON tags，后续不得用自然语言 final 或 task_name 替代身份。

```go
type HookEvent struct {
    Name string `json:"hook_event_name"`
    Cwd string `json:"cwd"`
    SessionID string `json:"session_id"`
    AgentID string `json:"agent_id"`
    AgentType string `json:"agent_type"`
    StopActive bool `json:"stop_hook_active"`
}

func routingContext(root string) (string, error) {
    if root == "" { return "", errors.New("PLUGIN_ROOT is missing") }
    raw, err := os.ReadFile(filepath.Join(root, "skills", "model-routing", "SKILL.md"))
    if err != nil { return "", err }
    text := strings.TrimPrefix(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\ufeff")
    if strings.HasPrefix(text, "---\n") {
        _, rest, ok := strings.Cut(text[4:], "\n---\n")
        if !ok { return "", errors.New("unclosed skill frontmatter") }
        text = rest
    }
    text = strings.TrimSpace(text)
    if text == "" { return "", errors.New("empty routing policy") }
    return text, nil
}
```

RunHook 放入 hook.go，main.go 只放版本和顶层命令。额外 1 byte 用于识别超限；Task 7/8 再添加各自的显式分派，不提前写成功占位分支。

```go
var version = "dev"

func RunHook(input io.Reader, output io.Writer, root string) error {
    raw, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
    if err != nil { return err }
    if len(raw) > 1<<20 { return errors.New("hook input exceeds 1 MiB") }
    var event HookEvent
    if err := json.Unmarshal(raw, &event); err != nil { return err }
    switch event.Name {
    case "SessionStart", "SubagentStart":
        body, err := routingContext(root)
        if err != nil { return err }
        return json.NewEncoder(output).Encode(map[string]any{
            "hookSpecificOutput": map[string]string{
                "hookEventName": event.Name, "additionalContext": body,
            },
        })
    default:
        return nil
    }
}

func RunCLI(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer) int {
    if len(args) == 1 && args[0] == "--version" { fmt.Fprintln(output, version); return 0 }
    if len(args) == 0 || args[0] == "--help" {
        fmt.Fprintln(output, "Usage: goldilocks hook | pr-watch ACTION | watcher ACTION | --version")
        return 0
    }
    if args[0] == "hook" && len(args) == 1 {
        if err := RunHook(input, output, os.Getenv("PLUGIN_ROOT")); err != nil {
            fmt.Fprintln(diagnostics, "goldilocks:", err)
        }
        return 0 // Hook errors diagnose and allow; ordinary CLI errors do not.
    }
    fmt.Fprintln(diagnostics, "goldilocks: unknown or unsupported command")
    return 2
}

func main() {
    os.Exit(RunCLI(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
```

- [ ] **Step 4: 覆盖无外部依赖的命令分派并编译。**

```go
func TestVersionDoesNotNeedTools(t *testing.T) {
    t.Setenv("PATH", "")
    var out, diagnostic bytes.Buffer
    code := RunCLI(context.Background(), []string{"--version"}, strings.NewReader(""), &out, &diagnostic)
    if code != 0 || strings.TrimSpace(out.String()) != version || diagnostic.Len() != 0 { t.Fatal(code, out.String(), diagnostic.String()) }
    out.Reset()
    if RunCLI(context.Background(), []string{"unknown"}, strings.NewReader(""), &out, &diagnostic) != 2 { t.Fatal("unknown action must fail") }
}
```


```sh
rtk proxy gofmt -w cmd/goldilocks
rtk proxy go test ./cmd/goldilocks -v
rtk proxy env CGO_ENABLED=0 go build -trimpath -o .superpowers/goldilocks ./cmd/goldilocks
```

Expected: PASS。当前生产 hooks 仍用已修路径的旧脚本；不在这一任务留下引用缺失二进制的安装配置。

- [ ] **Step 5: 提交 Go 注入单元。**

```sh
rtk git add go.mod cmd/goldilocks
rtk git commit -m "feat(hooks): add Go routing injector" -m "Co-authored-by: Codex <noreply@openai.com>"
```

## Task 4: Go 状态存储、v2 兼容与原锁互斥

**Files:** Create internal/prwatch/types.go、state.go、lock_unix.go、lock_windows.go；internal/prwatch/state_test.go、testdata/v2-pending.json。

**Interfaces:**
- Consumes: 固定源码的 JSON v2、规范化 PR URL、原 .json/.lock/.stop 文件名。
- Produces: ParsePR(string) (PR,error)、NewStore(dir, prURL string) (*Store,error)、ReadState(path string) (State,error)、(*Store).Lock(migrate bool) (func() error,error)、Save() error、PendingEvent() (json.RawMessage,error)、Ack(eventID string) error、Stage(kind string, snapshot Snapshot, changes Changes, failure *string, observations []Observation, at time.Time) (json.RawMessage,error)。
- Store 暴露 PR、Path、StopPath、Data，仅供这个 internal 包及仓库测试使用。ReadState 校验文件而不锁、不迁移，供 hook 快速读取本地状态。

- [ ] **Step 1: 定义状态形状和迁移 fixture。** Snapshot/Changes 保持现有 JSON 对象形状；所有泛型 JSON 解码调用 Decoder.UseNumber，禁止 ID 经 float64。pending 使用 RawMessage，以便保存旧 event 与已 prepare 的 messages。

```go
var ErrLocked = errors.New("another command holds this watch lock")
var ErrUnsupportedPlatform = errors.New("PR monitoring currently supports macOS/Linux")
var ErrStopped = errors.New("watch stop requested")
type PR struct { URL, Host, Owner, Repo, Number, Key string }
type Snapshot map[string]any
type Changes map[string]any
type Observation struct {
    Type string `json:"type"`
    ObservedAt string `json:"observed_at"`
    HeadSHA string `json:"head_sha"`
    Changes Changes `json:"changes"`
}
type Batch struct {
    Snapshot Snapshot `json:"snapshot"`
    Kind string `json:"kind"`
    Observations []Observation `json:"observations"`
    RecoveredError *string `json:"recovered_error"`
}
type State struct {
    Version int `json:"version"`
    PRURL string `json:"pr_url"`
    Snapshot Snapshot `json:"snapshot"`
    Pending json.RawMessage `json:"pending"`
    LastAck *string `json:"last_ack"`
    Error *string `json:"error"`
    Finished bool `json:"finished"`
    Collecting *Batch `json:"collecting"`
}
type Store struct { PR PR; Path, StopPath string; Data State }
type Event struct {
    Source string `json:"source"`
    WatcherID string `json:"watcher_id"`
    EventID string `json:"event_id"`
    Type string `json:"type"`
    PRURL string `json:"pr_url"`
    ObservedAt string `json:"observed_at"`
    HeadSHA *string `json:"head_sha"`
    Changes Changes `json:"changes"`
    Error *string `json:"error,omitempty"`
    Observations []Observation `json:"observations,omitempty"`
}
```

state_test.go 中的辅助函数和测试均使用真实文件；后续 PR 测试复用同文件内的 mustStore/emptySnapshot/check。测试不能在取得锁前执行写操作。

```go
const testPR = "https://github.com/example/project/pull/7"
func check(t *testing.T, err error) { t.Helper(); if err != nil { t.Fatal(err) } }
func mustStore(t *testing.T, dir string) *pw.Store {
    t.Helper(); if runtime.GOOS == "windows" { t.Skip("POSIX PR watch locking") }; s, err := pw.NewStore(dir, testPR); check(t, err); return s
}
func emptySnapshot() pw.Snapshot {
    return pw.Snapshot{"head_sha":"abc123", "state":"OPEN", "draft":false,
        "mergeable":"MERGEABLE", "merge_state":"CLEAN", "checks":[]any{},
        "comments":map[string]any{}, "reviews":map[string]any{}, "threads":map[string]any{}}
}
func TestV2PendingAndManifestSurviveUpgrade(t *testing.T) {
    dir := t.TempDir(); s := mustStore(t, dir)
    legacy := fmt.Sprintf(`{"version":2,"pr_url":%q,"snapshot":null,"last_ack":null,"error":null,"finished":false,"pending":{"event":{"source":"pr-watch","watcher_id":%q,"event_id":"legacy-event","type":"initial","pr_url":%q,"observed_at":"2026-09-09T00:00:00Z","head_sha":null,"changes":{}},"snapshot":null,"error":null,"messages":[{"prompt":"legacy JSON\n原文 \\ and quotes \""}]}}`, testPR, s.PR.Key, testPR)
    check(t, os.WriteFile(s.Path, []byte(legacy), 0600))
    s = mustStore(t, dir)
    release, err := s.Lock(false); check(t, err); check(t, release())
    before, err := os.ReadFile(s.Path); check(t, err)
    if string(before) != legacy { t.Fatal("status-style read rewrote v2") }
    release, err = s.Lock(true); check(t, err)
    var expected, actual any
    check(t, json.Unmarshal([]byte(legacy), &expected))
    check(t, json.Unmarshal(s.Data.Pending, &actual))
    if s.Data.Version != 3 || !reflect.DeepEqual(actual, expected.(map[string]any)["pending"]) { t.Fatal("pending changed") }
    check(t, release())
    s = mustStore(t, dir)
    raw, err := s.PendingEvent(); check(t, err)
    var event pw.Event; check(t, json.Unmarshal(raw, &event))
    if event.EventID != "legacy-event" { t.Fatal(event) }
}
```

将 legacy 字符串生成的完整 JSON 作为 testdata/v2-pending.json 的基线内容；新程序不需要 Python 来生成或校验它。

- [ ] **Step 2: 运行失败用例。**

```sh
rtk proxy go test ./internal/prwatch -run TestV2 -v
```

Expected: 缺少 State/NewStore/Lock 等实现，或旧 manifest 被重写。

- [ ] **Step 3: 实现 PR 身份、解码和校验。** ParsePR 使用 net/url；要求 https、非空 hostname、无 userinfo/port、/OWNER/REPO/pull/正整数，owner/repo 不得为 . 或 ..。保持 owner/repo 大小写，host 小写，忽略 query/fragment/末尾斜线；不重新编码 path 造成旧 key 变化。正则与固定来源的字符范围一致，拒绝百分号转义绕过路径校验。PR.Number 保留字符串，Key 为 canonical URL 的 SHA-256 前 16 个十六进制字符。

```go
sum := sha256.Sum256([]byte(canonicalURL))
key := fmt.Sprintf("%x", sum)[:16]
// Every JSON-object boundary, including nested snapshot values:
decoder := json.NewDecoder(bytes.NewReader(raw))
decoder.UseNumber()
if err := decoder.Decode(&value); err != nil { return err }
if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) { return errors.New("trailing JSON") }
```

ReadState 先验证原始字段存在，再解码到 State：version 只能 2/3；pr_url 必须已规范化；snapshot 为 null 或有效快照；finished 为 bool；last_ack/error 为 null 或 string；v3 必须有 collecting。pending 非 null 时校验 event.source、URL、watcher_id、非空 event_id 和 type，snapshot/error 字段必须存在，messages 若存在必须是非空 prompt-string 数组。collecting 校验 snapshot、kind、非空 observations、type/time/head/changes；不删除未知版本或坏文件。NewStore 再核对读取的 PRURL 与命令输入，只有不存在文件时才创建空 v3 内存状态。

- [ ] **Step 4: 实现同路径 flock 与原子保存。** lock_unix.go 使用 `//go:build darwin || linux`；Windows 文件返回 ErrUnsupportedPlatform。公共 sentinel ErrLocked 与 ErrUnsupportedPlatform 在 types.go 定义，普通 CLI 映射为 3/2。

```go
// lock_unix.go: flock(path string) (func() error, error)
f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
if err != nil { return nil, err }
if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
    f.Close()
    if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) { return nil, ErrLocked }
    return nil, err
}
return func() error { return f.Close() }, nil // Closing this descriptor releases flock.
```

Store.Lock 获取 .lock 后必须 ReadState 重读并验证；失败先释放。migrate=true 时才把有效 version=2 改为 3、增加 collecting=null 并 Save；不得覆盖读取到的未知版本。status 使用 migrate=false。新 watch 在任何网络读取前 Save 空 v3。Save 使用 os.CreateTemp(同目录)、json.Encoder.SetEscapeHTML(false)、File.Sync、Close、Rename；失败移除自己的临时文件并保留原文件。

lock_windows.go 只包含以下实现；公共 JSON 读取仍可用于 Windows hooks，只有 PR command/锁操作被平台边界限制。

```go
//go:build windows

package prwatch

func flock(path string) (func() error, error) {
    return nil, ErrUnsupportedPlatform
}
```

```go
func (s *Store) Save() error {
    f, err := os.CreateTemp(filepath.Dir(s.Path), ".watch-*.tmp")
    if err != nil { return err }
    defer os.Remove(f.Name())
    encoder := json.NewEncoder(f); encoder.SetEscapeHTML(false)
    if err := encoder.Encode(s.Data); err != nil { f.Close(); return err }
    if err := f.Sync(); err != nil { f.Close(); return err }
    if err := f.Close(); err != nil { return err }
    return os.Rename(f.Name(), s.Path)
}
```

Stage 生成一次 crypto/rand UUID v4，并保存 {event,snapshot,error}；observed_at 默认取 at.UTC().Format(time.RFC3339Nano)，有 observations 时取最后一条变化时间。observations 非 nil 表示把 collecting 转 pending，须在同一次 Save 清空 collecting。error 的 observations=nil，保留 collecting，pending.snapshot 仍是已 ack 基线。PendingEvent 返回保存的 event RawMessage。Ack 验证 ID；重复 last_ack 幂等；成功后采用 pending.snapshot/error、更新 last_ack、清 pending，终态才 finished=true 并删除 stop marker；ack error 不能清 collecting。

- [ ] **Step 5: 覆盖锁内复读、身份和错误输入。**

```go
func TestLockRevalidatesAndRemainsExclusive(t *testing.T) {
    s := mustStore(t, t.TempDir())
    release, err := s.Lock(true); check(t, err)
    _, err = mustStore(t, filepath.Dir(s.Path)).Lock(true)
    if !errors.Is(err, pw.ErrLocked) { t.Fatal("second owner acquired lock", err) }
    check(t, release())
    check(t, os.WriteFile(s.Path, []byte(`{"version":999}`), 0600))
    if release, err = s.Lock(true); err == nil { release(); t.Fatal("stale constructor bypassed validation") }
    raw, err := os.ReadFile(s.Path); check(t, err)
    if string(raw) != `{"version":999}` { t.Fatal("corrupt state was replaced") }
}
```

- [ ] **Step 6: 通过 Go 检查并提交。**

```sh
rtk proxy gofmt -w internal/prwatch
rtk proxy go test ./...
rtk git add internal/prwatch
rtk git commit -m "feat(pr-watch): port durable state and locks to Go" -m "Co-authored-by: Codex <noreply@openai.com>"
```

## Task 5: 迁移 GitHub collector 与变化识别

**Files:** Create internal/prwatch/github.go、changes.go；internal/prwatch/github_test.go、testdata/github/。

**Interfaces:**
- Consumes: PR、Snapshot、Changes，源 collect_snapshot/changes_between 的读取与排序规则。
- Produces: `type JSONReader func(context.Context, ...string) (any,error)`；`ReadGH(ctx context.Context,args ...string) (any,error)`；`Collect(ctx context.Context,pr PR,read JSONReader,cancelled func() bool) (Snapshot,error)`；`ChangesBetween(previous,current Snapshot) (Changes,error)`；`CloneSnapshot(Snapshot) (Snapshot,error)`；`type ReadError struct { Err error }`（实现 Error/Unwrap）；`ErrStopped` sentinel。
- ReadError 只表示可重试的 GitHub 读取/响应问题；缺 gh、调用者取消与本地状态错误不能包装成 ReadError。

```go
type JSONReader func(context.Context, ...string) (any, error)
type ReadError struct { Err error }
func (e *ReadError) Error() string { return e.Err.Error() }
func (e *ReadError) Unwrap() error { return e.Err }
```

- [ ] **Step 1: 用真实响应形状测试终态优先和取消。** 网络边界只替换 JSONReader，collector/分页/delta 均运行真实 Go 逻辑。

```go
func TestTerminalMetadataSkipsAllFeedbackRequests(t *testing.T) {
    pr, err := pw.ParsePR(testPR); check(t, err)
    calls := 0
    read := func(ctx context.Context, args ...string) (any,error) {
        calls++
        if calls != 1 || args[0] != "pr" || args[1] != "view" { t.Fatal(args) }
        return map[string]any{"headRefOid":"abc123", "state":"MERGED", "isDraft":false,
            "mergeable":"UNKNOWN", "mergeStateStatus":"UNKNOWN"},nil
    }
    snap, err := pw.Collect(context.Background(), pr, read, func()bool{return false}); check(t, err)
    if calls != 1 || snap["state"] != "MERGED" { t.Fatal(calls,snap) }
    stopped := false; calls = 0
    stopRead := func(ctx context.Context,args ...string)(any,error) {
        stopped=true; return read(ctx,args...)
    }
    _, err = pw.Collect(context.Background(),pr,stopRead,func()bool{return stopped})
    if !errors.Is(err,pw.ErrStopped) || calls != 1 { t.Fatal("cancel after read ignored",err,calls) }
}
```

- [ ] **Step 2: 确认缺少实现时失败。**

```sh
rtk proxy go test ./internal/prwatch -run 'TestTerminalMetadata' -v
```

- [ ] **Step 3: 实现 bounded gh 读取。** 每次实际请求独立 45 秒。使用 exec.CommandContext 的参数数组，不拼 shell。stdout 经 UseNumber 解码；失败只保留最多 1200 个 rune 的诊断，原评论正文不截断。

```go
func ReadGH(ctx context.Context,args ...string)(any,error) {
    callCtx,cancel := context.WithTimeout(ctx,45*time.Second); defer cancel()
    cmd := exec.CommandContext(callCtx,"gh",args...)
    var out,diagnostic bytes.Buffer
    cmd.Stdout,cmd.Stderr=&out,&diagnostic
    if err:=cmd.Run(); err!=nil {
        if ctx.Err()!=nil { return nil,ctx.Err() }
        var missing *exec.Error
        if errors.As(err,&missing) { return nil,err }
        text:=[]rune(strings.TrimSpace(diagnostic.String()))
        if len(text)>1200 { text=text[:1200] }
        if len(text)==0 { text=[]rune(err.Error()) }
        return nil,&ReadError{Err:fmt.Errorf("GitHub read failed: %s",string(text))}
    }
    var value any
    decoder:=json.NewDecoder(&out); decoder.UseNumber()
    if err:=decoder.Decode(&value); err!=nil { return nil,&ReadError{Err:err} }
    if err:=decoder.Decode(new(any)); !errors.Is(err,io.EOF) { return nil,&ReadError{Err:errors.New("trailing GitHub JSON")} }
    if object,ok:=value.(map[string]any); ok && object["errors"]!=nil {
        return nil,&ReadError{Err:errors.New("GitHub GraphQL returned errors")}
    }
    return value,nil
}
```

- [ ] **Step 4: 依原接口顺序移植 collector。** 以下请求表是完整读取顺序；终态在第一个响应后返回。每次 read 前后均检查 ctx.Err 与 cancelled()，分别返回 context error/ErrStopped；响应容器或必需字段不符返回 ReadError。

| 顺序 | gh 参数/响应处理 |
| --- | --- |
| 1 | `pr view URL --json url,number,state,isDraft,mergeable,mergeStateStatus,headRefOid`；映射 head_sha/state/draft/mergeable/merge_state |
| 2 | `api --hostname HOST --paginate --slurp repos/OWNER/REPO/issues/N/comments?per_page=100`；合并所有页 |
| 3 | 同上读取 `repos/OWNER/REPO/pulls/N/reviews?per_page=100` |
| 4 | `api --hostname HOST graphql -f query=THREAD_QUERY -f owner=OWNER -f repo=REPO -F number=N`，后续用 -f cursor=END；忽略 isResolved=true |
| 5 | 每个 thread 的 comments.pageInfo.hasNextPage 为 true 时执行 REPLIES_QUERY，携带 threadId/cursor，合并全部回复 |
| 6 | `api --hostname HOST --paginate --slurp repos/OWNER/REPO/commits/HEAD/check-runs?filter=latest&per_page=100`，展开 check_runs |
| 7 | 同上读取 `repos/OWNER/REPO/commits/HEAD/status?per_page=100`，展开 statuses |

THREAD_QUERY/REPLIES_QUERY 的完整 GraphQL 字段从固定来源两段常量原样复制为 Go raw string：id/body/url/path/line/originalLine/author、isResolved/isOutdated、两层 pageInfo 均保留。在 Go 中只把 %s 占位展开成原 COMMENT_FIELDS/PAGE_INFO 常量，不更改查询范围。

REST numeric id 取 json.Number.String；GraphQL string id 直接保留。统一 comment 字段 id/body/url/author 及存在的 path/line/originalLine/state/commit_id/submitted_at。checks 规范化为 name/status/conclusion/url，status 与 conclusion 使用大写。初始空集合必须为 []/{}；读取和旧状态比较前统一排序 checks，不把旧版 JSON 转义造成的排序差异当新变化。

- [ ] **Step 5: 实现 delta 并保留移出正文。** CloneSnapshot 通过 json.Marshal + UseNumber 解码深拷贝。ChangesBetween 校验两个 snapshot 后按以下字段构造结果；遍历输出使用排序后的 key，禁止随机 map 顺序影响消息。

```go
changes:=Changes{}
for _,key:=range []string{"head_sha","state","draft","mergeable","merge_state","checks"} {
    if previous==nil || !reflect.DeepEqual(previous[key],current[key]) {
        var before any
        if previous!=nil { before=previous[key] }
        changes[key]=map[string]any{"before":before,"after":current[key]}
    }
}
```

comments/reviews/threads 按 ID 比较值；首次 comments 只建基线。首次 reviews 按 submitted_at 与 ID 排序，只保留每位 author 最后一个 CHANGES_REQUESTED/APPROVED/DISMISSED 的决定，输出其中 CHANGES_REQUESTED；COMMENTED 不撤销请求修改。threads 仅输出新/编辑的回复及 outdated 变化。移出的顶层条目保存 *_removed ID 列表；removed_evidence 保存被移出 comments/reviews、仍存在 thread 中被移出的回复，带旧 head_sha。整个 thread 离开 unresolved 只用 threads_removed，不推断其中回复被删除。每条 delta 在采集中单独保存，不用最终快照覆盖中间版本。

```go
func TestInitialBaselineAndEditedCommentDelta(t *testing.T) {
    before:=emptySnapshot()
    before["comments"]=map[string]any{"9007199254740993":map[string]any{"id":"9007199254740993","body":"old","url":testPR}}
    initial,err:=pw.ChangesBetween(nil,before); check(t,err)
    if _,exists:=initial["comments"]; exists { t.Fatal("old top-level comments replayed") }
    after,err:=pw.CloneSnapshot(before); check(t,err)
    after["comments"].(map[string]any)["9007199254740993"].(map[string]any)["body"]="edited"
    delta,err:=pw.ChangesBetween(before,after); check(t,err)
    raw,err:=json.Marshal(delta); check(t,err)
    if !strings.Contains(string(raw),"9007199254740993") || !strings.Contains(string(raw),"edited") { t.Fatal(string(raw)) }
    same,err:=pw.ChangesBetween(after,after); check(t,err)
    if len(same)!=0 { t.Fatal("duplicate observation",same) }
}
```

分页 fixture 使用固定来源 test_collector_paginates_unresolved_threads_and_nested_comments 的原两页 threads 与第二页 replies，保留每个响应内容到 testdata/github。Go 测试逐次核对请求 cursor、返回 thread/reply 数量、resolved 过滤和请求中同一个 head；不以一次 mock 返回合并结果替代真实分页。

- [ ] **Step 6: 通过回归并提交。**

```sh
rtk proxy gofmt -w internal/prwatch
rtk proxy go test ./...
rtk git add internal/prwatch
rtk git commit -m "feat(pr-watch): port GitHub collection and deltas to Go" -m "Co-authored-by: Codex <noreply@openai.com>"
```

## Task 6: Go collecting 与可重置静默窗口

**Files:** Create internal/prwatch/watch.go；internal/prwatch/watch_test.go。

**Interfaces:**
- Consumes: Store、Collect、ChangesBetween、ReadError、ErrStopped。
- Produces: `(*Store).Observe(snapshot Snapshot,at time.Time) (string,error)`（无变化返回空字符串）；`Freeze(kind string,at time.Time) (json.RawMessage,error)`；`Failure(message string,at time.Time) (json.RawMessage,error)`（同一已报告错误返回 nil）；`Stop(at time.Time) (json.RawMessage,error)`；`Watch(ctx context.Context,s *Store,options Options,deps Dependencies) (json.RawMessage,error)`。
- Dependencies 只注入网络、当前时间与等待三个不确定边界，不增加通用 provider/clock 接口。

```go
type Options struct { Interval,Quiet time.Duration }
type Dependencies struct {
    Read func(context.Context,PR,func()bool)(Snapshot,error)
    Now func()time.Time
    Wait func(context.Context,time.Duration)error
}
```

- [ ] **Step 1: 编写实际 Store 配合假时钟的行为测试。** fakeClock 在同一测试文件定义，所有推进通过 Wait；不真实等待 22 秒。

```go
type fakeClock struct { start,now time.Time }
func newClock()*fakeClock { at:=time.Unix(0,0); return &fakeClock{start:at,now:at} }
func(c *fakeClock)Now()time.Time{return c.now}
func(c *fakeClock)Wait(ctx context.Context,d time.Duration)error{c.now=c.now.Add(d);return ctx.Err()}
func(c *fakeClock)Seconds()int{return int(c.now.Sub(c.start)/time.Second)}
func TestQuietResetsAtZeroSixTwelve(t *testing.T) {
    s:=mustStore(t,t.TempDir()); release,err:=s.Lock(true); check(t,err); defer release()
    c:=newClock(); _,err=s.Observe(emptySnapshot(),c.Now()); check(t,err)
    raw,err:=s.Freeze("",c.Now()); check(t,err)
    var event pw.Event; check(t,json.Unmarshal(raw,&event)); check(t,s.Ack(event.EventID))
    var starts []int
    read:=func(ctx context.Context,pr pw.PR,cancelled func()bool)(pw.Snapshot,error){
        second:=c.Seconds(); starts=append(starts,second)
        index:=min(second/6,2)
        snap:=emptySnapshot(); snap["comments"]=map[string]any{"1":map[string]any{"body":strconv.Itoa(index),"url":testPR}}
        return snap,nil
    }
    raw,err=pw.Watch(context.Background(),s,pw.Options{Interval:6*time.Second,Quiet:10*time.Second},pw.Dependencies{Read:read,Now:c.Now,Wait:c.Wait}); check(t,err)
    check(t,json.Unmarshal(raw,&event))
    if !reflect.DeepEqual(starts,[]int{0,6,12,18,22}) || len(event.Observations)!=3 { t.Fatal(starts,event) }
}
```

- [ ] **Step 2: 运行失败测试。**

```sh
rtk proxy go test ./internal/prwatch -run TestQuiet -v
```

- [ ] **Step 3: 实现采集、冻结与独立错误事件。** Observe 先检查 frozen pending/finished；previous 取 collecting.snapshot，否则取已 ack snapshot。terminal 仅更新 metadata，并复制 previous 的集合，不能从终态空集合生成删除。ChangesBetween 无变化时不 Save、不追加、不重置 quiet；首次成功为 initial，已报告错误恢复为 recovered。每条 Observation 记录 UTC 时间/head/delta，批次保存最近 snapshot 和 recovered_error，按错误字符串内容比较，不能比较指针地址；重复成功不重复累计 recovered。batch.kind 保留 initial/recovered，后续普通 update 不覆盖它；MERGED/CLOSED 必须覆盖成对应终态。

Freeze(kind="") 采用 batch.kind，顶层 changes 为最后 delta、observations 为全批；保持最后变化时间，Stage 同一次 Save 转 pending 并清 collecting。Stop 优先返回已有 pending，否则 Freeze("stopped")，无 collecting 也可生成 stopped。Failure 独立 Stage("error")，已报告的相同 message 抑制重复，保留 collecting 与成功基线。Observe/Save 等本地错误直接返回失败，不能被当成 GitHub 可重试错误。

- [ ] **Step 4: 以单调 deadline 实现等待循环。** Watch 进入前 CLI 已拿锁和保存新状态。重启存在 collecting 时 deadline=Now()+Quiet；pending 优先、finished 返回既有 finished envelope、stop marker 优先。单调 Time 不序列化；只将观察时间格式化为 UTC 字符串。

```go
quietDeadline:=time.Time{}
if s.Data.Collecting!=nil { quietDeadline=deps.Now().Add(options.Quiet) }
failures,readFailed:=0,false
stopped:=func()bool{_,err:=os.Stat(s.StopPath);return err==nil}
// Inside the loop, after pending/finished/stop checks:
started:=deps.Now()
snap,err:=deps.Read(ctx,s.PR,stopped)
if errors.Is(err,ErrStopped) { return s.Stop(deps.Now()) }
if err!=nil {
    var failure *ReadError
    if !errors.As(err,&failure) { return nil,err }
    failures++;readFailed=true
    if failures>=3 {
        event,saveErr:=s.Failure(err.Error(),deps.Now())
        if saveErr!=nil || event!=nil { return event,saveErr }
    }
} else {
    failures=0
    kind,saveErr:=s.Observe(snap,deps.Now())
    if saveErr!=nil{return nil,saveErr}
    if kind=="initial" || kind=="merged" || kind=="closed" || (kind=="recovered" && s.Data.Snapshot==nil) { return s.Freeze("",deps.Now()) }
    if kind!="" || (readFailed && s.Data.Collecting!=nil) {quietDeadline=deps.Now().Add(options.Quiet)}
    if s.Data.Collecting!=nil && kind=="" && !readFailed && !quietDeadline.IsZero() && !started.Before(quietDeadline) { return s.Freeze("",deps.Now()) }
    readFailed=false
}
delay:=min(options.Interval,300*time.Second)
for i:=0;i<min(failures,3);i++{delay=min(2*delay,300*time.Second)}
next:=deps.Now().Add(delay)
if !readFailed && !quietDeadline.IsZero() && quietDeadline.Before(next){next=quietDeadline}
for deps.Now().Before(next) && !stopped(){
    if err:=deps.Wait(ctx,min(time.Second,next.Sub(deps.Now())));err!=nil{return nil,err}
}
```

实时 Dependencies 的 Now=time.Now，Read 包装 Collect(ctx,pr,ReadGH,cancelled)，Wait 使用 time.NewTimer/select ctx.Done/ timer.C 并 defer Stop。timer 不 busy spin；调用者取消时保留已持久化内容并返回 context error。

- [ ] **Step 5: 将以下时间表放入表驱动测试，复用 fakeClock。** 输入函数按秒选择 snapshot，断言真实返回事件、观察数与开始读取时间；只更换 Read/Wait，不替换 Store。

| Case | 输入/操作 | 必须断言 |
| --- | --- | --- |
| interval60/quiet10 | 0 秒变化，之后重复 | 开始读取 [0,10]，仅 1 条 observation |
| interval6/quiet10 | 0 秒变化，之后重复 | [0,6,10]，重复不重置 |
| 请求越过 deadline | 0 秒变化；6 秒开始的无变化读取耗时 15 秒 | [0,6,21]，21 秒重新读取后才能结束 |
| 无变化 | initial 已 ack，一直到 30 秒触碰 stop marker | 不在 10 秒结束；stopped 时间 >=30 |
| 终态 | interval=3，0/6/12 变化，15 读取 MERGED/CLOSED | 15 秒返回，先前三次正文在最终通知中 |
| grace stop | 窗口开始后 Wait 在第 5 秒触碰 marker | 第 5 秒停止，证据转 pending |
| restart | collecting 已落盘，重新打开 Store、时钟归零 | 重新等完整 10 秒，不重复追加 |
| error/recovery | 0 秒变化，1/3/7 秒连续 ReadError，投递 ack 后成功 | error 不推进基线；7→17 重新等 10 秒，恢复只追加一次 |

为表驱动数据加入以下可直接复用的测试体；复杂 timeline 的 Read 在表项中闭包构造。

```go
func TestQuietCadence(t *testing.T) {
    for _,tc:=range []struct{interval int;starts []int}{{60,[]int{0,10}},{6,[]int{0,6,10}},{5,[]int{0,5,10}}}{
        s:=mustStore(t,t.TempDir()); release,err:=s.Lock(true); check(t,err)
        c:=newClock(); _,err=s.Observe(emptySnapshot(),c.Now()); check(t,err)
        raw,err:=s.Freeze("",c.Now()); check(t,err); var event pw.Event
        check(t,json.Unmarshal(raw,&event));check(t,s.Ack(event.EventID))
        var starts []int
        read:=func(ctx context.Context,pr pw.PR,cancelled func()bool)(pw.Snapshot,error){
            starts=append(starts,c.Seconds()); snap:=emptySnapshot()
            snap["comments"]=map[string]any{"1":map[string]any{"body":"changed","url":testPR}}
            return snap,nil
        }
        raw,err=pw.Watch(context.Background(),s,pw.Options{Interval:time.Duration(tc.interval)*time.Second,Quiet:10*time.Second},pw.Dependencies{Read:read,Now:c.Now,Wait:c.Wait});check(t,err)
        check(t,json.Unmarshal(raw,&event));check(t,release())
        if !reflect.DeepEqual(starts,tc.starts)||len(event.Observations)!=1{t.Fatal(tc,starts,event)}
    }
}
```

- [ ] **Step 6: 通过全部 Go 回归并提交。**

```sh
rtk proxy gofmt -w internal/prwatch
rtk proxy go test ./...
rtk git add internal/prwatch
rtk git commit -m "feat(pr-watch): batch observations with a resettable quiet window" -m "Co-authored-by: Codex <noreply@openai.com>"
```

## Task 7: Markdown、稳定分段与六个 PR 命令

**Files:** Create internal/prwatch/message.go、cli.go；internal/prwatch/message_test.go、cli_test.go；Modify cmd/goldilocks/main.go。

**Interfaces:**
- Consumes: Store、Event、Changes、Options、Dependencies。
- Produces: `type Message struct { Prompt string }`（json:prompt）；`NotificationBody(event Event) (string,error)`；`ObservationBody(kind string,changes Changes) (string,error)`；`(*Store).Prepare(extra string) (int,error)`；`Message(part int) (Message,error)`；`RunCLI(ctx context.Context,args []string,output io.Writer) error`（internal/prwatch）；`LiveDependencies() Dependencies`。

- [ ] **Step 1: 验证长文本与旧 manifest。** 先用 Task 4 的 v2 fixture 测 Prepare 不改已有 prompt；新事件以真实 Stage 创建，不假造 formatter 返回值。

```go
func TestMessagePartsPreserveUnicodeAndRestart(t *testing.T) {
    dir:=t.TempDir();s:=mustStore(t,dir);release,err:=s.Lock(true);check(t,err)
    body:=strings.Repeat("评论 😀 引号 \" 反斜杠 \\\n",1000)
    snapshot:=emptySnapshot()
    delta:=pw.Changes{"comments":map[string]any{"1":map[string]any{"body":body,"author":"reviewer","url":testPR}}}
    _,err=s.Stage("update",snapshot,delta,nil,[]pw.Observation{{Type:"update",ObservedAt:"2026-09-09T00:00:00Z",HeadSHA:"abc123",Changes:delta}},time.Unix(0,0));check(t,err)
    count,err:=s.Prepare("CI log\nsource: "+testPR);check(t,err)
    if count<2{t.Fatal("not split")}
    parts:=make([]pw.Message,count);var restored strings.Builder
    for i:=range parts{
        parts[i],err=s.Message(i+1);check(t,err)
        if !utf8.ValidString(parts[i].Prompt){t.Fatal("invalid UTF-8")}
        _,chunk,ok:=strings.Cut(parts[i].Prompt,"\n\n");if !ok{t.Fatal("missing header")}
        at:=strings.LastIndex(chunk,"\n\nWatch:");if at<0{t.Fatal("missing footer")}
        restored.WriteString(chunk[:at])
    }
    if !strings.Contains(strings.ReplaceAll(restored.String(),"\n> ","\n"),body){t.Fatal("comment body lost")}
    check(t,release());s=mustStore(t,dir);release,err=s.Lock(true);check(t,err);defer release()
    again,err:=s.Prepare("must not replace old text");check(t,err)
    if again!=count{t.Fatal("manifest split changed")}
    for i,part:=range parts{got,err:=s.Message(i+1);check(t,err);if got!=part{t.Fatal("retry prompt changed")}}
}
```

- [ ] **Step 2: 运行失败测试。**

```sh
rtk proxy go test ./internal/prwatch -run TestMessage -v
```

- [ ] **Step 3: 移植 formatter，按 rune 分段。** ObservationBody 逐项保持 source notification_body 的 CI 差异、原文引用、作者、URL、line/originalLine、outdated、reviewed commit 和状态文案；不包含未变化 CI。新 NotificationBody 顺序渲染每条 observation 的 time/head/正文；终态摘要在前，但不提前 return 丢掉此前正文。removed_evidence 用“上次观察到的正文”和旧 head；threads_removed 仍仅“不再未解决”。旧事件无 observations 时使用原单事件格式。

```go
runes:=[]rune(body)
var chunks []string
for start:=0;start<len(runes);start+=6000{chunks=append(chunks,string(runes[start:min(start+6000,len(runes))]))}
if len(chunks)==0{chunks=[]string{"No additional evidence."}}
messages:=make([]Message,len(chunks))
for i,chunk:=range chunks{
    messages[i]=Message{Prompt:header+"\n\n"+chunk+fmt.Sprintf("\n\nWatch: %s · Event: %s · Part: %d/%d",event.WatcherID,event.EventID,i+1,len(chunks))}
}
```

Prepare 先读取 pending 的原始 map[string]json.RawMessage；messages 已存在时仅校验和返回数量，忽略新 extra。未 prepare 时才格式化、加入带来源的 extra、生成完整 messages；将 messages 加回原 pending map 后一次 Save，保持其余原事件字段。Message 是 1-based 索引，越界返回参数错误。Ack 只由 child 在全数发送成功后调用。

- [ ] **Step 4: 实现 CLI action 参数与执行顺序。** 每个 action 用独立 flag.NewFlagSet(action,flag.ContinueOnError)。合法动作仅 watch/status/prepare/message/ack/stop；共同参数 --pr/--state-dir；watch 的秒参数默认 interval=60、quiet-seconds=30；prepare 的 --body-file 为 UTF-8 文本文件（读取后用 utf8.Valid 校验，非法编码明确失败）；message 必须 --part>0；ack 必须非空 --event-id。不接受旧脚本的位置参数或 thread ID flags。

```go
func seconds(value float64)(time.Duration,error){
    if math.IsNaN(value)||math.IsInf(value,0)||value<=0{return 0,errors.New("seconds must be finite and positive")}
    duration,err:=time.ParseDuration(strconv.FormatFloat(value,'f',-1,64)+"s")
    if err!=nil||duration<=0{return 0,errors.New("seconds out of range")}
    return duration,nil
}
```

PR RunCLI 先检查 action/flags/平台，再 NewStore。stop 不拿 poller 锁，创建 stop marker 并输出 stop_requested。status 用 Lock(false) 探测，ErrLocked→poller_running=true，其余错误不能视为“仍在监听”。其他四个 action 获取 Lock(true) 后执行；watch 保存新 state，再用 LiveDependencies 调 Watch；prepare/message/ack 不访问 gh。输出用 json.Encoder，watch 的 RawMessage 保持 JSON envelope。

顶层 cmd 的 RunCLI 在 hook 分派之后加入 PR 分派，保留 hook 的放行规则：

```go
if args[0]=="pr-watch" {
    runCtx,cancel:=signal.NotifyContext(ctx,os.Interrupt,syscall.SIGTERM);defer cancel()
    err:=prwatch.RunCLI(runCtx,args[1:],output)
    if err==nil{return 0}
    fmt.Fprintln(diagnostics,"goldilocks:",err)
    if errors.Is(err,context.Canceled){return 130}
    if errors.Is(err,prwatch.ErrLocked){return 3}
    return 2
}
```

- [ ] **Step 5: 实际 CLI 验证离线命令不依赖 gh/Python。** cli_test.go 构建一次目标 CLI 到 t.TempDir；用绝对路径启动它，只改变该子进程的 PATH，不影响用于构建的 Go。测试操作环境只有 CLI 与 fixture；不得通过 import Python 或运行源码 helper 实现兼容。

```go
func TestOfflineCLIDoesNotNeedRuntimeTools(t *testing.T){
    if runtime.GOOS=="windows"{t.Skip("PR polling is not supported on Windows in this release")}
    root,err:=filepath.Abs("../..");check(t,err)
    binary:=filepath.Join(t.TempDir(),"goldilocks")
    build:=exec.Command("go","build","-o",binary,"./cmd/goldilocks");build.Dir=root
    output,err:=build.CombinedOutput();if err!=nil{t.Fatalf("%v %s",err,output)}
    dir:=t.TempDir()
    for _,action:=range []string{"status","stop","watch"}{
        cmd:=exec.Command(binary,"pr-watch",action,"--pr",testPR,"--state-dir",dir)
        for _,entry:=range os.Environ(){if !strings.HasPrefix(entry,"PATH="){cmd.Env=append(cmd.Env,entry)}}
        cmd.Env=append(cmd.Env,"PATH=")
        output,err=cmd.CombinedOutput();if err!=nil{t.Fatalf("%s: %v %s",action,err,output)}
        if action=="watch"{var event pw.Event;check(t,json.Unmarshal(output,&event));if event.Type!="stopped"{t.Fatal(event)}}
    }
}
```

同一 CLI harness 覆盖 --quiet-seconds=0/-1/nan/inf、缺 part/event-id、未知 action 均返回 2，锁占用返回 3，未找到 gh 的开放 PR 返回 2；已有 pending 仍能离线返回。终态 ack 后 watch 只返回 finished，不重新投递。完整源用例对照表在文末，逐项落实对应输入和断言。

- [ ] **Step 6: 通过全部回归并提交统一 PR 命令。**

```sh
rtk proxy gofmt -w cmd/goldilocks internal/prwatch
rtk proxy go test ./...
rtk git add cmd/goldilocks internal/prwatch
rtk git commit -m "feat(cli): expose PR watching and durable messages in Go" -m "Co-authored-by: Codex <noreply@openai.com>"
```

## Task 8: 实现仅针对登记 watcher 的停止检查

**Files:**
- Create: `cmd/goldilocks/watcher.go`、`cmd/goldilocks/watcher_test.go`
- Modify: `cmd/goldilocks/main.go`

**Interfaces:**
- Consumes: `HookEvent`，Task 4 的 prwatch.ReadState 与 State，v2/v3 PR state，真实 session/agent UUID，固定的任务原始 cwd。
- Produces: `Registration`、`WatchState`（prwatch.State 别名）；`Register(cwd string, value Registration, resume bool) error`；`Checkpoint(cwd, sessionID, agentID, phase, handle string, stopping bool) error`；`Finish(cwd, sessionID, agentID string, executionEnded bool) error`；`Fail(cwd, sessionID, agentID, reason string, executionEnded bool) error`；`CheckStop(event HookEvent) (map[string]any, error)`；`RunWatcherCLI(args []string, output io.Writer) error`。
- Internal helpers: `registrationPath(cwd, sessionID, agentID string) string`、`loadRegistration(path string) (Registration, error)`、`saveRegistration(path string, value Registration) error`、`lockRegistration(path string) (func(), error)`、`readWatchState(path string) (WatchState, error)`、`nonNull(value json.RawMessage) bool`。全部在 watcher.go，不增加包层级。

- [ ] **Step 1: 写出登记数据与隔离测试。** Registration 的 JSON tags 与规格一致，额外保存清理确认与失败原因：

```go
type Registration struct {
    Version int `json:"version"`
    SessionID string `json:"session_id"`
    AgentID string `json:"agent_id"`
    PRURL string `json:"pr_url"`
    StateFile string `json:"state_file"`
    Status string `json:"status"`
    Phase string `json:"phase"`
    ExecutionHandle string `json:"execution_handle"`
    ExecutionEnded bool `json:"execution_ended"`
    Checkpoint uint64 `json:"checkpoint"`
    LastStopCheckpoint uint64 `json:"last_stop_checkpoint"`
    NoProgressStops int `json:"no_progress_stops"`
    UpdatedAt time.Time `json:"updated_at"`
    FailureReason string `json:"failure_reason,omitempty"`
}

type WatchState = prwatch.State

func nonNull(value json.RawMessage) bool {
    return len(value) > 0 && !bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}
```

下面辅助函数写入完整最小 v3 state 并登记。两个不同 watcher 的测试在其副本上使用不同 agent UUID、PR URL 与 state_file；不靠任务名字判定身份。

```go
func setupRegistration(t *testing.T, cwd string) HookEvent {
    t.Helper()
    state := filepath.Join(cwd, "watch.json")
    raw := `{"version":3,"pr_url":"https://github.com/example/project/pull/7","snapshot":null,"pending":null,"collecting":null,"finished":false,"error":null,"last_ack":null}`
    if err := os.WriteFile(state, []byte(raw), 0600); err != nil { t.Fatal(err) }
    event := HookEvent{Name: "SubagentStop", Cwd: cwd,
        SessionID: "00000000-0000-4000-8000-000000000001",
        AgentID: "00000000-0000-4000-8000-000000000002"}
    value := Registration{Version: 1, SessionID: event.SessionID, AgentID: event.AgentID,
        PRURL: "https://github.com/example/project/pull/7", StateFile: state,
        Status: "active", Phase: "waiting", ExecutionHandle: "exec:7", Checkpoint: 1}
    if err := Register(cwd, value, false); err != nil { t.Fatal(err) }
    return event
}
```

```go
func TestStopBudgetResetsOnlyAfterCheckpoint(t *testing.T) {
    event := setupRegistration(t, t.TempDir())
    for i := 0; i < 2; i++ {
        event.StopActive = i > 0
        decision, err := CheckStop(event)
        if err != nil || decision["decision"] != "block" { t.Fatalf("%v %v", decision, err) }
        if _, exists := decision["continue"]; exists { t.Fatal("must not override other hooks") }
    }
    if err := Checkpoint(event.Cwd, event.SessionID, event.AgentID, "waiting", "exec:7", false); err != nil { t.Fatal(err) }
    decision, err := CheckStop(event)
    if err != nil || decision["decision"] != "block" { t.Fatalf("progress must renew budget: %v %v", decision, err) }
    if _, err := CheckStop(event); err != nil { t.Fatal(err) }
    decision, err = CheckStop(event)
    if err != nil || decision["decision"] == "block" || decision["systemMessage"] == nil {
        t.Fatalf("no progress must become interrupted: %v %v", decision, err)
    }
}
```

未登记和错 parent 的情况直接覆盖如下；不存在登记是正常放行，不创建目录。

```go
func TestUnregisteredChildIsUnaffected(t *testing.T) {
    cwd := t.TempDir()
    event := HookEvent{Name: "SubagentStop", Cwd: cwd,
        SessionID: "00000000-0000-4000-8000-000000000001",
        AgentID: "00000000-0000-4000-8000-000000000002"}
    decision, err := CheckStop(event)
    if err != nil || decision != nil { t.Fatalf("%v %v", decision, err) }
    if _, err := os.Stat(filepath.Join(cwd, "work")); !errors.Is(err, os.ErrNotExist) { t.Fatal(err) }
    event = setupRegistration(t, cwd)
    event.SessionID = "00000000-0000-4000-8000-000000000003"
    decision, err = CheckStop(event)
    if err != nil || decision != nil { t.Fatalf("wrong parent matched: %v %v", decision, err) }
}
```

- [ ] **Step 2: 运行停止检查测试，确认缺少实现而失败。**

```sh
rtk proxy go test ./cmd/goldilocks -run 'TestStop|TestUnregistered|TestRegistration' -v
```

- [ ] **Step 3: 实现原子登记与校验。** registrationPath 使用原始 cwd 下 `work/pr-watch/agents`，文件名为 SHA-256(sessionID + NUL + agentID) 的完整十六进制。拒绝空/格式错误 UUID、非绝对 cwd/state_file、未知 status/phase、非 HTTPS PR URL、不匹配的 state.pr_url、未知 state.version。

原子路径与锁的实现如下；Register 通过校验后才创建 agents 父目录，CheckStop 先查文件存在性，不为普通 child 创建目录。陈旧锁立即报错，不自动删除。

```go
func registrationPath(cwd, sessionID, agentID string) string {
    sum := sha256.Sum256([]byte(sessionID + "\x00" + agentID))
    return filepath.Join(cwd, "work", "pr-watch", "agents", fmt.Sprintf("%x.json", sum))
}

func lockRegistration(path string) (func(), error) {
    name := path + ".lock"
    if err := os.Mkdir(name, 0700); err != nil { return nil, err }
    return func() {
        if err := os.Remove(name); err != nil { fmt.Fprintln(os.Stderr, "goldilocks: release lock:", err) }
    }, nil
}

func saveRegistration(path string, value Registration) error {
    value.UpdatedAt = time.Now().UTC()
    file, err := os.CreateTemp(filepath.Dir(path), ".registration-*.tmp")
    if err != nil { return err }
    defer os.Remove(file.Name())
    if err := json.NewEncoder(file).Encode(value); err != nil { file.Close(); return err }
    if err := file.Sync(); err != nil { file.Close(); return err }
    if err := file.Close(); err != nil { return err }
    return os.Rename(file.Name(), path)
}

func readWatchState(path string) (WatchState, error) {
    return prwatch.ReadState(path)
}

```

loadRegistration 使用 os.ReadFile/json.Unmarshal 读取 Registration，并核对 Version=1、两个 UUID、绝对 StateFile、状态枚举和 phase 枚举；每个调用者同时核对预期 sessionID/agentID 和 state.PRURL。校验后才能进入写入临界区，锁内重读并重复身份检查，防止验证与使用之间被更改。以下枚举是完整值域：

```go
switch value.Status {
case "active", "stopping", "finished", "failed", "interrupted":
default: return value, errors.New("invalid registration status")
}
switch value.Phase {
case "waiting", "delivery", "ack", "cleanup":
default: return value, errors.New("invalid registration phase")
}
```

Register 对同一身份/PR/state 的 active 登记幂等，不重置计数。failed/interrupted 的恢复需要 resume=true；finished 不自动重启，新的 watch 必须来自明确重开请求并使用新 state 路径。Checkpoint 只允许四个 phase，并递增 checkpoint；stopping=true 后状态保持 stopping，不能通过普通 checkpoint 重新变 active。

- [ ] **Step 4: 实现退出状态机。** 先读取并验证登记和业务 state，再在登记互斥内重读登记后更新。核心 budget 逻辑为：

```go
if registered.LastStopCheckpoint != registered.Checkpoint {
    registered.NoProgressStops = 0
}
registered.LastStopCheckpoint = registered.Checkpoint
if registered.NoProgressStops >= 2 {
    registered.Status = "interrupted"
    registered.FailureReason = "watcher made no progress across two hook continuations"
    if err := saveRegistration(path, registered); err != nil { return nil, err }
    return map[string]any{"systemMessage": "Goldilocks PR watcher interrupted: no progress after two continuations. Pending evidence is preserved."}, nil
}
registered.NoProgressStops++
if err := saveRegistration(path, registered); err != nil { return nil, err }
return map[string]any{
    "decision": "block",
    "reason": "Resume this registered PR watcher. Reuse its recorded execution or deliver and acknowledge its pending event. Continue its SOP; do not launch a duplicate poller.",
}, nil
```

此逻辑仅用于 active/stopping；返回前按 state 与登记选择 reason，避免终态后重新轮询。pending/collecting 存在时不能标 finished。Finish 仅在 executionEnded=true、state.finished=true、pending/collecting 均空时成功；Fail 保存 reason 与实际清理确认，允许保留 pending。failed/interrupted 不继续 block。没有登记正常放行；损坏或错配以 systemMessage/stderr 诊断并放行，不删除业务状态。

```go
reason := "Resume this watcher SOP with its actual saved execution handle; do not start a duplicate poller."
switch {
case state.Finished:
    reason = "Terminal event is acknowledged. Verify the original execution has ended, then finish the registration. Do not poll GitHub."
case registered.Status == "stopping":
    reason = "Complete the requested stop: preserve and deliver pending evidence, acknowledge it, and verify execution cleanup. Do not restart polling."
case nonNull(state.Pending):
    reason = "Deliver the saved pending event with its existing manifest, acknowledge only after every part succeeds, then continue the watcher SOP."
}
// Use reason in the returned block map above.
```

`stop_hook_active` 用作观测字段，不作为无条件跳过或始终续跑的开关。Finish 在锁内重读登记与 state 后使用下列条件，然后保存 finished；Fail 则保存 failed/FailureReason/ExecutionEnded，不清空业务 state。

```go
if !executionEnded || !state.Finished || nonNull(state.Pending) || state.Collecting != nil {
    return errors.New("finish requires terminal acknowledgement and confirmed execution cleanup")
}
registered.Status = "finished"
registered.Phase = "cleanup"
registered.ExecutionEnded = true
return saveRegistration(path, registered)
```

- [ ] **Step 5: 接入 CLI 与 hook 分派。** 标准库 flag 解析 `watcher register/checkpoint/finish/fail`。命令参数：`--cwd`、`--session-id`、`--agent-id`；register 增加 `--pr`、`--state-file`、`--phase`、`--handle`、可选 `--resume`；checkpoint 增加 `--phase`、`--handle`、可选 `--stopping`；finish 必须 `--execution-ended`；fail 必须 `--reason`，并按实际情况传 `--execution-ended`。

RunHook 的 switch 添加下面 case；损坏/身份不符的 CheckStop error 由 main 写 stderr，正常无登记返回 nil,nil。

```go
case "SubagentStop":
    decision, err := CheckStop(event)
    if err != nil { return err }
    if decision == nil { return nil }
    return json.NewEncoder(output).Encode(decision)
```

cmd/goldilocks/main.go 的 RunCLI 在默认错误分支前识别 watcher 命令。RunWatcherCLI 使用 flag.ContinueOnError，参数解析与分派如下；Register/Checkpoint/Finish/Fail 负责对应状态转换与校验，不能默默接受未知 action。

```go
// In RunCLI, before the unknown-command fallback:
if args[0] == "watcher" {
    if err := RunWatcherCLI(args[1:], output); err != nil {
        fmt.Fprintln(diagnostics, "goldilocks:", err)
        return 2
    }
    return 0
}
```

```go
func RunWatcherCLI(args []string, output io.Writer) error {
    if len(args) == 0 { return errors.New("missing watcher action") }
    action := args[0]
    flags := flag.NewFlagSet(action, flag.ContinueOnError)
    cwd := flags.String("cwd", "", "original task cwd")
    session := flags.String("session-id", "", "parent UUID")
    agent := flags.String("agent-id", "", "child UUID")
    pr := flags.String("pr", "", "canonical PR URL")
    stateFile := flags.String("state-file", "", "absolute helper state file")
    phase := flags.String("phase", "waiting", "watcher phase")
    handle := flags.String("handle", "", "actual execution handle, empty if none")
    resume := flags.Bool("resume", false, "explicitly resume failed/interrupted registration")
    stopping := flags.Bool("stopping", false, "only complete stop delivery and cleanup")
    ended := flags.Bool("execution-ended", false, "execution termination was verified")
    reason := flags.String("reason", "", "failure diagnosis")
    if err := flags.Parse(args[1:]); err != nil { return err }
    if flags.NArg() != 0 { return errors.New("unexpected watcher arguments") }
    var err error
    switch action {
    case "register":
        err = Register(*cwd, Registration{Version: 1, SessionID: *session, AgentID: *agent,
            PRURL: *pr, StateFile: *stateFile, Status: "active", Phase: *phase,
            ExecutionHandle: *handle, Checkpoint: 1}, *resume)
    case "checkpoint":
        err = Checkpoint(*cwd, *session, *agent, *phase, *handle, *stopping)
    case "finish":
        err = Finish(*cwd, *session, *agent, *ended)
    case "fail":
        if strings.TrimSpace(*reason) == "" { return errors.New("failure reason is required") }
        err = Fail(*cwd, *session, *agent, *reason, *ended)
    default:
        return errors.New("unknown watcher action: " + action)
    }
    if err != nil { return err }
    return json.NewEncoder(output).Encode(map[string]string{"type": action + "_ok"})
}
```

- [ ] **Step 6: 验证终态、损坏、并发和失败出口。**

```go
func TestFinishRequiresAcknowledgementAndCleanup(t *testing.T) {
    event := setupRegistration(t, t.TempDir())
    if err := Finish(event.Cwd, event.SessionID, event.AgentID, true); err == nil {
        t.Fatal("open watch cannot finish")
    }
    if err := Fail(event.Cwd, event.SessionID, event.AgentID, "execution handle lost", false); err != nil { t.Fatal(err) }
    decision, err := CheckStop(event)
    if err != nil || decision["decision"] == "block" { t.Fatalf("explicit failure must exit: %v %v", decision, err) }
}
```

增加 finish 条件表和错误状态表；只有 true/null/null/true 允许 finish。通过修改真实 JSON 覆盖输入边界，断言失败不会删除登记。

```go
func TestFinishStateMatrix(t *testing.T) {
    for _, tc := range []struct { finished, pending, collecting, ended, ok bool }{
        {false, false, false, true, false}, {true, true, false, true, false},
        {true, false, true, true, false}, {true, false, false, false, false},
        {true, false, false, true, true},
    } {
        event := setupRegistration(t, t.TempDir())
        path := registrationPath(event.Cwd, event.SessionID, event.AgentID)
        reg, err := loadRegistration(path)
        if err != nil { t.Fatal(err) }
        raw, err := os.ReadFile(reg.StateFile)
        if err != nil { t.Fatal(err) }
        var state map[string]any
        if err := json.Unmarshal(raw, &state); err != nil { t.Fatal(err) }
        state["finished"] = tc.finished
        if tc.pending { state["pending"] = map[string]any{} }
        if tc.collecting { state["collecting"] = map[string]any{} }
        raw, err = json.Marshal(state)
        if err != nil { t.Fatal(err) }
        if err := os.WriteFile(reg.StateFile, raw, 0600); err != nil { t.Fatal(err) }
        if err := Finish(event.Cwd, event.SessionID, event.AgentID, tc.ended); (err == nil) != tc.ok {
            t.Fatalf("case %+v: %v", tc, err)
        }
    }
}

func TestCorruptRegistrationsNeverBlock(t *testing.T) {
    for _, field := range []string{"json", "version", "session_id", "agent_id", "pr_url", "missing-state", "busy-lock"} {
        t.Run(field, func(t *testing.T) {
            event := setupRegistration(t, t.TempDir())
            path := registrationPath(event.Cwd, event.SessionID, event.AgentID)
            raw, err := os.ReadFile(path)
            if err != nil { t.Fatal(err) }
            var value map[string]any
            if err := json.Unmarshal(raw, &value); err != nil { t.Fatal(err) }
            switch field {
            case "json": raw = []byte("{")
            case "version": value[field] = 999
            case "pr_url": value[field] = "https://github.com/example/project/pull/8"
            case "session_id", "agent_id": value[field] = "00000000-0000-4000-8000-000000000009"
            case "missing-state":
                if err := os.Remove(value["state_file"].(string)); err != nil { t.Fatal(err) }
            case "busy-lock":
                unlock, err := lockRegistration(path)
                if err != nil { t.Fatal(err) }
                defer unlock()
            }
            if field != "json" {
                raw, err = json.Marshal(value)
                if err != nil { t.Fatal(err) }
            }
            if err := os.WriteFile(path, raw, 0600); err != nil { t.Fatal(err) }
            decision, err := CheckStop(event)
            if decision["decision"] == "block" { t.Fatalf("invalid registration blocked: %v", decision) }
            if err == nil && decision["systemMessage"] == nil { t.Fatal("missing diagnostic") }
            after, err := os.ReadFile(path)
            if err != nil || !bytes.Equal(after, raw) { t.Fatalf("invalid data must be preserved: %v", err) }
        })
    }
}
```

并发测试使用同一 cwd、不同 agent/PR/state，分别更新 10 次；同一登记锁冲突由上面的 busy-lock 覆盖。

```go
func TestRegistrationConcurrencyIsIsolated(t *testing.T) {
    event := setupRegistration(t, t.TempDir())
    firstPath := registrationPath(event.Cwd, event.SessionID, event.AgentID)
    second, err := loadRegistration(firstPath)
    if err != nil { t.Fatal(err) }
    second.AgentID = "00000000-0000-4000-8000-000000000003"
    second.PRURL = "https://github.com/example/project/pull/8"
    second.StateFile = filepath.Join(event.Cwd, "second.json")
    raw, err := os.ReadFile(filepath.Join(event.Cwd, "watch.json"))
    if err != nil { t.Fatal(err) }
    raw = bytes.ReplaceAll(raw, []byte("/pull/7"), []byte("/pull/8"))
    if err := os.WriteFile(second.StateFile, raw, 0600); err != nil { t.Fatal(err) }
    if err := Register(event.Cwd, second, false); err != nil { t.Fatal(err) }
    results := make(chan error, 2)
    for _, id := range []string{event.AgentID, second.AgentID} {
        go func(agentID string) {
            for i := 0; i < 10; i++ {
                if err := Checkpoint(event.Cwd, event.SessionID, agentID, "waiting", "exec:7", false); err != nil {
                    results <- err
                    return
                }
            }
            results <- nil
        }(id)
    }
    for i := 0; i < 2; i++ { if err := <-results; err != nil { t.Fatal(err) } }
    for _, id := range []string{event.AgentID, second.AgentID} {
        value, err := loadRegistration(registrationPath(event.Cwd, event.SessionID, id))
        if err != nil || value.Checkpoint != 11 || value.NoProgressStops != 0 {
            t.Fatalf("registrations leaked state: %+v %v", value, err)
        }
    }
}
```

- [ ] **Step 7: 运行 Go 与 PR 回归，提交完整 guard 单元。**

```sh
rtk proxy gofmt -w cmd/goldilocks
rtk proxy go test -race ./cmd/goldilocks
rtk proxy go test ./...
rtk git add cmd/goldilocks
rtk git commit -m "feat(hooks): guard registered PR watcher exits" -m "Co-authored-by: Codex <noreply@openai.com>"
```

## Task 9: 用 Go 构建并分发完整 CLI

**Files:** Create scripts/build.go、bin/SHA256SUMS、六个平台 CLI、.github/workflows/ci.yml；Modify hooks/hooks.json、manifest.version；Delete 原两份注入脚本。

**Interfaces:**
- Consumes: go build ./cmd/goldilocks、main.version、三个命令分支。
- Produces: `go run ./scripts/build.go` 重建并检查产物；`go run ./scripts/build.go --check` 只校验六个已提交产物和 native smoke，不下载/重编译运行文件。
- scripts/build.go 是独立 package main，所有构建辅助函数只在该文件，不成为运行时 CLI 子命令。

- [ ] **Step 1: 固定版本与平台表，先让缺失产物检查失败。** manifest.version=0.2.0；脚本接受零参数或 --check，其他参数失败。

```go
type target struct { OS,Arch,Dir,Name string }
var targets=[]target{
    {"darwin","arm64","Darwin-arm64","goldilocks"},
    {"darwin","amd64","Darwin-x86_64","goldilocks"},
    {"linux","arm64","Linux-aarch64","goldilocks"},
    {"linux","amd64","Linux-x86_64","goldilocks"},
    {"windows","arm64","Windows-ARM64","goldilocks.exe"},
    {"windows","amd64","Windows-AMD64","goldilocks.exe"},
}
```

```sh
rtk proxy go run ./scripts/build.go --check
```

Expected: 缺少产物或校验清单时非零退出；不能以远程 Release 资产替代随包文件。

- [ ] **Step 2: 编译到临时目录，全部成功后再更新 bin。** build.go 的 run() error 读取根 manifest 的 version，再执行下面的构建段；main 仅输出错误并 exit 1。固定工具链、关闭 VCS 变动标记，避免当前提交/dirty 状态影响产物。

```go
stage,err:=os.MkdirTemp("","goldilocks-build-");if err!=nil{return err};defer os.RemoveAll(stage)
for _,t:=range targets{
    output:=filepath.Join(stage,t.Dir,t.Name)
    if err:=os.MkdirAll(filepath.Dir(output),0755);err!=nil{return err}
    cmd:=exec.Command("go","build","-trimpath","-buildvcs=false","-ldflags=-s -w -X main.version="+version,"-o",output,"./cmd/goldilocks")
    cmd.Env=append(os.Environ(),"CGO_ENABLED=0","GOTOOLCHAIN=go1.25.6","GOOS="+t.OS,"GOARCH="+t.Arch)
    cmd.Stdout,cmd.Stderr=os.Stdout,os.Stderr
    if err:=cmd.Run();err!=nil{return err}
}
var sums []string
for _,t:=range targets{
    data,err:=os.ReadFile(filepath.Join(stage,t.Dir,t.Name));if err!=nil{return err}
    path:=filepath.Join("bin",t.Dir,t.Name)
    if err:=os.MkdirAll(filepath.Dir(path),0755);err!=nil{return err}
    if err:=os.WriteFile(path,data,0755);err!=nil{return err}
    if err:=os.Chmod(path,0755);err!=nil{return err}
    digest:=sha256.Sum256(data)
    sums=append(sums,fmt.Sprintf("%x  %s/%s",digest,t.Dir,t.Name))
}
sort.Strings(sums)
if err:=os.WriteFile("bin/SHA256SUMS",[]byte(strings.Join(sums,"\n")+"\n"),0644);err!=nil{return err}
```

check 阶段重新读取固定六个文件并生成同样排序的 SHA256SUMS，与现有清单逐字节相等。验证 Linux 文件前缀 ELF、Windows MZ、macOS Mach-O；不可访问或不匹配立即失败。所有构建版本来自同一 manifest，不另外维护 PR helper 版本。

- [ ] **Step 3: hook command 显式调用 hook 子命令，删除旧业务脚本。** 三个事件复用下面 handler；SessionStart matcher 仍为 startup|resume|clear|compact，SubagentStart/Stop 不设 task_name matcher。

```json
{
  "type": "command",
  "command": "\"${PLUGIN_ROOT}/bin/$(uname -s)-$(uname -m)/goldilocks\" hook",
  "commandWindows": "& \"$env:PLUGIN_ROOT/bin/Windows-$(if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE })/goldilocks.exe\" hook",
  "timeout": 5,
  "statusMessage": "Loading Goldilocks"
}
```

```sh
rtk git rm hooks/inject-router.sh hooks/inject-router.ps1
rtk proxy go run ./scripts/build.go
```

- [ ] **Step 4: native smoke 使用随包二进制和实际 command。** build.go 增加 `smoke(t target,version string) error`：复制 native CLI 与 skills/model-routing/SKILL.md 到名称含空格的临时 plugin root，设置 PLUGIN_ROOT；从另一个子目录执行 --version、三个事件。hook 的启动命令从 hooks/hooks.json 读取，不重写另一套测试专用 launcher。按 OS 使用 /bin/sh -c 或 powershell.exe -NoProfile -NonInteractive -Command。unknown CPU 明确记录 not_verified。

核心执行段如下，变量 binary 为复制到临时目录的 CLI 绝对路径，command 来自对应 handler，root/cwd 为上述临时目录：

```go
versionCommand:=exec.Command(binary,"--version")
output,err:=versionCommand.Output();if err!=nil{return err}
if strings.TrimSpace(string(output))!=version{return errors.New("bundled version mismatch")}
launcher:=[]string{"/bin/sh","-c",command}
if runtime.GOOS=="windows"{launcher=[]string{"powershell.exe","-NoProfile","-NonInteractive","-Command",command}}
cmd:=exec.Command(launcher[0],launcher[1:]...)
cmd.Dir=cwd;cmd.Env=append(os.Environ(),"PLUGIN_ROOT="+root)
payload,_:=json.Marshal(map[string]string{"hook_event_name":event,"cwd":cwd,
    "session_id":"00000000-0000-4000-8000-000000000001","agent_id":"00000000-0000-4000-8000-000000000002"})
cmd.Stdin=bytes.NewReader(payload)
output,err=cmd.Output();if err!=nil{return err}
if event=="SubagentStop"{
    if len(bytes.TrimSpace(output))!=0{return errors.New("unregistered child was affected")}
}else{
    var result struct{ HookSpecificOutput struct{ HookEventName,AdditionalContext string } }
    if err:=json.Unmarshal(output,&result);err!=nil{return err}
    if result.HookSpecificOutput.HookEventName!=event||!strings.Contains(result.HookSpecificOutput.AdditionalContext,"## Hard boundary"){return errors.New("routing injection failed")}
}
```

另在 smoke 后删除临时 native 文件，重跑实际 command，断言失败可诊断，没有 shell/Python fallback。脚本的构建/check/smoke 在开发者/CI 执行，安装用户仅运行预编译 CLI。

- [ ] **Step 5: CI 全部使用 Go。** Windows 只执行 hook/CLI 协议与纯 JSON 检查；要求 POSIX flock 的测试通过 runtime.GOOS 条件明确 skip，不能把 skipped 写成 Windows PR 监听通过。

```yaml
name: CI
on: [push, pull_request]
permissions:
  contents: read
jobs:
  test:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest, windows-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v6
      - uses: actions/setup-go@v5
        with:
          go-version: '1.25.6'
          cache: false
      - run: go test ./...
      - if: runner.os == 'Linux'
        run: go test -race ./...
      - run: go run ./scripts/build.go --check
  binaries:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
      - uses: actions/setup-go@v5
        with:
          go-version: '1.25.6'
          cache: false
      - run: go run ./scripts/build.go
      - run: git diff --exit-code -- bin
```

- [ ] **Step 6: 完整校验并提交。**

```sh
rtk proxy go test ./...
rtk proxy go run ./scripts/build.go --check
rtk git diff --check
rtk git add scripts/build.go bin hooks .github/workflows/ci.yml .codex-plugin/plugin.json
rtk git commit -m "build(cli): bundle the unified Go CLI for six platforms" -m "Co-authored-by: Codex <noreply@openai.com>"
```

## Task 10: 主/子 SOP、三语文档与元数据切到 CLI

**Files:** Create skills/pr-watch/SKILL.md、agents/openai.yaml、references/watcher.md（只迁入三份文档）；Modify 三种 README、.codex-plugin/plugin.json；检查 marketplace 保持 goldilocks@goldilocks。

**Interfaces:**
- Consumes: 完整 goldilocks CLI 的三组命令、真实主任务/child UUID、原始 cwd。
- Produces: 精简主任务入口、child 独占运行与消息投递、无 Python 依赖的安装说明。

- [ ] **Step 1: 迁入三份 skill 文档，移除脚本路径指令。** source 的 Python 文件和 Python 测试不复制；详细行为按原 SOP 加本规格更新。

```sh
rtk proxy mkdir -p skills/pr-watch/agents skills/pr-watch/references
rtk proxy cp .superpowers/pr-watch-source/pr-watch/SKILL.md skills/pr-watch/SKILL.md
rtk proxy cp .superpowers/pr-watch-source/pr-watch/agents/openai.yaml skills/pr-watch/agents/openai.yaml
rtk proxy cp .superpowers/pr-watch-source/pr-watch/references/watcher.md skills/pr-watch/references/watcher.md
```

- [ ] **Step 2: 主任务入口只负责委派、接收和控制。** 最小交接内容如下；真实值由主任务上下文提供，不要求用户手工找 UUID。

```text
Read the actual absolute path to references/watcher.md.
PR URL: the resolved canonical PR URL.
Main task threadId: this main task's actual runtime UUID.
Forward only explicit parameter overrides and an explicit reopened-watch request.
```

主任务记录 canonical PR → child control handle；重复请求复用，idle 用 collaboration.followup_task 恢复原 child。低成本模型依当前 schema 和用户指定选择，不固定 Luna。主任务不读 reference、不跑 CLI 状态/轮询/ack；接收按 Watch/Event/Part 去重，等齐 multipart，依已有授权行动前核对 PR 当前状态和 head。非终态、非明确失败的提前退出最多恢复原 child 一次，持续失败如实报告。

- [ ] **Step 3: child 发现实际 CLI，核对版本并登记。** CLI_BIN 是插件安装根 bin/当前平台/goldilocks 的绝对路径；由已读取 reference 的实际位置确定插件根，不假定 PATH、PLUGIN_DATA 或旧电脑路径。检查 --version=0.2.0 与命令帮助；仅有 skill 且没有兼容 CLI 时明确失败，不使用 Python fallback。

```sh
rtk proxy "$CLI_BIN" --version
rtk proxy "$CLI_BIN" pr-watch status --pr "$PR_URL"
rtk proxy "$CLI_BIN" pr-watch watch --pr "$PR_URL" --interval 60 --quiet-seconds 30
```

所有 PR 命令都使用首次确定的 state-dir 和原始 cwd；下面省略 --state-dir 的示例使用默认值，显式用户参数覆盖示例中的 60/30。WATCH_CWD 始终是原始任务 cwd，STATE_FILE 来自 status。已有 state/pending 先登记再投递；新 watch 先保存空 v3，child 收到执行句柄/完成事件后、进一步等待/投递前登记。若工具返回很快而文件尚未创建，在原句柄上等待一次并核对 state，不能另起进程。锁占用且无法恢复原执行句柄时显式失败。

```sh
rtk proxy "$CLI_BIN" watcher register --cwd "$WATCH_CWD" --session-id "$MAIN_THREAD_ID" --agent-id "$CODEX_THREAD_ID" --pr "$PR_URL" --state-file "$STATE_FILE" --phase waiting --handle "$EXEC_HANDLE"
rtk proxy "$CLI_BIN" watcher checkpoint --cwd "$WATCH_CWD" --session-id "$MAIN_THREAD_ID" --agent-id "$CODEX_THREAD_ID" --phase waiting --handle "$EXEC_HANDLE"
```

- [ ] **Step 4: 详细 SOP 使用下列消息/ack 命令。** 每次等待不超过 60 秒并使用原 session/cell/handle；真实等待返回后才递增 checkpoint。CLI 结束后 prepare→逐段 message→App 发送原 prompt；每段明确成功记录 delivery checkpoint，全部成功才 ack。

```sh
rtk proxy "$CLI_BIN" pr-watch prepare --pr "$PR_URL" --body-file "$EVIDENCE_FILE"
rtk proxy "$CLI_BIN" pr-watch message --pr "$PR_URL" --part 1
rtk proxy "$CLI_BIN" pr-watch ack --pr "$PR_URL" --event-id "$EVENT_ID"
```

没有额外日志文件时省略 --body-file。保留原 SOP 的失败 CI 日志读取：取实际 check URL 的 run/job，使用已认证 gh 只读获取，保留必要摘录与来源；不可用时如实说明。评论完整原文不因为 bot、长文本或看似无效而丢弃。

App 仅由 child 调用实际发现的 send_message_to_thread，threadId=MAIN_THREAD_ID，prompt=message 返回的 prompt 字符串；省略 model/thinking。失败或不确定最多三次同文本重试，不 ack；仍失败保留 pending、报告失败并 watcher fail。非终态 ack 后立即下一轮 watch；CLI error 事件表示读取受损，交付/ack 后继续恢复，不能把它当干净快照。

- [ ] **Step 5: 停止与终态完成原执行清理。**

```sh
rtk proxy "$CLI_BIN" watcher checkpoint --cwd "$WATCH_CWD" --session-id "$MAIN_THREAD_ID" --agent-id "$CODEX_THREAD_ID" --phase cleanup --handle "$EXEC_HANDLE" --stopping
rtk proxy "$CLI_BIN" pr-watch stop --pr "$PR_URL"
rtk proxy "$CLI_BIN" pr-watch status --pr "$PR_URL"
rtk proxy "$CLI_BIN" watcher finish --cwd "$WATCH_CWD" --session-id "$MAIN_THREAD_ID" --agent-id "$CODEX_THREAD_ID" --execution-ended
```

这些命令是流程节点，不能把 stop 后立即 status 看成执行已结束：必须等原执行退出、优先送完 pending、后续 watch 返回 stopped 后投递/ack。仅在 state.finished=true、pending/collecting 均空并确认实际执行结束时 finish。失去句柄或清理无法确认时 watcher fail --reason，不传虚假的 --execution-ended。硬中断可能不触发 Stop hook，不承诺自动恢复或进程清理。

- [ ] **Step 6: 同步三语 README 与 manifest。** 保留语言链接、logo、license、原路由表；将唯一职责文案限定于 model-routing。安装入口继续是 codex plugin marketplace add baranwang/goldilocks、codex plugin add goldilocks@goldilocks。

| English | 简体中文 | 繁體中文 |
| --- | --- | --- |
| One bundled Go CLI provides hooks, PR monitoring, and watcher registration. | 一个随包 Go CLI 提供 hooks、PR 监听和 watcher 登记。 | 一個隨套件 Go CLI 提供 hooks、PR 監聽和 watcher 登記。 |
| No Python or Go installation is required; online PR reads use authenticated gh. | 无需安装 Python 或 Go；在线 PR 读取使用已登录的 gh。 | 無需安裝 Python 或 Go；線上 PR 讀取使用已登入的 gh。 |
| Changes are delivered after 30 seconds of observed quiet by default. | 新变化默认在观察到 30 秒静默后合批投递。 | 新變化預設在觀察到 30 秒靜默後合併投遞。 |
| PR monitoring supports macOS/Linux in this release; Windows covers hooks. | 本版 PR 监听支持 macOS/Linux；Windows 支持 hooks。 | 本版 PR 監聽支援 macOS/Linux；Windows 支援 hooks。 |
| Standalone skills contain instructions only and require a compatible CLI. | 单独 skill 仅含指令，需要已有兼容 CLI。 | 單獨 skill 僅含指令，需要已有相容 CLI。 |
| Review and trust the installed hooks, then use a task that loaded that version. | 审阅并信任已安装 hooks，再使用加载了该版本的任务。 | 審閱並信任已安裝 hooks，再使用載入了該版本的任務。 |

加入机器休眠、应用退出、硬中断与额度耗尽时不保证继续的相同限制；不要承诺零成本守护进程。manifest 的 description/shortDescription/longDescription/defaultPrompt 同时覆盖路由与 PR 监听，keywords 增加 pr-watch；版本 0.2.0 与构建保持一致。

- [ ] **Step 7: 校验当前生产说明没有旧可执行路径并提交。** 旧源码名称可以保留在迁移历史文档；下面搜索范围中不应再出现旧脚本调用。

```sh
rtk rg -n 'watch_pr\.py|python3|Python 3\.9|build-hooks\.py|skills/goldilocks' skills hooks README.md README.zh-hans.md README.zh-hant.md
rtk git diff --check
rtk git add skills/pr-watch .codex-plugin/plugin.json README.md README.zh-hans.md README.zh-hant.md
rtk git commit -m "docs(cli): document unified PR monitoring and watcher ownership" -m "Co-authored-by: Codex <noreply@openai.com>"
```

搜索预期无匹配（rg exit 1）。使用当前插件/skill validator 检查既有 manifest 和 SKILL frontmatter，不增加只检索文案的测试套件。

## Task 11: 对实际安装完成生命周期与消息链路验收

**Files:**
- Modify: `docs/verification/pr-watch-runtime.md`
- Local-only: `.superpowers/issue5-integration/`（测试记录、明确授权的 fixture 内容；不发布私有 UUID、凭据或完整任务历史）

**Interfaces:**
- Consumes: 完整待发布插件、实际 Codex/App 版本、经过授权的专用测试 PR 和测试主任务 UUID。
- Produces: 每项实测结果、可定位的 event/part/head 证据、实际平台清单；没有新产品 API。

- [ ] **Step 1: 先验证全部离线行为。**

```sh
rtk proxy go test -race ./cmd/goldilocks
rtk proxy go test ./...
rtk proxy go run ./scripts/build.go
rtk git diff --exit-code -- bin
rtk git diff --check
```

任何失败先修对应任务，不能靠扩大重试次数跳过。

- [ ] **Step 2: 安装实际候选包并核对加载版本。** 在专用验证环境使用当前 plugin-creator 的安装/更新流程，按实际 marketplace 来源安装候选 revision；不把主分支旧包当作候选包。核对安装缓存中的 manifest.version、二进制 --version 和 SHA256SUMS。通过 `/hooks` 或实际 hooks/list 核对三种事件、command、trustStatus 与所有 SubagentStop 冲突；用户已信任相同定义时不再次索取确认，新定义不得自行写 trusted_hash。

重新加载配置后运行 v2 测试，重复任务 1 的两种提前 final 窗口，至少各两次。生产 hook 不得注入探针身份上下文；child 使用实测 CODEX_THREAD_ID 和原始 cwd 登记。root 控制句柄与登记 UUID 分别记录，不能互换。

- [ ] **Step 3: 验证控制退出和隔离。** 实际测试：普通 child 正常结束；两个不同 PR watcher 同 cwd 互不影响；同 PR 重复请求复用 child；terminal ack 前/后 premature final；stopping 只完成清理；显式失败可退出；同 checkpoint 两次续跑后第三次诊断 interrupted；有新 checkpoint 后再次续跑。验证损坏/错配登记不锁住普通 child，并检查原进程未重复启动。

分别记录 graceful stop、API interrupt 和 Desktop UI 取消。API interrupt 已知可能不触发 SubagentStop；不能据此承诺 hook 自动清理。硬中断后若执行句柄无法恢复，应明确失败并保留 pending。

- [ ] **Step 4: 在 idle 的真实 App 主任务验证一批 multipart。** `WATCH_TEST_PR_URL` 与 `WATCH_TEST_THREAD_ID` 必须来自明确的测试范围和真实运行时。让主任务完成当前回复而处于 idle，child 使用 App 工具发送 helper message 的原始 prompt。逐段核对工具成功响应、主任务是否真的续跑、去重与最终 ack；不传 model/thinking，不用 collaboration.send_message 代替。发送一段成功、下一段失败时，恢复同一 child 后必须保持原 event_id、part 与文本。

只测试已授权的专用 PR。产生测试反馈前将正文写为文件，使用 `--body-file`，避免 shell 插值：

```sh
rtk proxy mkdir -p .superpowers/issue5-integration
rtk proxy tee .superpowers/issue5-integration/comment.md <<'EOF'
Goldilocks integration fixture: event after the quiet observation period.

Preserve "quotes", a backslash \, Unicode 测试, and this newline.
EOF
rtk gh pr comment "$WATCH_TEST_PR_URL" --body-file .superpowers/issue5-integration/comment.md
```

若测试 PR 或允许写入的范围尚未提供，完成其余可验证工作后请求该具体输入；不在用户生产 PR 上注入噪声。

- [ ] **Step 5: 连续观察至少 25 分钟，并在长时间无事件后投递新事件。** 25 分钟是外部观察过程，不是一次工具 sleep；child 每次等待不超过 60 秒，主任务不接管 helper。先保持至少 20 分钟无变化，再执行上一步的已授权评论创建，核对观测时间、quiet 窗口、发送成功、主任务续跑和 ack。随后在 quiet 窗口内增加一条授权的反馈/CI 变化，验证重置；再将专用测试 PR 关闭，验证终态携带本批内容并完成清理。

```sh
rtk gh pr close "$WATCH_TEST_PR_URL"
```

只关闭明确授权的专用 fixture PR；不 merge 代码、不关闭用户实际工作 PR。最终由 watcher 检查无 pending/collecting、poller_running=false，完成 finish；验证者核对无遗留测试执行。全部绿灯或一段等待无输出不是完成标准。

- [ ] **Step 6: 核对六平台安装能力，记录真实范围。** 对每个可运行平台实际执行随包二进制、空格安装路径、不同 cwd、缺失/错误架构的错误路径。Windows 测试 hook 与 pr-watch 不支持时的清楚错误；Go 单进程/无 Python 不等于 Windows polling 已验收。没有对应机器的 native 测试标 not_verified，保留实际交叉构建结果；不能把“六个平台编译成功”扩写为“六个平台安装实测通过”。

- [ ] **Step 7: 将证据写入验证文档并完成发布评审。** 报告覆盖的版本、时间跨度、平台、成功事件数、两种提前结束窗口、取消方式与剩余限制。移除/停用临时项目探针定义，仅删除本次添加的 handler；保留其他用户 hook 和未提交文件。按原工作流处理 PR review、未解决线程与 CI；此计划不自动授权 push/merge/发布。

```sh
rtk git add docs/verification/pr-watch-runtime.md
rtk git commit -m "test(runtime): record PR watcher integration results" -m "Co-authored-by: Codex <noreply@openai.com>"
```

## Task 12: 新来源可用后切换旧 skills 仓库

**Files:**
- In `baranwang/skills`: Modify `README.md`；Delete `pr-watch/`、`__tests__/pr-watch/` 的旧实现与测试。
- Local-only if present: 原 `~/.agents/skills/pr-watch` 开发 symlink。

**Interfaces:**
- Consumes: 任务 11 的实际通过结果、已可安装的 Goldilocks revision、旧仓库最新 main。
- Produces: 新安装路径与迁移说明，只有 Goldilocks 维护实现；无新运行时协议。

- [ ] **Step 1: 验证切换前置条件。** 新版尚未可安装、关键运行时门槛未通过时，不删除旧实现。重新读取旧仓库 main 对比固定交接 SHA，确认迁移以来是否有新修复；有差异先移入 Goldilocks 并重跑相关测试，再继续。

```sh
rtk git -C .superpowers/pr-watch-source fetch origin main
rtk git -C .superpowers/pr-watch-source diff 7417f5545a3aa070216113299a1ffd9b543a7669 origin/main -- pr-watch __tests__/pr-watch
rtk git -C .superpowers/pr-watch-source switch -c codex/move-pr-watch-to-goldilocks origin/main
```

- [ ] **Step 2: 在旧 README 写入明确迁移指引，再移除重复实现。** 文案包含以下实质内容：

```markdown
PR Watch is maintained in [Goldilocks](https://github.com/baranwang/goldilocks).
Install `goldilocks@goldilocks` for model-routing and PR watching with the
plugin's unified Go CLI and hooks. Review and trust the current hooks in Codex.
Standalone `skills/pr-watch` contains instructions only and requires an
existing compatible Goldilocks CLI; it does not install plugin lifecycle hooks. Existing pending state must be preserved and upgraded
by the new helper. Do not delete undelivered events during migration.
```

```sh
rtk git -C .superpowers/pr-watch-source rm -r pr-watch __tests__/pr-watch
```

不删除仓库中其他 skill、其他测试或用户运行数据。旧实现路径若被其他 README 索引引用，同步改为 Goldilocks 的来源链接。

- [ ] **Step 3: 核对实际开发链接。** 只有路径确实是指向旧 pr-watch 的 symlink 时才更新到当前 Goldilocks checkout 的 `skills/pr-watch`。目录或指向其他来源的链接不覆盖；本机不存在时不创建无关安装。记录旧目标以便核对，不生成用户禁止的本地 .bak，不移动未投递状态。

- [ ] **Step 4: 验证旧仓库 diff 与新安装说明后提交。**

```sh
rtk git -C .superpowers/pr-watch-source diff --check
rtk git -C .superpowers/pr-watch-source status --short
rtk git -C .superpowers/pr-watch-source add README.md
rtk git -C .superpowers/pr-watch-source commit -m "docs(pr-watch): move maintenance to Goldilocks" -m "Co-authored-by: Codex <noreply@openai.com>"
```

旧仓库的发布/合并是独立外部变更，按用户现有授权处理；只完成本地变更时如实报告，不能声称迁移已对外完成。

## 原 20 个回归场景的 Go 迁移对照

这里映射的是固定来源的行为断言，不是声称 Go 实现已经通过。执行者必须把每行输入、输出与保留条件落实到对应 Go 文件；测试结果进入 Task 11 验证记录。网络 fixture 只替换 gh 边界，状态、锁、分页、格式化与 CLI 使用真实实现。

| 原 test_ 后缀 | Go 文件 | 保留的断言 |
| --- | --- | --- |
| initial_reports_existing_blockers_and_green_open_does_not_stop | watch_test.go | initial 报阻塞，绿 CI 后仍监听 |
| pending_event_survives_restart_until_successful_delivery | state_test.go | event_id 不变，错 ack 拒绝、重复 ack 幂等 |
| new_and_edited_comments_and_thread_replies_are_delivered | github_test.go | 新增/编辑/行内回复 delta |
| merge_and_close_are_retried_then_finish_after_ack | state_test.go | terminal pending 可重投，ack 后 finished |
| error_does_not_advance_snapshot_and_recovery_is_reported | watch_test.go | error 不推进成功基线，recovered 可见 |
| pr_identity_reuses_state_and_separates_repositories | state_test.go | canonical URL 去重，跨 repo/host/number 隔离 |
| exclusive_lock_blocks_a_second_poller | state_test.go | 独占锁，第二 poller 明确失败 |
| stop_command_ends_watcher_without_contacting_github | cli_test.go | stop→watch 无网络、ack 后 finished |
| cancellation_preserves_pending_delivery_before_stopped_event | state_test.go | 先原 pending，再 stopped |
| a_later_comment_review_does_not_clear_requested_changes | github_test.go | COMMENTED 不覆盖 CHANGES_REQUESTED |
| prepared_message_parts_are_identical_after_partial_send_and_restart | message_test.go | 部分投递重启后 manifest 不变 |
| notifications_keep_comment_evidence_without_raw_event_json | message_test.go | 作者/原文/位置/head/review commit，不发 JSON 外壳 |
| notifications_show_changed_checks_and_removed_feedback | message_test.go | 排除未变化 CI，thread 仅“不再未解决” |
| notification_parts_preserve_long_comments_and_log_excerpts | message_test.go | 完整重组长评论与补充日志 |
| notifications_describe_errors_recovery_and_terminal_states | message_test.go | error/recovered/terminal 文案 |
| existing_json_message_manifest_is_not_rewritten | state_test.go、message_test.go | 原 JSON prompt 和分段原样重用 |
| cancel_during_a_github_call_prevents_further_requests | github_test.go | 单次调用之后取消，不再发后续请求 |
| collector_paginates_unresolved_threads_and_nested_comments | github_test.go | 两层分页、resolved 过滤、固定 head |
| terminal_state_is_reported_even_if_comment_access_is_unavailable | github_test.go | terminal metadata 后不访问评论 |
| cli_waits_quietly_and_exits_on_merge_or_cancellation | cli_test.go | 构建后二进制等待、退出、无无变化通知 |

新增 Go 专属检查还包括：大于 2^53 的 ID、[]rune 分段、跨版本 flock、无 Python/Go 运行环境、独立 action flags 与错误码。新静默窗口、collecting 恢复和两种 premature final 的验收不能被原 20 个测试替代。

## 规格覆盖与交付条件

| 规格 | 任务 |
| --- | --- |
| 统一 CLI、无 Python、原路由边界、源行为映射 | 1–3、7、9–10 |
| v2/pending/manifest/ack、URL 身份、原锁与新状态 | 4、6–7 |
| collector/分页/大整数、head/draft/mergeability/CI/反馈 delta | 5 |
| 静默无限等待、重置、长读取、恢复、终态、停止 | 6 |
| Markdown、完整正文、确定性 Unicode 分段、删除证据 | 5–7 |
| watcher 登记、真实 UUID、续跑预算、故障与清理 | 8、11 |
| 六平台 CLI、Go 构建、安装信任、standalone 边界 | 9–10 |
| App idle 唤醒、multipart ack、25 分钟真实事件 | 11 |
| 新来源可用后旧仓库切换、symlink 与回滚约束 | 12 |

完整回归命令使用 `go test ./...`，自动包含就近放置的包测试。测试通过不代表 App/平台/长时间运行通过，未运行的验收标 not_verified。执行时遇到当前运行时与文档差异，应记录证据并修订对应契约，不增加 daemon、独立任务或无限重启。

## 执行方式

本文交付只更新设计与计划，不启动实现。之后可选择：

1. **Subagent-Driven（推荐）**：按 12 个已定义任务分别执行并做规格/代码评审，使用 subagent-driven-development。
2. **Inline Execution**：使用 executing-plans，在当前任务顺序执行并保留检查点。

用户授权的数据范围、hook 信任和真实验收门槛在两种方式下相同。

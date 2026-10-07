本文档为在本仓库中工作的智能体助手提供指导。

## 项目背景

`go-reader`：一个中文网络小说阅读器，拥有 WebUI（chi + 内嵌模板）和 TUI（bubbletea）两种前端，共享同一套 parser/processor/repo 后端。启动时通过 `UI_TYPE` 选择界面。README 和模板均为中文；保留面向用户的代码中的中文字符串。

## 命令

```bash
go mod tidy
go run ./cmd/reader              # 根据 UI_TYPE 运行 WebUI 或 TUI
go build -o reader.exe ./cmd/reader

go test ./...                    # 全部测试
go test ./tests/parser -run TestRuleParserCSSRules -v   # 单个测试
```

可执行文件旁边必须存在 `.env`（或 `.env.example`）；`config.Get()` 会相对于运行中的二进制文件（而非工作目录）查找 `.env`。数据库连接通过 `DB_URI` 配置，可指向 PostgreSQL、MySQL 或 SQLite。使用 Postgres 时，schema 仍通过 URI 查询串中的 `search_path=...` 指定。WebUI/TUI 启动时通过 GORM（`repo.Migrate`）自动迁移表结构；从 `configs/booksources.sql` 初始化 `book_sources` 数据。

## 架构

### 服务层与领域层

`internal/reading/reading.go` 中的 `reading.Service` 结构体是面向应用层的阅读后端。两个前端的阅读与书架操作都适配于它。
- **Service API**：
  - `Open(ctx, url)`：抓取 URL、解析、更新阅读记录，并在启用时应用日志伪装。
  - `ReloadCurrent(ctx)`：在无需重新抓取的情况下，重新解析 webdriver 当前页面源码，并应用伪装。
  - `Bookshelf(ctx)`：从仓库中按更新时间倒序取出全部阅读记录。
- **伪装配置**：
  - 通过可选配置构建：`reading.New(proc, reading.Disguised())` 会在内容上启用屏幕共享日志伪装。TUI 使用带伪装的服务；WebUI 使用标准服务。

### 请求流程

```
前端 (TUI / WebUI)
    │
    ▼
reading.Service (Open / ReloadCurrent / Bookshelf)
    │
    ▼
processor.Processor
    │
    ├──► webdriver.Driver (Get / PageSource)
    │
    ├──► parser.Engine (ParseContent)
    │
    ├──► processor.postProcess (t2s 简繁转换、空行处理)
    │
    └─► repo (UpdateRecord / GetAllRecords)
```

为编写和测试书源，WebUI 绕过 `reading.Service`，直接调用 `processor.PreviewSource(ctx, src, url)`。该函数仅预览解析规则，不会修改数据库记录，也不会应用后处理。

### 解析引擎

`internal/parser/engine.go` 通过两种策略路由每个页面：

1. **`RuleParser`** — 从 Postgres 中查找书源，选择 `BookSourceURL` 与页面匹配度最高的那个（主机必须相同，路径前缀优先；见 `url.go` 中的 `matchScore`），并运行其 CSS/XPath/regex 规则。若内容规则无结果则返回 `ErrRuleExecution`。
2. **`DefaultParser`** — 兜底启发式规则。剔除噪音标签，捕获 `下一章`/`下一页` 锚点，并选取去重后最大的非 `<div>` 文本块（在最长文本的 15% 范围内）。若存在已启用的 `LLMConfig` 行，标题提取通过 LLM（`/chat/completions`，兼容 OpenAI）完成，否则回退到 `<title>`。

`RuleParser` 实现了 **Legado 规则的一个受限子集**：

- `@css:selector@field` — field 为 `text`（默认）、`html`、`all`、`textNodes`/`ownText`、`href`/`src` 或 `@attr`。field 位置允许嵌套 `@css:`。
- `@XPath:...` 与裸 `//...` — 由 `xpathToCSS` 转换为 CSS。仅支持 `//tag`、`tag[@attr='v']`、`tag[contains(@attr,'v')]` 与 `tag[n]` 谓词。其余情况返回 `""`，规则静默失败。
- `:pattern` — 开头的 `:` 在原始 HTML 上运行 `regexFindAll`。
- `##pat##repl[###]` — 基础规则之后串联的正则替换。`$1` 组引用会在 `re.ReplaceAllString` 之前被改写为 Go 的 `${1}` 模板形式。
- URL 形态的规则（包含 `href`/`src`/`Url`/...）通过 `normalizeURL` 相对页面 URL 解析，同时剔除末尾的 `,{"..."}` Legado 选项标记。

`getTextSep`（位于 `default_parser.go`）替代 goquery 的 `Text()`：它遍历后代文本节点并用分隔符连接。当拼接嵌套的 `<p>`/`<br>` 文本时使用它，因为 goquery 内置的 `Text()` 会无分隔符地压平文本，丢失段落换行。

### Webdriver 层

`webdriver.Driver` 是浏览器驱动风格的接口（`Get`/`PageSource`/`ExecuteScript`/...）。`webdriver.New(name)` 按 `CHROME_DRIVER` 选择实现：

- `none`（默认）→ `NoneDriver`：带 Chrome User-Agent 的纯 `net/http`。
- `chrome` → `ChromeDriver`：经 chromedp + chromedp-undetected 驱动真实 Chrome（有可见窗口）。
- `chrome-headless` → `NewChromeHeadlessDriver()`：同一 `ChromeDriver`，通过 `--headless` 无窗口运行。

**无头模式不要使用 cu 的 `cu.WithHeadless()`**：它靠启动 Xvfb 实现，仅在 Linux 可用，在 Windows 上直接返回 `headless mode not supported in windows`。`ChromeDriver` 改为通过 `cu.WithChromeFlags(chromedp.Flag("headless", true), ...)` 传入 Chrome 自身的 headless 参数，并固定带上 `--no-sandbox` 与 `--disable-dev-shm-usage`（容器内默认能力集没有 `CAP_SYS_ADMIN`，SUID 沙箱起不来）。另外 chromedp 的 `DefaultExecAllocatorOptions`（含默认 `Headless`）**不会**被应用，因为 cu 自己调用 `NewExecAllocator`，所以非 headless 分支确实是有窗口的。`CHROME_VERSION` 仍为占位符，未使用；`CHROME_DATA_DIR` 为空时 cu 使用临时目录。

Docker 镜像（`Dockerfile` 运行阶段）安装了 Debian 的 `chromium` 与 `fonts-noto-cjk`，默认 `CHROME_DRIVER=chrome-headless`，并设置 `HOME=/home/reader`（chromium 的 fontconfig/crashpad 缓存需要可写的 HOME）。

### Repo 层

GORM 模型位于 `internal/model`。`BookSource` 使用 **camelCase 列名**（`bookSourceUrl`、`bookName`、`chapterName`、`nextContentUrl`）以兼容 Legado schema —— 请保持列标签原样。`repo.UpdateRecord` 以 `book_name` 为键执行 upsert，而非 URL。`repo.GetEnabledLLMConfig` 吞掉 "not found" 错误并返回 `(nil, nil)`。

### 后处理与伪装

- **常规后处理**（`processor.postProcess`）：
  - 对书名、章节名和正文运行 `postprocess.ToSimplified`（OpenCC `t2s`）。
  - 将正文段落间距规范为段落之间恰好一个空行。
- **日志伪装**（`postprocess.Disguise`）：
  - 通过 `reading.Service`（TUI 模式）启用。
  - 在每行前加上 `postprocess.GenLog()` —— 一个伪造的 Java/Spring 日志前缀（`<timestamp> <LEVEL> <classname>: `），使终端看起来像构建日志。

### WebUI 细节

- 模板由根目录在 `internal/web/templates` 的 `embed.FS` 提供服务（`static.go`）。
- 当 `WEBUI_TOKEN` 为空时，`AuthMiddleware` 为空操作。设置后，除 `/`、`/api/v1/login`、`/ui/login.html`、`/ui/style*` 与 `/ui/sidebar*` 外，所有路径都需要 `auth_token` cookie。API 请求返回 401；HTML 请求 302 跳转到 `/ui/login.html`。

### TUI 细节

`internal/tui/app.go` 是一个 Bubble Tea 模型，具有 `modeContent` 与 `modeBooks` 两种显示模式。

**加载与请求编排：**
- 模型实现 `beginLoad(loadURL, timeout)` 来管理异步请求。
- 通过 `context.CancelFunc` 取消进行中的操作，并维护一个递增的 `reqID`。
- 在 `Update` 中，若异步消息（`chapterMsg`、`booksMsg`、`backupMsg`）的 `reqID` 与模型当前的 `reqID` 不匹配，则将其丢弃，防止缓慢/过期的操作覆盖视图。

**输入与按键绑定：**
- `Ctrl+B` 是前缀/引导键（设置 `prefixKey = true`）。按下后，期望第二个键：`b` → 书架，`n` → 下一章，`q` → 退出。其他任何键都会清除前缀状态。
- 按 `/` 打开底部输入栏（`showInput = true`）；退格删空则关闭。`bottomReserve`（值为 4）是 `resizeViewport` 使用的布局常量，用于为输入栏 + 提示栏留出空间。
- 斜杠命令：`/exit`、`/quit`、`/books`、`/next`、`/refresh`（重新抓取当前章节 URL）、`/open <url>`、`/export <path>` 与 `/import <path>`。裸 `http...` 输入当作 `/open` 处理。

**内容与导航：**
- `lineLinks` 将渲染的每个视口行映射到一个可点击 URL。鼠标点击通过 `urlAt(screenY)` 映射，并计入 `viewport.YOffset`。鼠标滚轮事件滚动视口。
- `viewportHeader` 将章节标题（回退为 "Reader"）前置到视口内容之前，使其随文本一起滚动。
- 请求进行中时，视口中显示 spinner + "加载中: <url>" 消息。
- **预取**：当章节成功加载且存在 `NextURL` 时，TUI 静默调用 `cmdPrefetch`，通过 `svc.Prefetch` 预热处理器的 LRU 缓存。这不会影响加载/spinner 状态。

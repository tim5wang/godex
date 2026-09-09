# GoDex 自我认知（Self-Knowledge）设计

> 状态：Implemented（v1.4 落地最小闭环）
> 需求：任务看板 t-1788865719870-1「让 godex 更了解它自己」
> 目标：godex 能通过 tool / CLI 按需查阅"它自己有什么功能、怎么用"，且文档与代码不漂移。

## 1. 背景与需求

docs/ 现有 60+ 篇文档（约 16k 行、3.3MB），由 `make docs-check` 维护索引/状态头/链接一致性，
但**文档与代码是两份独立产物**：改代码时文档不会自动跟改，文档漂移是常态。

需求提出两条思路：

1. **思路1（注释内化）**：功能介绍内化到代码注释，经编译/生成输出到文件，供 godex 按需查阅。
2. **思路2（文档 embed）**：优化 docs 文档，编译进二进制，godex 通过 tool / CLI 自查。

发起人倾向思路1，认为它更易解决文档-代码不一致，但可行性待验证。

## 2. 两方案对比

| 维度 | 思路1 注释内化 | 思路2 docs embed |
|---|---|---|
| 一致性 | **注释与代码同位置**，改代码时注释自然跟随，漂移最小 | 文档独立于代码，改代码易忘改文档（漂移来源） |
| 可行性 | ✅ 已实测：`go/parser + ParseComments` 可扫全部 447 个 go 文件（原型验证） | ✅ 已有先例：`internal/uiassets`（web dist）、`internal/core/templates`（builtin yaml）均用 `//go:embed` |
| 实现成本 | 需定义注释标记规范 + 写提取器 + 生成步骤 | 直接 `//go:embed docs/*.md` + 读取 API，成本低 |
| 内容粒度 | 适合"功能点级"短描述（3-8 行），不适合大段教程 | 可承载完整长文档 |
| 维护摩擦 | 生成物需入库或构建时重生成，需防过期 | 无生成步骤，但内容易陈旧 |
| 自查入口 | tool + CLI 均可 | tool + CLI 均可 |

**结论**：两者不互斥，采用**思路1为主、思路2为辅**：
- 核心知识（功能点：是什么/入口/文档）以**代码注释为单一事实源**，`go:generate` 提取生成 Go 数据文件，编译进二进制；
- 需要长篇教程的场景仍保留 docs 文档，注释中的 `文档：` 键指向对应 doc，自查时可提示进一步查阅。

## 3. 落地设计（最小闭环）

### 3.1 注释标记规范

在任意 Go 源文件中，用如下注释块声明一个功能点：

```go
// godex-feature: longtask
// 长任务韧性：Ralph-style LongTask story loop（动态并行 DAG）、auto-repair、
// validation artifact、auto merge/commit、重启恢复、运行中 follow-up/steer。
// 入口：godex longtask、Web Task Center、longtask 工具
// 文档：docs/roadmap、README
func (a *Agent) ...
```

规则：
- 标记行：`// godex-feature: <id>`（id 为 kebab-case，全局唯一）；
- 后续连续注释行为描述，合并为 Description（保留换行）；
- 可选键值行：`入口：`（逗号分隔列表）、`文档：`（逗号分隔路径）；
- 一个注释组只声明一个功能点；提取器记录源码位置 `文件:行号` 便于追溯。

### 3.2 提取器（go:generate）

`internal/selfdocs/gen/main.go`：
- 从仓库根向上查找 `go.mod` 定位根目录；
- 扫描 `internal/`、`cmd/` 下所有非 `_test.go` 的 `.go` 文件；
- `go/parser` 解析注释组，匹配 `godex-feature:` 前缀，解析描述与键值行；
- 生成 `internal/selfdocs/features_gen.go`（`// Code generated ... DO NOT EDIT.`，含 `generatedFeatures []Feature` 字面量）。

生成入口：`go generate ./internal/selfdocs/...`（或 `make docs-gen`）。
生成物**入库**（与 uiassets/templates 一致），保证二进制自带知识；文档明确说明改注释后需重跑生成。

### 3.3 数据与查询 API（internal/selfdocs）

```go
type Feature struct {
    ID          string   `json:"id"`
    Title       string   `json:"title,omitempty"`
    Description string   `json:"description"`
    EntryPoints []string `json:"entry_points,omitempty"`
    Docs        []string `json:"docs,omitempty"`
    Location    string   `json:"location"` // 源码文件:行号
}

func All() []Feature                // 全部功能点
func Get(id string) (Feature, bool) // 按 id 精确查
func Search(q string) []Feature     // 大小写不敏感子串匹配（id/描述/入口/文档）
```

### 3.4 自查入口

**Tool**：`godex_docs`（bundle `core_code`，随 core 默认可用）
- `action=list`：列出全部功能点（id + 首行描述）；
- `action=get`：按 id 查详情；
- `action=search`：关键词搜索。

**CLI**：`godex docs list|get <id>|search <query>`（加入 rootHelpText）。

### 3.5 防过期

- `make docs-gen` 重生成生成物；`make verify` 可选加 `docs-gen` 后 `git diff --exit-code internal/selfdocs/features_gen.go` 检查（本期以文档约定 + 入库为准，不改 verify 门槛）。

## 4. 验收标准

1. `go generate ./internal/selfdocs/...` 能从注释生成 `features_gen.go`；
2. `godex docs list` 输出覆盖 README 全部核心特性（当前 36 个功能点：session-runtime、providers、longtask、agentgraph、workflow、harness、subagent、compaction、memory、notes、session-tree、templates、packages、skills、mcp、lsp、web、browser、desktop、terminal、cron、heartbeat、channels、noderegistry、security、usage、storage-gc、scope、sandbox、session-mode、webui、taskboard、acp、cache、insights、background 等）；
3. `godex docs get <id>` 输出描述/入口/文档/源码位置；
4. agent 会话中 `godex_docs` 工具可 list/get/search（冒烟验证）；
5. `go test ./internal/selfdocs/...` 覆盖 All/Get/Search；
6. 全量 `go test ./...` 无新增失败。

## 5. 后续可选项（本期不做）

- 思路2 的 docs embed（若需要 godex 查阅完整长文档）；
- 生成物新鲜度纳入 CI 门槛（git diff 检查）；
- 子 agent 继承 selfdocs 数据，作为角色知识注入；
- 更多功能点标注（按同一规范批量补充）。

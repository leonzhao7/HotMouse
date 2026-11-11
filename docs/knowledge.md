# sapper-v2 技能与 CTF 知识迁移

HotMouse 内置 sapper-v2 的 56 条离线知识，以及适配后的
`hotmouse-dispatcher`、`hotmouse-ctf` 两个角色技能，并接入 `backend/skills`
的 11 个专项技能，共 13 个。知识与技能随 Go 二进制分发，本地和
Docker 中均可使用。项目页的 **CTF 知识** 标签可按关键词和分类检索，查看
条目方法、命令示例、来源与技能全文。

运行 `./start.sh` 会重新构建并启动应用。已有服务进程需要重新启动，才能使用
更新后的二进制；静态知识无需导入数据库或安装 sapper。

## 离线使用

```bash
cd backend
go build -o hotmouse ./cmd/hotmouse
./hotmouse kb status
./hotmouse kb list --tag web
./hotmouse kb search 'Linux 提权' --tag privesc --limit 3
./hotmouse kb search --query 'SQL injection' --output json
./hotmouse kb show kb041 --output yaml
./hotmouse kb skills
./hotmouse kb skill hotmouse-dispatcher --select content
./hotmouse kb skill web-exploit --select content
./hotmouse kb export ./ctf-reference
```

`kb` 命令直接读取二进制中的资源，不需要启动 HTTP 服务、登录、配置 Profile
或连接 sapper-v2 源目录。输出支持 table、JSON、YAML 与标量 `--select`。
`search` 返回最多 50 条匹配摘要；`list` 列出全部或指定标签的条目。
检索使用中英文分词、中文相邻双字、字段权重与逆文档频率，支持技术别名。
全文读取保留命令与来源；这些命令是示例，不会由检索命令执行。

`export` 导出 `entries/`、`skills/`、来源清单、许可证与免责声明。相同文件可
重复使用，已有不同内容的文件不会被覆盖。导出的技能入口与相对参考链接可供
其它本地 Agent 按需读取。

## Dispatcher 与 Worker 如何使用

Decide 和 Execute 的紧凑上下文加入 `related_knowledge`，最多包含三条摘要。
检索使用当前目标或步骤，以及必要事实；规划时还使用近期事实。执行召回优先
使用当前步骤与其前提，避免其它路线的近期结果挤占相关方法。纯“获取 CTF
flag”一类通用词不触发自动召回，完整日志和整库正文不会注入上下文。

实际调用 provider 时，Dispatcher 加载 `hotmouse-dispatcher`，执行 Worker
加载 `hotmouse-ctf`。二者分别说明证据驱动规划、阶段划分、止损语义，以及
知识检索、现有产物复用、有限步骤执行与结果复核。原生 Codex、Claude、Pi
和自定义 command 都通过相同的 `HOTMOUSE_WORKER_PROMPT` 接收技能。

每次规划或执行在自身工作区的
`.hotmouse/knowledge/{bundle-version}/` 提供完整的离线参考。版本由内置资源
内容计算，服务重启可以复用同版本文件。自定义适配器可使用：

| 环境变量 | 内容 |
| --- | --- |
| `HOTMOUSE_SKILL` | 本次调用的技能名称 |
| `HOTMOUSE_SKILL_FILE` | 导出的技能入口绝对路径 |
| `HOTMOUSE_KNOWLEDGE_DIR` | 完整知识与参考资料目录 |
| `HOTMOUSE_KNOWLEDGE_VERSION` | 内容版本 |
| `HOTMOUSE_KNOWLEDGE_PROMPT` | 本次技能与读取说明 |
| `HOTMOUSE_SKILLS_DIR` | 全部角色与专项技能的绝对目录 |
| `HOTMOUSE_SKILLS_PROMPT` | 专项技能索引、总纲与运行时适配说明 |
| `HOTMOUSE_PENTEST_TOOLKIT_DIR` | 共享工具库绝对目录 |
| `HOTMOUSE_PENTEST_TOOLKIT_PROMPT` | 本地工具与离线资料的入口说明 |

提示词列出相关条目的绝对路径，CLI 不在 PATH 时也可直接读取 JSON。目标服务
分类和执行恢复总结保留各自的阶段约束，不加载执行技能。恢复总结继续只整理
保存的证据，不开展新检查。

## 导入的专项技能与工具库

`backend/skills` 是专项技能源码，构建时直接嵌入，不维护另一份 `.agents/skills`
镜像。默认执行总纲为 `flag-hunting`，专项技能覆盖侦察、Web、认证、过滤、代码
执行、云、二进制、逆向、多阶段与检测验证。提示词列出各技能的描述及绝对路径，
Agent 根据现有证据读取所需正文。Dispatcher 只用来规划，执行 Worker 仍只处理
当前 Step。原文中的 PI、driver、Cairn 和 memory 约定由运行时适配到当前 Agent、
协调器、Finding/result、事实图与工作区证据。CTFy 同样接收专项技能，遵循平台
自己的结果和提交协议。

`backend/pentest_toolkit` 的脚本、字典、playbook、HackTricks、nuclei 模板和
ExploitDB 保持在共享磁盘目录，避免为每个 Worker 复制整库。运行时从服务 cwd、
二进制位置和工作区向上发现 `pentest_toolkit` 或 `backend/pentest_toolkit`。
Docker 三种镜像都将工具库复制到 `/usr/local/share/hotmouse/pentest_toolkit`。
环境变量 `HOTMOUSE_PENTEST_TOOLKIT_DIR` 可指定其它位置，Worker/Dispatcher
保存的 provider environment 同名设置优先；显式配置的无效路径会报错。
独立二进制未发现工具库时仍可使用内置知识与技能。

提示词提供现象索引、各资料目录和脚本目录，只读取相关资料。执行脚本使用绝对
路径，当前 cwd 保持在 Worker 工作区；输出、脚本改版、auth_queue 数据库和证据
均写在自己的工作区。共享索引不回填修改。工具是否可用以实际能力清单为准；
Docker 在构建时安装常用命令与 Python 依赖，见 [Docker 文档](../docker/README.md)。
工具库根目录的独立 `go.mod` 将 ExploitDB 中的 Go/C 示例排除在后端 `go test ./...`
之外，它们仍可作为文件读取和按需改编。

## 研究结论与适配

参考源码来自 `/home/kali/workspace/sapper-v2` 的提交
`ba3d3d49a3211b7d26e96cee9df9788506c3e16c`。重点研究了 `SKILL.md`、
`skill/SKILL.md`、`docker/commander/SKILL.md`、reason/explore/verify 模板，
以及 `lib/kb.py` 和全部 `kb/entries/`。

| sapper-v2 的机制 | HotMouse 中的适配 |
| --- | --- |
| JSON 事实意图图、项目 setup/hooks | 使用已有 SQLite 事实图、租约和紧凑上下文 |
| 指挥官读板、侦察分级、同族方法迁移 | Dispatcher 技能，基于现有事实规划有限且不重复的步骤 |
| 执行前 recall/KB，标题→摘要→证据 | 三条知识召回、按路径读全文、Fact 与 result.detail/evidence |
| 独立 verify 流程 | 执行技能的复核参考，沿用 HotMouse 的 Step 结果协议 |
| tmux 军团、固定模型/实例数/比赛时长 | 使用已有 Dispatcher 配置、Worker 池、项目上限和真实预算 |
| 即时 flag 保存与平台提交 | Worker 返回 Finding；Dispatcher 原子提交事实和台账；适配器负责平台请求 |

56 条原始条目的标题、标签、来源和时间保持可追溯。其中七条（kb019、kb020、
kb022、kb023、kb024、kb025、kb053）调整了 sapper 命令、平台配置和调度说明。
收尾盲目重提 flag 的建议改为核对 pending/unknown 状态；路线搁置按 HotMouse
的实际语义使整个项目失败。详细的源文件与迁入文件 SHA-256、调整标记和上游
提交保存在 [provenance.json](../backend/internal/knowledge/provenance.json)。
原 MIT 许可证和免责声明随知识包保留。

KB 是方法参考，不是当前项目的已确认事实。历史 CVE、产品版本范围、赛制和
工具预装假设须根据当前证据确认。工具库和 nuclei 模板由上述共享目录提供，
命令及可选依赖以运行环境为准。
跨项目学习继续使用已有 `experience` 存储，知识召回不会写入项目事实或经验表。
具体图协议见 [strategy.md](strategy.md)。

## HTTP 读取接口

所有接口沿用服务认证。没有知识写入接口。

| 接口 | 内容 |
| --- | --- |
| `GET /api/knowledge` | 来源、版本、条目数、技能数、标签计数 |
| `GET /api/knowledge/entries?q=SSRF&tag=web&limit=5` | 有 query 时检索；无 query 时列出全部或指定标签条目 |
| `GET /api/knowledge/entries/{id}` | 条目全文 |
| `GET /api/knowledge/skills` | 技能列表及入口全文 |
| `GET /api/knowledge/skills/{name}` | 单个技能入口全文 |

验证覆盖来源校验、中英文检索排序、标签筛选、上下文前提召回、文件导出复用
与已有内容保护、离线 CLI、API 认证、真实子进程读取知识并提交结果，以及
浏览器检索/筛选/全文阅读。尚未进行真实 CTF 得分、成功率或模型成本对比。

# CTF 解题 skills

目标是在当前授权赛题中持续取得更多可信 flag。HotMouse Worker 只执行分配的当前步骤，Dispatcher 负责规划，比赛协调器负责调度与 flag 提交。技能提供判型与下一步检查，当前任务协议、项目授权和工作区约束优先。

## 目录与路由

此目录是 11 个专项技能的源码，由 Go 嵌入二进制。保持每个 SKILL.md 的 name 与目录名一致；修改后重新构建并重启服务即可生效，不需要同步 .agents/skills。

运行时与两个 HotMouse 角色技能一起导出到工作区的 `.hotmouse/knowledge/{version}/skills/`，绝对路径通过 `HOTMOUSE_SKILLS_DIR` 提供。Dispatcher、Codex、Claude、Pi 和自定义 command 共享提示词中的技能索引，按当前证据读取专项 SKILL.md；执行 Worker 与 CTFy Agent 默认收到 flag-hunting 总纲。`hotmouse kb skills`、`kb skill web-exploit --select content` 和知识页也可读取这些技能。

保留的技能正文来自原 PI 工程。运行时适配说明将 PI 映射为当前 Agent、driver 映射为协调器、Cairn fact 与 tree/deadends 映射为当前结果协议、事实图和工作区证据。执行 Worker 通过 Finding/result 返回 flag；CTFy Agent 遵循平台题面与结果协议。启动判型和恢复总结不注入专项执行指导。

| 场景 | 加载 |
|---|---|
| 每题起手与决策 | flag-hunting |
| 服务未知 | recon-fingerprint |
| Web/API/业务逻辑 | web-exploit；有身份时加 auth-attack |
| Web 过滤与解析差异 | filter-bypass |
| 在线执行或沙箱 | code-exec |
| 对象存储、云身份、容器、K8s | cloud-exploit |
| 内存漏洞 | binary-pwn |
| 静态/动态逆向 | reverse-engine |
| 已有立足点但目标仍未完成 | multi-stage |
| 题面明确考检测或规避 | detection-evaluation |

只加载当前题需要的专项技能；已有可复用原语时优先接续利用。成熟组件先检索本地 pentest_toolkit 知识，再根据版本与回显验证。避免无依据大扫描、账号口令矩阵、公开 payload 盲打。

## 镜像工具边界

Docker 镜像带有 pentest_toolkit、curl、ffuf、sqlmap、git-dumper、Python pwntools、gdb、ROPgadget、7z、unsquashfs、rg、jq、ssh、nc、socat 等。Python 依赖在构建时安装到 `/opt/hotmouse/venv`，由 `HOTMOUSE_PYTHON` 指定。技能正文提及的 radare2、binwalk、etcdctl 等可选命令须根据实际环境确认；nuclei-templates 是本地资料，未安装 nuclei 命令。宿主机运行时复用固定能力清单，仅补查当前步骤必需的命令或 import。

本地运行会发现 `backend/pentest_toolkit`，Docker 中使用 `/usr/local/share/hotmouse/pentest_toolkit`，也可通过 `HOTMOUSE_PENTEST_TOOLKIT_DIR` 指定。引用与脚本使用此根目录下的绝对路径，输出和认证台账写在当前 Worker 工作区；共享工具库保持原样。

不在赛时执行 apt、pip、npm、gem 安装，不从外部站点下载工具或脚本。遇到缺少的工具，先选镜像已有的等效命令，或用 Python 标准库写当前题脚本。对题目授权目标的 HTTP 请求与读取题面文件不属于下载工具。

## 执行规则

只测试当前题 Origin 授权的资产和网段。扫描、脚本及响应写在当前 Worker 工作区；跨重试证据通过 result.detail/evidence 和事实图保存。可信 flag 按当前任务协议返回，并在当前步骤范围内继续推进。认证测试遵守项目授权与工具的 auth_queue 规则。

时间预算用作检查点：没有新证据就换观测通道或攻击面；协调器决定项目切换。每个发现记录基准、变化、结果和结论适用范围。

## 覆盖边界

聚焦本赛场常见的 Web、API、二进制、云和多阶段题。无线、社工、真实企业持久化等不在这套技能的默认范围。遇到题面明确考这些方向，再按证据增加专项技能。

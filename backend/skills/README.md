# CTF 解题 skills

目标是在当前授权赛题中持续取得更多可信 flag。PI 只处理 driver 分配的当前题；driver 负责题目调度和 flag 提交。技能提供判型与下一步检查，不替代题目证据和 PI_SYSTEM_PROMPT.md 的规则。

## 目录与路由

运行时从 .agents/skills 加载；skills 是同内容镜像。改动须同步两处，并保持每个 SKILL.md 的 name 与目录名一致。

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

比赛时使用下列镜像内工具和随镜像复制的 pentest_toolkit。Web 用 curl、ffuf、sqlmap、git-dumper；二进制/逆向用 Python pwntools、gdb、ROPgadget、radare2、binwalk、7z、unsquashfs；云题在确认目标后可用 etcdctl。基础命令如 grep、rg、jq、gzip、ssh、nc、socat 也在镜像中。nuclei-templates 是本地资料，未安装 nuclei 命令。

不在赛时执行 apt、pip、npm、gem 安装，不从外部站点下载工具或脚本。遇到缺少的工具，先选镜像已有的等效命令，或用 Python 标准库写当前题脚本。对题目授权目标的 HTTP 请求与读取题面文件不属于下载工具。

## 执行规则

只测试当前题 Origin 授权的资产和网段。扫描、脚本及响应写在当前题工作区；跨重试事实写入 memory/<题号>.tree.md 和 deadends.md。可信 flag 立即记为 Cairn fact，继续寻找本题其他 flag；PI 不调用提交接口。默认无外网，不使用外部回连或外带。认证测试遵守系统规则的速率和字典上限。

时间预算用作检查点：没有新证据就换观测通道或攻击面；driver 决定项目切换。每个发现记录基准、变化、结果和结论适用范围。

## 覆盖边界

聚焦本赛场常见的 Web、API、二进制、云和多阶段题。无线、社工、真实企业持久化等不在这套技能的默认范围。遇到题面明确考这些方向，再按证据增加专项技能。

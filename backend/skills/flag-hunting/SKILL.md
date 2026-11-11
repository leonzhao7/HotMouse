---
name: flag-hunting
description: 离线 CTF 多 flag 题目总纲。每题判型并选择专项 skill；仅使用镜像已有工具和本地资料，记录可信 flag 后继续推进。
---

# CTF 多 flag 总纲

**目标：在当前题持续获得更多可信 flag。** 先检查低成本的直接路径，再沿已确认原语推进。能从配置/注释/接口直接读出，就先检查直接读取路径，再按题目目标决定是否继续利用。

## 1. 固定行动流程

```
① 侦察 30-60s      → ② 判定题型 → ③ 打该题型"最可能一击命中"的那一招
④ 命中即记录为 Cairn fact    → ⑤ 检查线索并换攻击面
```

## 2. 题型决策树

| 观察到的特征 | 题型 | 第一动作 | 专项 skill |
|---|---|---|---|
| 响应头/Cookie/注释里直接有 `flag` | 送分 | 确认来源并记为 fact | — |
| 出现 WAF/403/过滤/压缩提示 | 过滤绕过 | 先定位拦截点与后端解析 | `filter-bypass` |
| 登录框 + 角色区分（`developer`/`admin`） | 认证/越权 | 读前端 JS + 改 role/伪造会话 | `auth-attack`、`web-exploit` |
| 静态资源目录 + `Index of` 列举 | 路径穿越 | `static..` 拼接穿越读上级目录 | `web-exploit` |
| 有 `?file=`/`?preview=`/`?page=` 参数 | 文件读取 | 直接 `../../../../flag` | `web-exploit` |
| `/api/*`、`/api/functions`、S3、OAuth、STS | 云原生 | 未授权列举 + config 接口 | `cloud-exploit` |
| 在线执行代码 / 规则引擎 / RSC | 代码执行 | 逃逸 / 原型污染 | `code-exec` |
| 裸 TCP，nc 连通即有菜单/欢迎语 | 二进制 | 长度越界读 | `binary-pwn` |
| 单文件可执行 / 固件 / 序列号校验 | 逆向 | strings + 逆推校验 | `reverse-engine` |

**判型不清** → 先走 `recon-fingerprint` 30 秒，再回本表。

## 3. 检查点与换面

| 场景 | 检查点 | 无新证据时 |
|---|---|---|
| 直接泄露 / 云配置 | 初探后 | 核对入口与权限，再换观测通道 |
| Web / 代码执行 | 同类尝试无差异时 | 换攻击面或判据 |
| 逆向 / pwn / 多步链 | 每建立一个新原语后 | 先找目标，再决定下一跳 |

**熔断规则（最重要）**
- **同一 payload 微调超过 3 次仍失败 → 换攻击面**，不是继续调 payload。
- 连续 2 次同类命令无新信息 → 立即切换方向。
- 连续尝试无新证据 → 换攻击面；项目切换由 driver 决定。

## 4. 输出卫生（直接决定推理速度与是否被压缩）

```bash
nmap ...   > scan.txt 2>&1; head -c 2000 scan.txt
curl ...   > response.txt    2>&1; grep -iE 'flag|token|admin|secret|error' response.txt | head -20
```
- 大输出**一律落盘 + grep/head**，禁止整段回灌上下文。
- 每步只留一句结论。**不重复探索已否证路径**。
- 记录死路清单：`当前题 memory/<题号>.deadends.md`，后续先查再试。

## 5. 提交与校验

- 每发现一个可信 flag 就记为 Cairn fact；继续寻找本题其他 flag。
- **排除占位符**：`flag{...}`、`flag{test}`、`flag{example}`、被截断的残片。
- 同一值只记录一次，保留来源。

```python
import re
FLAG = re.compile(r"flag\{[^}\s]{6,200}\}", re.I)
BAD  = {'flag{...}', 'flag{test}', 'flag{example}', 'flag{redacted}'}
def extract(t):
    return [m for m in FLAG.findall(t)
            if m.lower() not in BAD and len(set(m[5:-1])) > 2]
```

## 6. 最高性价比的 6 个动作（先试这些）

1. **读原始响应全量**：`curl -i`，看 `server`/`x-powered-by`/`set-cookie`/`location`/HTML 注释/前端 JS。
   实战中：账号、接口路径、JWT 结构、角色名常直接写在前端 JS 或页脚里。
2. **未授权接口**：`/admin`、`/api/config`、`/control.php`、`/debug`、`/preview`。
3. **前端 JS 抽接口**：`curl /js/*.js | grep -oE '"/[a-z0-9_/.-]+"' | sort -u`——比目录爆破快 10 倍。
4. **路径穿越读上级目录**：`/public/static../`（nginx 拼接缺陷）、`?file=../../../../flag`。
5. **改角色字段**：`role`/`is_admin`/`user_type`/`email` 提权；或伪造会话 cookie。
6. **备份/配置泄露**：`.git/`、`www.zip`、`config.php.bak`、`.env`、`*.key`、`*.pem`。

## 7. 反模式清单

- ❌ 上手全端口或大字典扫描；先用题面线索与有限端口探测。
- ❌ 同一 payload 无限微调（大量 `curl` 调用是同一请求换编码重发）。
- ❌ 无证据地重复同一尝试。
- ❌ 大输出直接进上下文（导致上下文被 compact，丢失早期关键发现）。
- ❌ 把占位符当有效 flag 记录。
- ❌ **已确认的线索不落盘，上下文压缩后丢失**——关键发现（路径、密钥、token 结构）必须写进文件。

## 8. 镜像内工具边界

比赛时只使用镜像已提供的命令、Python 标准库和本地 pentest_toolkit。常用入口为 curl、nmap、ffuf、sqlmap、git-dumper、python3、pwntools、gdb、ROPgadget、radare2、binwalk、7z、unsquashfs 和 etcdctl。使用前可用 command -v 检查；缺失时改用镜像内的等效方法，不在赛时 apt/pip/npm/gem 安装，也不从外部网站下载工具或脚本。nuclei-templates 只作本地 YAML 知识检索，不代表已安装 nuclei 命令。

## 9. 运行时规则与跨阶段路由

只测试当前题 Origin 授权范围。输出、脚本和响应保存在当前题工作区；跨重试事实写入 memory/<题号>.tree.md、deadends.md 和 reports/<题号>.md。不得使用外部回连或外带。PI 不调用提交接口，driver 负责提交与切题。检查点用于调整方向，不是停止当前题或整场比赛的命令。

已有文件读取、会话、凭据或 shell，且 flag 尚未找到时加载 multi-stage。云身份与权限关系使用 cloud-exploit。题面明确要求检测或规避时加载 detection-evaluation；仅 Web 过滤使用 filter-bypass。

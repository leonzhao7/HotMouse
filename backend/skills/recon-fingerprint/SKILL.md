---
name: recon-fingerprint
description: 快速侦察与服务/技术栈指纹识别。拿到新目标后的 30-60 秒内完成 HTTP 指纹、端口探测、技术栈判定与题型归类，并输出一行结论。遇到未知目标和服务时加载。 使用 curl、nmap、nc、rg。
---

# 快速侦察与指纹

目标：**30-60 秒**内拿到足够信息判定题型。不做无差别全端口深扫。

## 1. HTTP 指纹（≤15s，最高优先）

```bash
# 一次性拿到状态码/头/跳转/重定向链
curl -s -i -m 5 http://TARGET/ | head -60

# 关注这几个头：server / x-powered-by / set-cookie / location / x-envoy-*
# 例：server: envoy + x-powered-by: PHP/5.6.40 → 网关 + 老 PHP 应用
```

**必须看原始响应体**（不只是头）：

```bash
curl -s -m 5 http://TARGET/ -o page.html
grep -oiE '<title>[^<]*|<!--.*?-->' page.html | head -20     # 标题与注释
grep -oiE 'src="[^"]*\.js|href="[^"]*\.js' page.html | head   # 前端 JS 入口
```

## 2. 前端 JS 是情报金矿（这一步常被跳过，但收益最高）

```bash
# 抽出所有 JS 并搜索接口、角色、账号、密钥
for js in $(grep -oE '/[a-zA-Z0-9_/.-]+\.js' page.html | sort -u | head -10); do
  echo "== $js"; curl -s -m 5 "http://TARGET$js" | grep -oE '"/[a-zA-Z0-9_/.-]+"|role|admin|token|key|email|password' | sort -u | head -30
done
```

实战中前端 JS 直接暴露：登录接口、`role` 字段名、管理员邮箱、JWT 结构（`kid`/`alg`）。

## 3. 端口与服务（≤20s）

```bash
nmap -Pn -T4 --top-ports 100 -sV TARGET > scan.txt 2>&1; head -40 scan.txt
# 单个可疑端口快速确认
nc -w2 TARGET PORT </dev/null && echo open
# 裸 TCP 看欢迎语（最关键的判型信号）
printf "help\n" | nc -w2 TARGET PORT
```

## 4. 技术栈 → 第一反应

| 指纹 | 组件 | 第一动作 |
|---|---|---|
| `x-powered-by: PHP/5.x` | 老 PHP | 备份源码、`.phps`、上传、LFI |
| `server: envoy` / `nginx` + 静态目录 | 网关 | 试路径拼接穿越（`static..`）、头覆盖 |
| `Spring` / `Whitelabel Error` | Java | Actuator、SpEL、`/error` 泄露 |
| `X-Powered-By: Express` + `next` | Node | RSC/原型污染、`__proto__` |
| Dify / Grafana / Jenkins / HugeGraph | 中间件 | 查对应未授权 API 与 CVE |
| 裸 TCP 欢迎语/菜单 | 自定义协议 | 长度越界读 |
| 页面有 `role: developer` 徽章 | 多角色应用 | 直接打提权（改 role/cookie） |

## 5. 低成本高收益探测（一次性批量，别单条试）

```bash
for p in robots.txt .git/HEAD .env www.zip backup.zip config.php.bak .DS_Store \
         /js/login.js /api /api/config /admin /control.php /debug /preview /flag; do
  printf "%-22s " "$p"
  curl -s -o /dev/null -w "%{http_code} %{size_download}\n" -m 5 "http://TARGET$p"
done
```
**判据看 `size_download`，不只看状态码**：同样 200 但大小异常（如 293 vs 169）说明是真实目录/文件。

## 6. 输出（强制一行）

```
端口=… | 技术栈=… | 角色/鉴权形态=… | 题型=… | 首选打法=…
```

把这一行写进 `当前题工作区的 recon.md`，后续上下文压缩后还能找回。

## 7. 反模式

- ❌ 上手全端口、大字典扫描；先用题面和已知服务缩小范围。
- ❌ 逐个路径手工 curl（用上面的 for 循环批量跑）。
- ❌ 只看状态码不看响应体大小与标题。
- ✅ 连续 2 次无新信息 → 换方向。

先看题面给出的服务、源码和提示。只扫描当前题明确授权的地址与端口；出现新内网线索时配合 multi-stage。输出保存到当前题工作区。

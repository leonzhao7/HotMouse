---
name: reverse-engine
description: 逆向 / 固件 / 授权校验类题目。用于只给一个可执行文件、固件模块、授权码/序列号验证器、许可证校验器的场景，覆盖静态定位校验点、字符串与常量提取、哈希/异或/分组密码校验逆推、隐藏命令与打包数据还原、关键跳转补丁。 使用 file、strings、objdump、gdb、r2、binwalk、7z、unsquashfs。
---

# 逆向 / 固件 / 授权校验

目标：**先静态定位校验点，再逆推或直接补丁让它吐凭证。**

## 0. 起手（先静态，别急着反编译）

```bash
file ./chall && checksec --file=./chall 2>/dev/null
strings -n 6 ./chall | grep -iE 'flag|auth|key|serial|password|verify|secret|token|correct|wrong'
objdump -d -M intel ./chall | head -150
```

**命门**：`strings` 输出里的 `flag` / `correct` / `wrong` / `Invalid` 附近，就是校验点。

## 1. 校验形态 → 逆推方法

| 形态 | 识别特征 | 方法 |
|---|---|---|
| 明文字符串比较 | `strcmp` / `memcmp` / 常量就在 .rodata | 直接读常量 |
| 异或加密 | 循环里 `xor` 常量 | 提密文数组，暴力单字节异或 |
| 哈希比对（FNV/CRC/MD5） | `0x811c9dc5`(FNV)、`0xedb88320`(CRC32) | 按输入空间枚举 / 反推 |
| 分组密码（TEA/XTEA） | `0x9E3779B9` delta 常数 | 识别轮函数，提密钥解密 |
| 内部打包数据 | `decompress` / `unpack` / 自定义头 | 还原后输出凭证 |
| 隐藏命令 | 命令分发表有未公开项 | 反汇编找比较分支 |
| 自研 VM / 字节码 | 解释循环 + opcode 表 | 提取字节码，写解释器 |
| 序列号算法 | 逐位加减乘 / 查表 | 逆运算或直接补丁 |

## 2. 高频脚本

```python
# 单字节异或爆破
enc = bytes.fromhex('...')
for k in range(256):
    d = bytes(b ^ k for b in enc)
    if b'flag' in d.lower():
        print(k, d)

# FNV-1a 暴力（短输入场景）
def fnv1a(s):
    h = 0x811c9dc5
    for c in s: h = ((h ^ c) * 0x01000193) & 0xffffffff
    return h
import itertools, string
for n in range(1, 7):
    for t in itertools.product(string.printable, repeat=n):
        s = ''.join(t)
        if fnv1a(s.encode()) == TARGET:
            print('FOUND', s); raise SystemExit
```

```python
# TEA/XTEA 解密（delta = 0x9E3779B9）
def xtea_decrypt(v0, v1, key, rounds=32):
    delta = 0x9E3779B9
    s = (delta * rounds) & 0xFFFFFFFF
    for _ in range(rounds):
        v1 = (v1 - (((v0 << 4 ^ v0 >> 5) + v0) ^ (s + key[(s >> 11) & 3]))) & 0xFFFFFFFF
        s = (s - delta) & 0xFFFFFFFF
        v0 = (v0 - (((v1 << 4 ^ v1 >> 5) + v1) ^ (s + key[s & 3]))) & 0xFFFFFFFF
    return v0, v1
```

## 3. 动态手段（静态卡住时）

```bash
ltrace ./chall          # 库调用参数（比较什么、和什么比）
strace ./chall          # 打开哪些文件
gdb -q ./chall          # 在 strcmp/校验分支下断点，直接看寄存器
# 例：b *0x401234 然后 r，输入任意值，看 rsi/rdi 里的期望值
```

**对比法**：输入同一个错误值两次、再输入不同值，用 `ltrace` 看哪次调用参数变了——变化点即校验点。

## 4. 补丁（比逆推快）

```bash
# 让校验分支永真：把 jne/jz 改成 jmp 或 nop
r2 -w -q -c 'aaa; s <addr>; wx 9090; q' ./chall
# 或 objdump 定位后：
python3 -c "
d=bytearray(open('chall','rb').read())
d[OFFSET:OFFSET+2]=b'\x90\x90'
open('chall_p','wb').write(d)"
```
补丁后直接运行，让它自己打印凭证。

## 5. 固件常见套路

```bash
binwalk -e firmware.bin          # 解包
strings -n 8 firmware.bin | grep -iE 'pass|key|admin|flag|ssh'
# 找硬编码密钥/证书
grep -aE 'BEGIN (RSA|OPENSSH|CERTIFICATE)' -A3 firmware.bin
```
交叉引用字符串定位授权函数；MCU 固件找常量表与比较循环。

## 6. 自研 VM / 混淆时的取舍

1. 先**动态喂输入看输出**，判断是否可当黑盒绕过
2. 能补丁就补丁，别硬还原 VM
3. 必须还原时：提 opcode 表 → 写一个 Python 解释器 → 跑内置字节码

## 7. 反模式

- ❌ 一上来完整反编译大二进制 → 先 `strings | grep` 定位热点。
- ❌ 死磕复杂 VM → 先动态、再补丁、最后才还原。
- ❌ 在思维链里手算算法（尤其密钥碰撞）→ **必须写脚本自动化**。
- ❌ 远程题只本地跑通就交（环境差异）。

## 8. 输出

`校验算法（一句话）+ 正确的输入/序列号 或 直接输出的凭证`。

补丁前复制原文件，保存原始哈希与修改位置。补丁只能证明本地控制分支；若题目要求远程输入或 flag，仍须对真实目标验证。

固件解包只使用镜像已有的 binwalk、7z、unsquashfs；提取失败时先用 file、strings、objdump 或 r2 分析，不在赛时下载提取器。

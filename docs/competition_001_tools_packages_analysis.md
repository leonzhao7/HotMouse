# competition_001 下载工具与 Python 包分析

根据留存执行记录，比赛中确认新增并实际使用的工具只有 **UPX 4.2.4**。附件和工具下载主要使用已有的 **curl**。**没有发现 Python 包安装成功的记录**；两次安装 `pycryptodome` 都在启动安装器时失败，原因是环境缺少 `pip`。已有的 `cryptography 41.0.7` 被用于解密。

## 范围和证据口径

- 数据来自只读查询的 `backend/data/hotmouse.db`，以及对应项目的 `workers/*/.hotmouse/pi-sessions/*.jsonl`。比赛时间为 2026-11-10 01:50:48–07:50:49 UTC。
- `competition_001` 共 40 个比赛条目，创建了 proj_001–proj_029 共 29 个项目，另 11 个条目没有创建项目。29 个项目的会话均已检查；实际工具调用只出现在 proj_001–proj_022。
- 共核对 636 次工具调用：497 次 `bash`、107 次 `read`、29 次 `write`、3 次 `edit`。这些是模型调用接口；下载器、解包工具和 Python 包是另一层依赖。
- “下载成功”“安装成功”“原有可用”“仅检查过”分别依据执行返回、留存文件、导入结果和命令内容判断。脚本含有 `import` 或系统包目录中存在文件，不直接等同于该包被实际使用。
- [证据索引 CSV](competition_001_tools_packages_evidence.csv) 保留项目、时间、状态、摘要和原始日志位置，不复制凭证、题目内容或 flag。下文 E01–E20 对应该索引。

## 下载与解包工具

| 工具 | 赛中状态 | 用途与观察 |
|---|---|---|
| `curl` | 已有并实际使用 | 获取附件、网页、比赛 API、UPX 发布包及题目配套运行库。前 22 个项目均有相关执行命令；其中也包含连通性探测和提交，不能把每条 curl 命令都计为附件下载。 |
| `tar` | 已有并实际使用 | 解开 UPX 的 `.tar.xz` 发布包，见 E04。 |
| `unzip` | 已有并实际使用 | 解开比赛 ZIP 附件。赛中盘点显示版本为 UnZip 6.00。 |
| `file`、`sha256sum` | 已有并实际使用 | 下载后识别文件类型、记录摘要；它们负责校验和分类。 |
| Python `http.client`、`urllib.request` | 标准库，有 HTTP 调用记录 | proj_004 使用它们访问题目 Web API。它们无需 pip 安装；主要附件获取仍由 curl 完成。 |
| `wget` | 检查时存在，未见实际下载命令 | proj_014 的盘点返回 `/usr/bin/wget`，见 E13。 |
| `apt-get download upx-ucl` | 尝试过，未见下载成功证据 | proj_003 加了 8 秒限时；返回没有确认成功的 apt 输出，未发现对应留存 `.deb`，随后改用 GitHub 发布包，见 E03。 |

没有发现通过 `aria2c`、`git clone`、`requests.get` 下载资源的执行记录，也没有发现赛中 `apt install`、`npm install`、`go install`、`cargo install` 安装工具的执行记录。

### 实际新增工具：UPX

| 项目 | 内容 |
|---|---|
| 项目/题目 | proj_003 / g-12 |
| 工具 | UPX 4.2.4，Linux amd64 原生可执行文件 |
| 来源 | [UPX 官方 GitHub 发布包](https://github.com/upx/upx/releases/download/v4.2.4/upx-4.2.4-amd64_linux.tar.xz) |
| 下载时间 | 02:25:52 UTC，见 E04 |
| 下载及解包 | `curl -sSL -o upx.tar.xz …`，随后 `tar -xf upx.tar.xz` |
| 压缩包大小 | 590,272 字节 |
| 压缩包 SHA-256 | `75cab4e57ab72fb4585ee45ff36388d280c7afd72aa03e8d4b9c3cbddb474193` |
| 留存目录 | `backend/data/workspaces/hotmouse/proj_003/workers/worker-f6c9bc99e3f39505/tools/` |
| 实际使用 | 02:28:14 UTC 执行 `./upx -d packed -o …/packed_unpacked`，见 E05 |
| 结果 | 返回 `Unpacked 1 file.`，解包后的 ELF 为 22,867 字节 |

UPX 下载与解包整条命令约用 0.81 秒，脱壳命令约用 0.12 秒。前面的环境盘点、获取工具和后面的继续分析跨了多个模型回合；本次 UPX 下载本身没有表现出明显耗时问题。UPX 是原生工具，前面尝试 `import upx` 无法替代这个可执行文件。

proj_020 还下载了题目匹配的 `libc.so.6` 和 `ld-linux-x86-64.so.2`，并调用加载器进行本地验证，见 E19。它们属于题目运行环境依赖；题目 ELF、ZIP、PCAP、Verilog 和源代码则属于附件。

## Python 包

赛中使用 Python 3.12.3。下面列出与执行、求解或能力检查有关的第三方包，不是系统 Python 的全量包清单。

### 原有可用并使用的包

| 包 | 版本 | 实际用途与证据 |
|---|---|---|
| `cryptography` | 41.0.7 | proj_010 在缺少 `Crypto` 后走 AES 解密替代实现，最终脚本运行成功，见 E07、E10；proj_011 的 RSA/OAEP 解密脚本使用该包并运行成功，见 E09、E11。 |
| `cryptography` | 41.0.7 | proj_015 的 `ChaCha20Poly1305` 加解密自测返回 `roundtrip: True`，见 E14；这是能力检查，不能据此认定题目密钥或真实密文已经恢复。proj_017 也确认该包能够导入，见 E16。 |

### 赛中安装尝试

| 时间 UTC | 项目 | 目标包 | 实际执行 | 结果 |
|---|---|---|---|---|
| 03:25:23 | proj_011 | `pycryptodome` | `pip install pycryptodome` | `/bin/bash: … pip: command not found`，未启动包下载或安装，见 E09。 |
| 05:54:07 | proj_017 | `pycryptodome` | `timeout 90 pip install --no-input pycryptodome` | `timeout: failed to run command ‘pip’: No such file or directory`，后续仍无法导入 `Crypto`，见 E17。 |

proj_003、014、015、017 的 `python3 -m pip --version` 也返回 `No module named pip`。没有发现赛中创建 venv、执行 ensurepip、安装 pip 引导脚本、下载 Python wheel 或成功安装包的记录。

这里的失败发生在安装器启动阶段，不能归因为 PyPI 包下载慢。部分命令把输出接到 `tail` 或在后面继续执行其他命令，工具外层的 `isError: false` 仍可能出现；必须查看实际错误文本，不能把这个字段当成安装成功。

### 确认缺失的第三方包

共确认以下 18 个包对应的模块不可用，没有后续安装成功证据。

| 包名 | 导入名 | 确认缺失的项目 | 对应能力 |
|---|---|---|---|
| `pycryptodome` | `Crypto` | 010、011、015、017 | AES、RSA、ChaCha20 等；两次安装失败。 |
| `sympy` | `sympy` | 007、010、011 | 素性检查、符号及数论运算，见 E06–E08。 |
| `gmpy2` | `gmpy2` | 011 | 大整数和数论加速，见 E08。 |
| `fpylll` | `fpylll` | 011 | 格算法，见 E08。 |
| `pyaes` | `pyaes` | 010 | AES 替代实现，见 E07。 |
| `PyNaCl` | `nacl` | 015 | libsodium 相关能力。 |
| `capstone` | `capstone` | 014、015 | 反汇编，见 E13、E15；003 也检查过，但输出截断。 |
| `pyelftools` | `elftools` | 014、015 | ELF 结构解析，见 E13、E15；003 也检查过，但输出截断。 |
| `pwntools` | `pwn`、`pwnlib` | 014、015 | 二进制交互及利用辅助，见 E13、E15。 |
| `scapy` | `scapy` | 017 | 报文解析，见 E16。 |
| `dpkt` | `dpkt` | 017 | PCAP/协议解析，见 E16。 |
| `unicorn` | `unicorn` | 014 | 指令模拟，见 E13。 |
| `keystone-engine` | `keystone` | 014 | 指令汇编，见 E13。 |
| `angr` | `angr` | 014 | 符号执行，见 E13。 |
| `lief` | `lief` | 014 | 可执行文件解析，见 E13。 |
| `r2pipe` | `r2pipe` | 014 | radare2 Python 接口，见 E13。 |
| `pefile` | `pefile` | 014 | PE 文件解析，见 E13。 |
| `z3-solver` | `z3` | 014 | SMT 约束求解，见 E13。 |

这些包中的一部分只被检查过，并未进入实际求解。例如 proj_014 的 11 个模块检查有 `pwn`、`pwnlib` 两个导入名，但对应同一个 `pwntools` 包。不能把模块数量直接当成包数量。

`socket`、`struct`、`hashlib`、`base64`、`zlib`、`zipfile`、`urllib`、`http.client` 等属于 Python 标准库，无需另外安装。留存执行主要使用这些标准库和已有命令行工具完成下载后的解析、计算及网络交互。

## 对求解流程的影响与环境建议

- **优先补齐 Python 安装器。** 比赛中反复确认缺少 pip，却再次发起依赖 pip 的安装命令。赛前准备一个固定 Python venv，并验证 `python -m pip --version` 和常用包的实际导入，可以减少这些无效回合。
- **按已出现的缺口准备常用包。** 建议优先准备 `pycryptodome`、`sympy`、`gmpy2`、`pyelftools`、`capstone`、`pwntools`、`dpkt`、`scapy`，并验证 `cryptography` 可用。`fpylll`、`z3-solver`、`angr` 等根据题型单独准备；仅被盘点过的包不必全部作为默认依赖。
- **准备原生工具，减少临场获取。** 现有 `file`、binutils、`unzip`、`strace`、Go 工具链和 `tcpdump` 可用。UPX 可以预先准备；赛中盘点还显示 `gdb`、radare2/rizin、Delve 等不可用，proj_017 没有找到 `tshark`。这些工具需要独立于 Python 包配置，见 E12、E13、E18、E20。
- **工具缺失后尽快采用已验证的替代实现。** proj_010、011 利用已有 `cryptography` 完成了解密；proj_017 在缺少 Scapy、dpkt 时使用 tcpdump 和标准库手工处理流量。缺包增加了实现负担，但留存证据不足以把任何题目失败单独归因为缺包。
- **下载后先验证类型和结果。** 留存附件入口有 ZIP 包，即使 URL 或本地文件名看似 `.sh` 等单文件，也应先检查 HTTP 状态、`file` 和压缩包目录。安装或解包后做一次最小能力验证，并保存结果供后续步骤复用。

## 分析时的环境快照

分析时另行只读检查了当前环境，防止把现在可用的包反推为赛中安装成功：

| 环境 | 检查时确认状态 | 与比赛记录的关系 |
|---|---|---|
| 系统 `/usr/bin/python3` | Python 3.12.3；`cryptography 41.0.7`、`requests 2.31.0`、`pip 24.0` 检查时存在 | `cryptography` 有赛中证据。赛中多次明确缺少 pip；分析时存在 pip 不能改变该结论。执行记录未见 requests 用于下载。 |
| 仓库根目录 `.venv` | 禁用系统 site-packages；检查时第三方包元数据仅有 `pip 24.0` | `pyvenv.cfg` 修改时间为比赛结束后的 15:15:53 UTC，没有赛中使用该 venv 的证据。 |
| 比赛项目目录 | 未发现留存的 `pyvenv.cfg`、Python wheel 或 `.dist-info/METADATA` | 与没有项目内包安装成功记录相符，但不能用目录缺失单独证明历史上从未发生安装。 |

本次仅新增分析文档和证据索引，没有安装任何工具或 Python 包，也没有修改比赛数据与项目工件。

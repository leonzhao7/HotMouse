# competition_003：新增资源作用与 Dispatcher 并行分析

范围为排除 g-26 整场截断及未开始题后的 27 个有效项目。本次只读审计会话和比赛数据，另运行既有调度测试，没有修改业务实现或比赛配置。

## 新增 skills 和 pentest_toolkit 的实际作用

| 资源 | 主动使用 | 覆盖题目 | 可观察结果 |
|---|---|---|---|
| 新增专项 SKILL.md | 4 次读取：Dispatcher 3 次、Worker 1 次，均为 code-exec | g-20、g-36，2/27 | g-36 成功，g-20 失败；成功不能全部归因于 skill |
| pentest_toolkit | 9 条含检索的 bash 命令，均由 Worker 发起 | g-37，1/27 | 3 条超时，没有找到可用的 CVE-2022-39227 方法，未见执行 toolkit 脚本 |

逐次调用、角色、原始会话路径及行号保存在 [competition_003_resource_usage.csv](competition_003_resource_usage.csv)。专项文件被导出到 worker 的 `.hotmouse/knowledge/<version>/skills/`，所以不能只搜索 backend/skills 字面路径。

g-36 的 Dispatcher 同时读取新增 code-exec 和旧 kb047，再规划沙箱探测与文件读取。第二个执行步骤通过 `print(open('flag.txt').read())` 成功。旧 kb047 也提供直接 open 读取方法，能确认新增 skill 参与规划，不能确认它提供了独有方法。003 用时 543.34 秒（9.06 分钟），002 为 373.34 秒（6.22 分钟），没有观察到该题提速。

g-20 的 Dispatcher 两次、Worker 一次读取同一个 code-exec；仍大量重复白名单与连接协议探测，未完成逃逸。它缺的是把可靠字符限制转为可验证表达式构造，不是再读一份通用沙箱说明。

g-37 反复检索 python-jwt、39227 等词，命中包括与目标无关的 Exploit-DB 编号 39227。9 条包含 toolkit 检索的命令，从调用到记录结果的跨度合计 189.69 秒；其中部分还执行网络请求、全盘找包及旧 kb009 读取，不能全部算作 toolkit 独占耗时。找出的 python_jwt 源码在旧项目和 /tmp，而非 toolkit；没有看到检索输出转成成功利用链。

上述统计不等于只有两题受到 skill 提示影响：新增 flag-hunting 总纲直接注入执行提示。26 个实际执行项目的 60 份初始规划会话提示、53 份初始执行会话提示都含新增索引及 toolkit 说明，53 份执行提示都含 flag-hunting 全文。g-07 停在分类，没有进入这些阶段。

逐段统计：执行提示的专项索引、flag-hunting 及运行时适配约 8,373 字符，toolkit 说明约 2,260 字符，合计 **10,633 字符**；规划提示合计约 7,139 字符。补充将完整首轮执行提示的全部 26 道相同题配对：002 中位 20,715.5 字符，003 中位 40,997.5 字符，新增两套资源约占 003 首轮输入的 26%。这是字符统计，不是 token 或响应时延的因果测量。

新增专项也没有独立密码学或抓包分析 skill，难以直接补上 g-33、g-16、g-29 的算法缺口。日志支持“参与了一道成功题的规划、直接使用范围窄、上下文成本明确”，不支持“带来了新增成功题”或“完全没有帮助”。应保留资料，压缩公共索引与路径说明，只注入匹配片段；建立精确组件/CVE/版本索引，并优先召回经过平台接受结果验证的算法。

接入代码：[knowledge.go](../backend/internal/worker/knowledge.go)、[toolkit.go](../backend/internal/worker/toolkit.go)。原有 hotmouse-ctf、hotmouse-dispatcher 和 kbNNN 条目不计作这次新增资源。

## 规划与 Worker 并行的准确语义

当前服务的 Runner.Start 为各项目 Dispatcher 和 AgentWorker 分别启动循环。规划租约不会阻止 Worker 领取已创建的 Step。Step 的 from 引用创建时已经存在的 Fact，不是未来 Step；Worker 领取还受占用、恢复状态、路线状态及项目 worker 上限约束。

一般结果的链路为：

```text
工具输出 → Worker 结束步骤并保存结果/恢复会话 → 释放 Worker
    → Dispatcher 提交（必要时先总结）→ ConcludeStep 写入 Fact/事件 → 后续规划
```

若已有另一条可执行 Step，Worker 可以在 Dispatcher 总结和规划时继续执行它；若下一动作尚未规划，或依赖未提交事实，则要等待。工具输出、解释文字、工作区文件里的“发现”不会自动进入事实图。当前没有一般事实的执行中增量上报通道。未带 finding 的 no_information/unclassified 结果也不会仅凭创建 Fact 强制触发规划。

003 的 g-37 实际时间（UTC）：

| 任务 | 时间 |
|---|---|
| Worker 执行 s002 | 11:14:12.518–11:19:13.023 |
| Dispatcher 总结并提交 s001 | 11:14:13.156–11:15:15.126 |
| Dispatcher 根据 s001 结果规划 | 11:15:15.143–11:18:51.034 |

27 个有效项目规划任务累计 **95.60 分钟**，与同项目 Worker 执行重叠 **18.55 分钟，19.4%**。明显重叠只有 g-37、g-21、g-04；g-08 另有 15 毫秒边界重叠。其余 77.05 分钟没有 Worker 同时执行，与主报告按执行优先统计的规划阶段一致。

同一 Dispatcher 的总结/提交和模型规划仍串行；它等待规划模型期间，新普通结果通常要等本轮返回才能提交。运行中的 Worker 和规划模型都使用启动时上下文快照。规划提交会保护期间已领取、已结束、有恢复记录的步骤，跳过其更新；本轮规划只确认启动时已读取的事件，后续事件保留。

flag 有专用通道：Worker 主动 checkpoint 后，后台每秒轮询候选文件，验证原始来源并创建候选 Fact/flags，协调器可直接提交。CheckpointFlag 明确不新建规划事件，避免同一答案交付期间重复规划；平台结果后续仍可产生事件。g-36 使用了 checkpoint，g-22 没有使用。

因此，当前有“规划与执行并行”，但没有“发现任意事实后实时规划并继续执行”的完整通道。建议支持结构化进展上报，保留原始证据、确认结论与待验证假设，让会话继续推进；Dispatcher 合并事件，只在解锁依赖、路线变化、队列不足时规划。有效结果的确定性提交应与模型总结分离，减少长规划、长总结占住事实提交路径。

代码位置：[runner.go](../backend/internal/worker/runner.go) 的 startDispatchersAndWorkers、executeNext、executeAsRole、dispatchProject；[plan.go](../backend/internal/core/plan.go) 的 ApplyDecisionPlan；[strategy.go](../backend/internal/core/strategy.go) 的 recordResultTx；[flag_checkpoint.go](../backend/internal/core/flag_checkpoint.go) 的 CheckpointFlag；[commit_schedule.go](../backend/internal/worker/commit_schedule.go) 的 pendingResultsEventGuard。

## 验证边界

三个既有测试为 TestWorkerClaimsDuringPlanningAndSurvivesAbandonment、TestFailedSummaryYieldsToIndependentStepsAndSuccessfulCommits、TestDeferredSummaryDoesNotInvokeDecideWithoutCommittedFacts。默认解释器下并行测试两次停在等待 planner 启动的 3 秒阈值，另两个通过；明确使用 `/usr/bin/python3` 后三个全部通过，耗时 3.43 秒。默认路径会发现工作区 .venv 并执行能力探测，既有后台目标测试也注明冷缓存准备可能超过 3 秒。没有单独测量默认失败的全部根因，不把它当作已证实的调度死锁，也不宣称默认环境回归通过。

没有访问靶场、重新提交 flag 或更改运行配置。资源的增量收益与模型延迟还需要同题、同请求 A/B，不能由这次日志审计单独证明。

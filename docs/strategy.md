# 紧凑上下文、路线调度与经验台账

已实现改进建议末尾的前五项。数据库在服务启动时自动迁移；既有事实、步骤、Finding 与恢复检查点继续可用。项目工作区新增“路线与止损”“历史检索”“经验库”“flag 台账”，手动提交结论可以选择结果类型并保存详细证据。

## 模型上下文与完整导出

Decide、Execute 和中断后的总结使用专门的紧凑 YAML。Decide 包含活动目标、开放步骤及稳定 ID、必要事实、近期摘要、路线预算与待处理事件；Execute 包含当前步骤、来源事实、关联目标及父目标、相关历史和少量经验。目标描述与必需的事实描述不截断，完整日志和 `Finding.data` 不默认注入模型；事实摘要通过 `finding_ids` 保留相关详细证据的入口。

模型上下文中的 `detail_access` 给出按 ID 读取事实、步骤、Finding 和经验的入口。开放步骤的 `can_update` 与 `recovery_status` 明确区分可以修改的任务和正在恢复的执行。跨项目经验最多召回三条，来源项目中的事实 ID 不能用作当前项目的依赖或完成证据。

```bash
hotmouse --project proj_001 project context --phase decide
hotmouse --project proj_001 project context --phase execute --step s001
hotmouse --project proj_001 fact show f001
hotmouse --project proj_001 step show s001
hotmouse --project proj_001 finding show v001
hotmouse --project proj_001 project export --format yaml
```

`GET /projects/{project}/context?phase=execute&step_id=s001` 返回 `{phase, yaml}`。`GET /projects/{project}/export?format=yaml` 继续返回完整图 YAML，包含详细证据、步骤 ID、结构化结果、路线与止损记录、flag 与提交尝试。

命令适配器的 `HOTMOUSE_MODEL_CONTEXT_YAML` 是紧凑上下文；原 `HOTMOUSE_GRAPH_YAML` 保留为它的兼容别名。需要旧完整图结构的自定义适配器应调用导出接口。`HOTMOUSE_WORKER_PROMPT` 已改为使用紧凑上下文。项目授权和范围 YAML 的合并机制继续生效；图、目标输出和检索内容作为数据使用。

Worker 产物共享采用目录和提示词约定：每个 Worker 将全部新增数据、脚本、证据和输出保留在自己的项目子目录，浏览项目目录复用其它 Worker 的遗留文件，并保持这些文件不变。命令沿用宿主文件系统权限，HotMouse 不添加文件系统写权限隔离或 bubblewrap 依赖。`writable_directory` 表示当前 Worker 应存放产物的目录。

规划上下文不提供 Dispatcher 的 `writable_directory`，而以 `step_output_directory: ${HOTMOUSE_WORKSPACE_DIR}` 表示尚未分配的执行目录。新增输出、解压目录和脚本路径在步骤描述中保留这个占位符，例如 `${HOTMOUSE_WORKSPACE_DIR}/zip_list.txt`；执行时只在当前步骤描述及其 `HOTMOUSE_STEP_DESCRIPTION` 中替换为实际 Worker 目录。图中已记录的事实和其它 Worker 的证据读取路径保持原样。执行提示要求旧步骤中指向其它目录的新增输出改存当前 Worker 的目录，并报告实际绝对路径；恢复总结只引用原始证据路径，不重新执行或迁移产物。

## 结构化执行结果

执行结果独立于漏洞 Finding：

| `result.type` | 含义与调度处理 |
| --- | --- |
| `progress` | 获得新事实或阶段成果，清零连续无信息次数，触发 Decide |
| `negative` | 已确认的目标侧负面证据，清零连续无信息次数，触发 Decide |
| `blocked` | 目标侧阻碍，触发 Decide 寻找替代方向 |
| `environment_failure` | 工具、模型或执行环境问题，单独计数并触发 Decide，不作目标完成证据 |
| `no_information` | 没有新增信息，累积到路线阈值后止损并触发 Decide |
| `needs_attention` | 当前步骤无法安全继续，保存证据并搁置路线；自动规划寻找其他路线，没有可执行路线时项目失败 |
| `unclassified` | 兼容旧客户端或纯文本输出，不解释为进展，计入无信息次数 |

```json
{
  "accepted": true,
  "data": {
    "title": "登录参数确认",
    "description": "已确认参数过滤，当前方法不适用",
    "result": {
      "type": "negative",
      "detail": "完整请求、回复和验证笔记",
      "evidence": {"response_code": 400}
    }
  }
}
```

原 `description` 与可选 `finding` 协议仍然接受。`title` 为可选结果标题，`result.detail` 与 `result.evidence` 保存在结果记录中，可通过详情接口读取；事实额外保存标题、详细笔记和结果类型。环境失败、无信息和无法继续的步骤不能同时断言 Finding，也不能作为 Goal 完成证据。Worker 进程失败保留检查点，不伪造目标事实。

CLI 可通过完整请求 `--file` 提交嵌套结果，或使用：

```bash
hotmouse --project proj_001 step conclude s001 \
  --worker local --description '已确认过滤' --title '参数确认' \
  --result-file result.yaml
```

## 事件触发的 Decide

新进展、负面结果、目标阻碍、环境故障、目标完成、Finding、提示、flag 平台结果、路线止损和人工请求会产生持久化事件。Dispatcher 优先提交已经保存的成功结果，然后处理尚未失败的总结。总结失败时，按步骤保存退避时间，先推进已有事实支持的独立步骤和由新结果触发的规划，再轮流重试失败的总结；正在执行的独立步骤也不会被总结重试占用。单纯中断且没有提交结果时，不因为总结被延后而额外调用 Decide；原有事件和图未变化检查继续生效。依赖仍通过已存在的 Fact 表达，不能用 Step 顺序或优先级代替依赖。

Decide 可以通过 `step_updates` 调整优先级或放弃尚未领取的步骤，通过 `route_updates` 修改路线状态、预算和复核记录。整批图修改在同一事务中执行；一项无效更新会回滚整批。已领取、已结束以及带恢复检查点的步骤不能修改。路线搁置阻止新的执行领取，已经执行的结果及恢复总结可以继续提交。

```json
{
  "accepted": true,
  "data": {
    "step_updates": [{"id": "s002", "action": "abandon"}],
    "route_updates": [{
      "id": "route_g001", "status": "shelved",
      "reason": "前提已被推翻", "reopen_condition": "获得新的独立证据",
      "next_direction": "复核另一条路线"
    }]
  }
}
```

成功规划只确认调用开始时已看到的事件；规划过程中到达的新事件保留到下一轮。模型拒绝、非法计划和进程故障不会丢弃事件，现有失败冷却继续生效。Mock 只提供确定性的演示执行，不进行模型重规划。

```bash
hotmouse --project proj_001 decide request --reason '新证据改变前提'
```

## 路线累计投入与止损

新步骤可以指定 `route_id`。未指定时继承来源步骤的路线和目标；没有可继承路线时按目标建立默认路线。需要同一目标的多条独立路线时，先创建路线，再用返回 ID 创建步骤。人工为未领取步骤改关联目标时，步骤切换到新目标的默认路线，既有投入记录保留。

```bash
hotmouse --project proj_001 route create --title '登录接口路线' --goal g001 \
  --budget-seconds 900 --no-progress-limit 3
hotmouse --project proj_001 step create --from origin --description '检查登录接口' \
  --goal g001 --route r001
hotmouse --project proj_001 route list
hotmouse --project proj_001 route records
```

默认预算为 1200 秒，默认连续无信息上限为 3；`budget_seconds=0` 取消时间限制。预算与阈值均可调整。每次执行和恢复尝试按持久化工作历史单独计时，每个历史 ID 只累计一次。日志过期清理不清空累计投入；旧库中的已结束执行历史会补计。异常中断的尝试按最后保存的活动时间结算，服务离线时间不计入预算。

达到时间预算、连续无信息阈值或步骤无法安全继续时，程序保存搁置原因、重新开启条件与下一方向，阻止领取该路线的新步骤。Dispatcher 会寻找其他可执行路线；当活动目标仍未完成且没有可执行工作时，项目进入失败终态。正在执行的步骤不会被规划更新抢占，单次超时边界继续由 WorkerRuntime 控制；累计预算在尝试结束或恢复对账后检查。

重新开启须说明原因，耗尽的预算须先提高或明确取消限制。无信息次数在重新开启时清零，历史投入和止损记录保留。因 `needs_attention` 搁置的路线不会被自动重开，以免重复执行已失败动作；自动规划继续尝试独立路线，没有其他可行路线时项目失败。

```bash
hotmouse --project proj_001 route update r001 --status active \
  --reason '人工复核后采用新方法' --budget-seconds 1800 \
  --reopen-condition '已有新的验证依据' --next-direction '确认新前提'
```

## 历史检索与跨项目经验

当前项目的事实、步骤、Finding 及详细证据可按关键词检索，返回摘要、结果类型、详情标记和匹配分数。经验库独立保存方法、适用条件、失败条件、标签和来源项目/步骤/事实；通过人工整理或 API 写入，模型仅按当前任务召回少量匹配摘要。详情按需读取。

第一版使用 SQLite 保存记录和关键词匹配：英文按词匹配，中文按字拆分，标题权重更高，支持标签过滤。未引入向量服务。

```bash
hotmouse --project proj_001 project recall --query '登录 过滤' --limit 5
hotmouse experience create --title '登录参数验证' --conditions '存在可比较的登录回复' \
  --method '对比已保存的有限请求和回复' --failure-conditions '会话状态不一致' \
  --tags web --source-project-id proj_001 --source-step-id s001 --source-fact-ids f001
hotmouse experience search --query '登录' --tag web
hotmouse experience show 1
```

经验中的地址、凭据、flag 和服务状态不能作为新项目的已验证事实，必须重新确认。

## Flag 台账

flag 按项目、题目编号和值去重，引用来源事实。`kind=flag` 的 Finding 如含 `data.flag` 或 `data.value` 与来源事实会自动进入台账；旧库中符合这些条件的 Finding 也会迁移。台账包含独立的提交尝试和原始平台回复。

```text
captured → submission_pending → accepted / rejected / submission_unknown
```

```bash
hotmouse --project proj_001 flag capture --value 'flag{example}' --fact f001 --challenge-id task-1
hotmouse --project proj_001 flag list
hotmouse --project proj_001 flag begin 1
hotmouse --project proj_001 flag resolve 1 1 --status submission_unknown --response '请求超时，等待核对'
hotmouse --project proj_001 flag resolve 1 1 --status accepted --response '核对后平台确认接受'
```

`begin` 只登记提交尝试，便于外部平台适配器在发请求之前持久化；这一轮没有新增真实比赛平台的提交适配器。待提交或结果未知时拒绝创建另一尝试，需核对原尝试。已接受的结果不允许盲目重提或覆盖；明确拒绝后可创建新的尝试。

## 新接口

| 接口 | 用途 |
| --- | --- |
| `GET /projects/{p}/context` | 紧凑模型 YAML，参数 `phase`、`step_id` |
| `GET /projects/{p}/facts/{id}`、`steps/{id}`、`findings/{id}` | 完整记录详情 |
| `GET /projects/{p}/history?q=...&limit=5` | 项目历史检索 |
| `GET /projects/{p}/routes`、`POST /projects/{p}/routes` | 路线列表、创建 |
| `PATCH /projects/{p}/routes/{id}` | 路线状态与预算调整 |
| `GET /projects/{p}/route-records` | 止损与复核记录 |
| `GET /projects/{p}/decide/events`、`POST /projects/{p}/decide/request` | 待处理事件、人工触发 |
| `GET /api/experiences?q=...&tag=...&limit=5`、`POST /api/experiences` | 经验检索、创建 |
| `GET /api/experiences/{id}` | 经验全文 |
| `GET /projects/{p}/flags`、`POST /projects/{p}/flags` | 台账列表、捕获 |
| `POST /projects/{p}/flags/{f}/attempts` | 登记提交尝试 |
| `PATCH /projects/{p}/flags/{f}/attempts/{a}` | 保存或核对平台结果 |

## 验证

后端测试覆盖完整导出与紧凑视图、必要事实保留、结果类型与完成证据、事件调度、事务回滚、领取与恢复保护、累计耗时、止损与重开、日志清理/重启/旧库迁移、项目检索隔离、经验召回和 flag 去重/未知状态核对。浏览器测试覆盖新界面流程及原有工作台操作。

```bash
cd backend
go test -race ./...
cd ../frontend
npm test
npm run build
npm run test:e2e
```

尚未使用真实题集与模型进行成功数、token、有效耗时和重复动作次数的前后对比；这些指标仍需固定题集验收。

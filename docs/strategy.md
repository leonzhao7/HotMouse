# 紧凑上下文、路线调度与经验台账

已实现改进建议末尾的前五项。数据库在服务启动时自动迁移；既有事实、步骤、Finding 与恢复检查点继续可用。项目工作区新增“路线与止损”“历史检索”“经验库”“flag 台账”，手动提交结论可以选择结果类型并保存详细证据。

## 模型上下文与完整导出

Decide、Execute 和中断后的总结使用专门的紧凑 YAML。Decide 包含活动目标、开放步骤及稳定 ID、全部历史步骤、必要事实、近期摘要、项目共享预算与待处理事件；Execute 包含当前步骤、来源事实、关联目标及父目标、相关历史和少量经验。目标描述与必需的事实描述不截断，完整日志和 `Finding.data` 不默认注入模型；事实摘要通过 `finding_ids` 保留相关详细证据的入口。

Decide 的 `step_history` 保留当前项目所有步骤的完整动作描述、来源事实、状态和结果类型，包括待执行、执行中、已结束、已放弃及恢复中的步骤。规划提示要求比较实际动作、目标、方法与输入，避免通过改写描述、切换目标或路线重复工作，优先复用既有证据。计划入库还会检查当前项目全部历史和同批新步骤，拒绝忽略大小写与空白后描述相同的步骤，并回滚整批计划。

模型上下文中的 `detail_access` 给出按 ID 读取事实、步骤、Finding 和经验的入口。开放步骤的 `can_update` 与 `recovery_status` 明确区分可以修改的任务和正在恢复的执行。跨项目经验最多召回三条，来源项目中的事实 ID 不能用作当前项目的依赖或完成证据。

`related_knowledge` 另行召回最多三条内置 CTF 方法摘要，并通过 `detail_access` 和 Worker 工作区路径提供全文。规划和执行分别加载适配自 sapper-v2 的 Dispatcher、CTF 技能；知识方法须基于当前证据验证，KB ID 不能用作项目依赖或完成证据。原始条目、技能、离线 CLI 与迁移范围见 [knowledge.md](knowledge.md)。

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
| `no_information` | 没有新增信息，累积到路线阈值后搁置路线并直接将项目标记为 failed，不再触发 Decide |
| `needs_attention` | 当前步骤无法安全继续，保存证据并搁置路线；项目直接失败，不再自动规划 |
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

新进展、负面结果、目标阻碍、环境故障、目标完成、Finding、提示、flag 平台结果和人工请求会产生持久化事件；路线被搁置时直接令项目失败，不再请求 Decide。Dispatcher 优先提交已经保存的成功结果，然后处理尚未失败的总结。总结失败时，按步骤保存退避时间，先推进已有事实支持的独立步骤和由新结果触发的规划，再轮流重试失败的总结；正在执行的独立步骤也不会被总结重试占用。单纯中断且没有提交结果时，不因为总结被延后而额外调用 Decide；原有事件和图未变化检查继续生效。依赖仍通过已存在的 Fact 表达，不能用 Step 顺序或优先级代替依赖。

Decide 可以通过 `step_updates` 调整优先级或放弃尚未领取的步骤，通过 `route_updates` 修改路线状态、无进展阈值和复核记录。项目时间预算由操作者配置。图修改在同一事务中执行；步骤在规划期间被领取、结束或进入恢复后，其更新会被跳过，其余无效修改仍回滚整批。路线搁置是终止项目的操作：提交搁置记录与项目失败状态后，不再应用同份计划中的后续步骤或目标修改，也不继续规划。

```json
{
  "accepted": true,
  "data": {
    "route_updates": [{
      "id": "route_g001", "status": "shelved",
      "reason": "前提已被推翻", "reopen_condition": "获得新的独立证据",
      "next_direction": "复核已保存证据后人工恢复"
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
  --no-progress-limit 3
hotmouse --project proj_001 step create --from origin --description '检查登录接口' \
  --goal g001 --route r001
hotmouse --project proj_001 route list
hotmouse --project proj_001 route records
```

系统设置中的 `default_project_budget_seconds` 控制新项目默认预算，初始值保持 1200 秒，接受非负整数，`0` 表示取消时间限制。创建项目时自动读取并保存到 `project.budget_seconds`；网页创建表单留空即继承配置，CLI 的 `project create --budget-seconds` 或 API 创建请求的 `budget_seconds` 可单独覆盖。修改系统默认值只影响以后创建的项目。

所有路线共同消耗一份项目时间预算，新增路线不会增加额度。时间消耗由各路线的执行投入汇总，不按项目创建后的自然时间计算；并行执行按各次尝试的耗时相加。项目详情中的 `budget_spent_ms` 表示本轮已消耗额度，`invested_ms` 表示全部历史投入。网页“路线与止损”的“修改项目预算”、`hotmouse project budget --budget-seconds` 和 `PUT /projects/{id}/budget` 可修改已有项目的额度，修改不重置累计消耗或隐式恢复项目。

路线保留累计投入和无进展计数，取消独立时间预算。路线 API、导出和模型上下文不再返回路线预算字段；创建/修改路线若仍提交 `budget_seconds` 会报错，CLI 路线命令已移除此参数。旧库的路线预算列仅作兼容读取，预算限制统一由项目控制；原项目预算、投入、止损记录与停止状态保留，迁移将旧路线基线汇总为项目基线。

默认连续无信息上限为 3，阈值仍可按路线调整。项目 `budget_spent_ms` 为所有路线累计投入减去项目 `budget_baseline_ms`，时间限制以项目本轮消耗为准。每次执行尝试按持久化工作历史单独计时，每个历史 ID 只累计一次。日志过期清理不清空累计投入；旧库中的已结束执行历史会补计。异常中断的尝试按最后保存的活动时间结算，服务离线时间不计入预算。

达到项目共享时间预算时，保存项目失败原因并停止调度。达到路线连续无信息阈值或步骤无法安全继续时，保存路线搁置记录与证据并将整个 project 标记为 failed。即使还有其他待执行或正在执行的路线，也停止项目调度，不再触发重新规划或自动切换路线。人工或模型主动搁置路线同样使项目失败。仅作废未认领的 Step 不会触发这项规则；累计预算在尝试结束或恢复对账后检查，既有单次超时仍由 WorkerRuntime 控制。

手动“继续推进”优先于旧的止损决定：从 failed 或 stopped 恢复项目时，在同一事务内重新开启全部路线、清除旧搁置原因、重置连续无信息次数，并按项目保存的预算给予一份新的共享额度。恢复前先结算旧执行历史，避免迟到的旧尝试消耗新预算。累计投入、已有结果、未完成步骤、恢复检查点和旧止损记录保留，路线记录增加 `operator_resume` 原因的重开或恢复记录。已 active 的重复恢复请求不会重置额度；完成项目通过反馈重新打开时也获得一份新的共享额度。新一轮再次达到止损条件时仍直接将项目判为 failed，不自动规划。失败项目也可单独人工调整路线或项目预算，此操作不隐式恢复项目。

停止、继续推进和通过反馈重新打开均保留已有目标识别结果与初始预处理证据。紧凑模型上下文通过 `target_analysis` 提供已保存的识别结果，恢复后从已有事实和产物继续规划，不重新分析初始事实或识别已知题目目标。旧版本曾清空识别结果的项目，会从历史中恢复最近一次成功识别。尚未完成首次识别的项目仍须完成首次识别。

```bash
hotmouse --project proj_001 route update r001 --status active \
  --reason '人工复核后采用新方法' \
  --reopen-condition '已有新的验证依据' --next-direction '确认新前提'
hotmouse --project proj_001 project resume
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

## 远程 CTF 目标探活

项目启动后，Dispatcher 首先以 `HOTMOUSE_TASK_TYPE=target_probe` 请求 LLM 分析项目来源、目标、提示及授权范围，判断是否为需要探活的远程 CTF，并提取目标 IP/域名和 TCP 端口。分析完成前不进行常规规划或自动执行，即使项目已有人为创建的步骤。原生模型驱动在该阶段只允许读取工具；连接检查由 HotMouse 的独立 Go 协程执行。自定义命令适配器须支持这个新任务类型：

```json
{
  "accepted": true,
  "data": {
    "required": true,
    "reason": "远程 CTF 题目服务",
    "targets": [{"host": "ctf.example", "port": 31337}]
  }
}
```

本地文件题或其他无需探活的项目返回 `required:false`、原因和空的 `targets` 数组。HTTP/HTTPS URL 没有显式端口时分别使用 80/443；只有主机名后面的 `:端口` 才是 URL 显式端口，域名或路径中的数字不能当作端口。HotMouse 通过 `net/url` 解析来源 URL，将候选地址和端口传给 LLM，并在保存探活方案前校正模型返回的端口。例如 `http://oj-10-30-15-1-54549.adworld.xctf.org.cn/` 的探活端口是 80。单独指定的 TCP `host:port` 保留其端口；同一主机有多个显式服务且模型选择了未提供的端口时重新分析。不探活比赛平台、flag 提交平台或无关服务。无法确定所需地址或端口时应返回拒绝及原因。无效输出或模型调用失败会保留启动门槛，间隔 5 秒重试，连续 3 次分析失败则项目失败。Mock 模式不调用 LLM，直接记录无需探活的演示分析结果。

探活每 20 秒为一个窗口，在窗口内执行带超时的 TCP 连接并关闭连接，在窗口结束时记录结果。每个必需的目标独立累计连续失败次数，任意目标成功连接会清零自身计数；任意目标连续 3 个窗口无法连接（1 分钟）即强制项目进入 `failed`，保存地址与失败原因并释放规划、执行租约，执行进程随后由租约心跳取消。正在规划、执行或恢复不会阻止探活。TCP 连接成功即视为存活，无需 HTTP 返回成功状态。

`GET /projects/{project}` 的 `target_probe` 包含分析、各目标失败计数、最近检查时间和连接错误。服务重启会继续已保存的探活方案与计数；恢复监控前会校正旧方案中与来源 URL 不符的端口，并清零这些错误连接的失败计数，使旧协程结果失效。停止或重新激活项目会保留识别结果和初始预处理证据，仅更新运行 ID、清零探活失败计数；继续推进不再调用 LLM 重新识别已知目标，旧运行的迟到结果不能修改新运行。项目停止、完成、失败或服务关闭后探活协程退出。

## 新接口

| 接口 | 用途 |
| --- | --- |
| `GET /projects/{p}/context` | 紧凑模型 YAML，参数 `phase`、`step_id` |
| `GET /projects/{p}/facts/{id}`、`steps/{id}`、`findings/{id}` | 完整记录详情 |
| `GET /projects/{p}/history?q=...&limit=5` | 项目历史检索 |
| `GET /projects/{p}/routes`、`POST /projects/{p}/routes` | 路线列表、创建 |
| `PATCH /projects/{p}/routes/{id}` | 路线状态、无进展阈值与复核记录 |
| `PUT /projects/{p}/budget` | 修改所有路线共用的项目时间预算 |
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

# eDoc（易道 / edoc2）数据源连接器

把 eDoc 内容库中的文件夹子树同步进 WeKnora 知识库，文件本体落对象存储，解析后参与检索。
本连接器**只读** eDoc：它只会登录、列目录、下载当前版本文件，不做任何写操作。

- 连接器类型：`edoc`
- 实现位置：`internal/datasource/connector/edoc/`
- 前端定义：`frontend/src/views/knowledge/settings/DataSourceEditorDialog.vue`（`type: 'edoc'`）

---

## 1. 凭证字段

| 字段 | 必填 | 说明 |
|---|---|---|
| `base_url` | 是 | eDoc 站点地址，如 `https://edoc.example.com:8866`。不带 query/fragment/用户名口令；缺协议默认 `https`。会过 SSRF 校验。 |
| `login_name` | 是 | 集成登录账号，例如专门给 AI 用的服务账号 |
| `integration_key` | 是 | eDoc「系统管理 → 安全策略 → 登录验证 → 集成登录密钥」里配置的密钥（UUID 形态）。**这是唯一的密钥凭据，连接器不保存密码。** |
| `ip_address` | 否 | 集成登录时上报给 eDoc 的审计 IP，默认 `127.0.0.1`；不参与鉴权 |

鉴权流程：`POST /api/services/org/UserLoginIntegrationByUserLoginName`
`{loginName, ipAddress, integrationKey}` → `data` 为会话 token；
之后所有 SDK 接口用 `?token=` 传递，服务端返回 `{"errorCode":"4"}` 表示会话过期，连接器会**重新登录一次**并重试。

`auth_type` 在 UI 上显示为 `custom`（既不是 OAuth 也不是纯 API token）。

### 账号需要的最小权限

- 对要同步的**文件夹**：浏览 + 下载（eDoc 权限位里的下载位 `32`）。
- 实测：权限只在授予的文件夹内生效，文件夹外的文件 `DownLoad/DownLoadCheck` 返回 `nResult=5`（没有权限），连接器会把它记为
  `edoc_permission_denied` 的失败项，不会静默丢数据。

---

## 2. 范围、增量与版本策略

### 2.1 同步范围

- 资源 ID 就是 **eDoc 文件夹 id**（十进制字符串，如 `204100`）。
- 选择器只列文件夹（`资源` = 目录），文件夹内的文件自动递归同步；**不支持单独勾选某个文件**。
- 一个数据源可以选多个文件夹；`ResolveResourceAncestors` 会按 `ParentFolderId` 逐级回溯，供懒加载树展开已保存的选择。

> 防误删保护：如果新选择与上次记录的文件夹**完全没有交集**，同步会拒绝执行并提示「新建数据源」，
> 避免把数据源指到另一个库后一轮把已同步内容全删掉。

### 2.2 只同步当前版本

eDoc 每次改文件都会递增 `FileLastVerId`。连接器只读 `FileLastVerId / FileModifyTime / FileCurSize`，
**从不调用版本列表接口、下载 URL 也不带 `ver_id`**，因此历史版本一个字节都不会被拉取。

`s settings.current_version_only` 显式为 `false` 时会直接报错（不支持历史版本），避免静默忽略。

### 2.3 增量指纹

```
fingerprint = "v:<FileLastVerId>|m:<FileModifyTime>|s:<FileCurSize>"
cursor.connector_cursor = {
  schema_version: 1,
  roots: ["204100"],
  root_names: {"204100": "竞标PPT"},        // 首次同步冻结，改名不会重命名已有条目
  files: {"1757239": {"fp": "...", "name": "..."} , ...}
}
```

一轮同步：

1. 递归列出所选文件夹下所有文件（`File/GetChildFilePageListByFolderId`，100 条/页）；
2. 指纹相同 → 跳过，**不发起下载**；
3. 指纹不同/新文件 → `DownLoad/DownLoadCheck` 取 `RegionHash` → `downLoad/index` 取字节 → 交给 WeKnora 入库；
4. 下载失败（无权限/超限/空文件）→ 发一条带 `error_reason_code` 的失败项，并把该文件指纹置空，**下一轮自动重试**；
5. 游标里有、本轮没列出的文件 → 发 `IsDeleted` 项做删除对账。

> 删除对账依赖「本轮列举完整成功」：任何一层目录列失败都会中止整轮，不产生删除，避免半可见的目录树被误判成大规模删除。
> 每 50 次游标变更写一次 checkpoint，中断后可续传。

### 2.4 文件命名

知识条目 `FileName = "<文件夹名>/<相对路径>/<原文件名>"`，逐段做文件名清洗。
`Title` 保留原文件名，便于前端展示。`ExternalID = "edoc:file:<fileId>"`，与改名前后的路径解耦。

---

## 3. 部署前置（必须）

| 项 | 值 | 原因 |
|---|---|---|
| `MAX_FILE_SIZE_MB` | ≥ 200 | 竞标PPT 有 25 个文件 >50MB，最大 129MB |
| `DOCREADER_GRPC_MAX_FILE_SIZE_MB` | ≥ `MAX_FILE_SIZE_MB` | docreader gRPC 单条消息上限，否则大文件直接被拒 |
| 工作空间配额 | 建议 ≥ 40 GiB | 竞标PPT 原始 9.45 GB，解析出的 chunk/向量另算 |
| 知识库存储后端 | 显式绑定可写对象存储 | 知识库**一旦有文件就不能再换存储后端**，必须建库时选对 |
| 车规：对象存储 region | 必须是真实 region（如 `cn-shanghai`），不能填桶名 | 否则 OSS 报 `Invalid signing region` |

镜像：连接器依赖 `anydoc`（pptx/docx 进程内转 markdown）。生产镜像若用 `-tags anydoc` 构建，
`DefaultParserEngine` 会把 ppt/pptx 优先交给 anydoc；不构建该 tag 时 pptx 回落 `markitdown`(docreader)，也能跑。

---

## 4. 上线步骤（可复跑）

```bash
BASE=http://localhost:8080/api/v1
H='X-API-Key: <workspace full-access key>'

# 1) 确认连接器已注册
curl -s -H "$H" $BASE/datasource/types | grep -o '"type":"edoc"'

# 2) 建知识库（显式绑定存储后端，创建后不可再改）
curl -s -X POST -H "$H" -H 'Content-Type: application/json' $BASE/knowledge-bases -d '{
  "name":"BD竞标","type":"document",
  "storage_backend_id":"<storage-backend-id>",
  "embedding_model_id":"<embedding-model-id>","summary_model_id":"<chat-model-id>",
  "chunking_config":{"chunk_size":512,"chunk_overlap":50,"separators":["\n\n","\n","。"]}}'

# 3) 建数据源（凭证必须随创建一起提交，服务端会立即校验配置）
curl -s -X POST -H "$H" -H 'Content-Type: application/json' $BASE/datasource -d '{
  "knowledge_base_id":"<kb-id>","name":"edoc-竞标PPT","type":"edoc",
  "sync_mode":"incremental","sync_schedule":"0 0 2 * * *",
  "conflict_strategy":"overwrite","sync_deletions":true,
  "config":{"type":"edoc","resource_ids":["204100"],
            "settings":{"current_version_only":true},
            "credentials":{"base_url":"https://edoc.example.com:8866",
                           "login_name":"<integration-account>",
                           "integration_key":"<integration-key>"}}}'

# 4) 校验 + 看树 + 触发
curl -s -X POST -H "$H" $BASE/datasource/<ds-id>/validate
curl -s -H "$H" "$BASE/datasource/<ds-id>/resources?parent_id=204038"
curl -s -X POST -H "$H" $BASE/datasource/<ds-id>/sync
curl -s -H "$H" $BASE/datasource/<ds-id>/logs?limit=5
```

注意：`sync_schedule` 是 **6 段 cron**（含秒），`0 2 * * *` 会被拒，要写 `0 0 2 * * *`。

---

## 5. 回滚与凭据吊销

| 动作 | 做法 |
|---|---|
| 回滚应用镜像 | `docker tag wechatopenai/weknora-app:upstream-20261023 wechatopenai/weknora-app:latest && docker compose up -d app`（UI 同理，`weknora-ui:upstream-20261023`） |
| 停止同步 | `POST /api/v1/datasource/<id>/pause`，或删除数据源（不删已有知识） |
| 彻底停用连接器 | 回滚镜像后重建 app；连接器不再出现在 `/datasource/types` |
| 吊销 API key | 删除 `tenant_api_keys` 中对应行，或走 UI 的 API Key 管理页 |
| 撤销 eDoc 权限 | eDoc 后台取消该账号对文件夹的下载位即可，连接器会以 `edoc_permission_denied` 失败而不是静默丢数据 |
| 清理已同步数据 | `DELETE /api/v1/knowledge-bases/<kb-id>/knowledge`（清空知识库内容），再删除数据源 |

密钥只出现在数据源 `credentials`（AES-256-GCM 落库，接口返回脱敏），**不要**写进 `.env`、代码或文档。

---

## 6. 后续扩目录

1. 在数据源编辑页新增/移除文件夹；或
2. `PUT /api/v1/datasource/<id>`，把 `config.resource_ids` 改为新的文件夹 id 列表（保留至少一个旧 id 以免触发防误删保护）。

新增目录后第一轮会下载新目录下的全部文件；移除目录后**下一轮会把该目录下已同步的条目当删除处理**（`sync_deletions=true` 时）。
若只想停止同步但保留内容，先 `sync_deletions=false` 再改范围。

---

## 7. 验收记录（2026-10-23，竞标PPT / folderId 204100）

| 项 | 结果 |
|---|---|
| 源端文件 | 378 个（365 pptx + 13 pdf），9.45 GB，最大 129 MB |
| 首轮同步 | 2443s，`total=378 / created=365 / skipped=13 / failed=0`，status=success |
| 13 个 skipped | 源端存在**字节完全相同**的副本（不同文件名）。修复前 WeKnora 按内容哈希去重会丢这 13 个；已把 `edoc` 加入 `usesSourceIdentityDuplicateCheck`，改为按 `datasource_id + external_id` 去重，重跑后补齐 |
| 补齐轮 | 98s，`created=13 / failed=0`，知识库条目 **378** |
| 无变更增量 | **1s**，`total=0 / created=0`，游标仍 378 条 → 零下载 |
| 落盘位置 | 378/378 `provider=oss`，`physical_path = storage://2250a85a…/oss://unionclinknow/BD/10000/<kb>/<uuid>.<ext>` |
| 解析 | 378/378 `parse_status=completed`，共 47,270 chunks |
| 检索抽查 | 「177Lu 核药」76 hits（0.955）、「特发性肺纤维化 ROCK」59 hits（0.99）、「细胞治疗 实体瘤 IND」92 hits（0.92） |
| 真实下载校验 | 直连 SDK 下载 2 个文件（23.5 MB / 46.5 MB），字节数与列表一致 |

---

## 8. 已知问题与排查

| 现象 | 原因 | 处理 |
|---|---|---|
| `invalid configuration: base_url is required` | 创建数据源时没带 `config.credentials`，服务端会立即校验 | 创建请求里一并提交凭证 |
| `Invalid signing region in Authorization header` | 存储后端 `config.region` 填成了桶名 | 改成真实 region（如 `cn-shanghai`） |
| `PutObject … AccessDenied … bucket acl` | AK 对该桶没有写权限 | 给 AK 授予 `oss:PutObject`，或换可写桶 |
| `Storage backend cannot be changed while the knowledge base contains files` | 建库时绑错了后端 | 只能新建库并迁移，故建库前先确认后端可写 |
| 同步日志里大量 `edoc_permission_denied` | 账号只有浏览权限，或范围越权 | 在 eDoc 侧补下载位 |
| `edoc_file_too_large` | 单文件超过 `MAX_FILE_SIZE_MB` | 调大该值并同步调大 docreader gRPC 上限 |
| `edoc_invalid_response` | 网关/反向代理返回了非 JSON（常见于 302 到登录页） | 检查 eDoc 反代是否把 API 也导向 SPA |
| 首轮后 `last_sync_cursor` 为空 | 观察到一次（仅首轮），第二轮起正常持久化 | 首轮结束后确认游标非空；为空则再触发一次（幂等，只会重复列举，不会重复入库） |

错误码前缀统一 `edoc_`，前端通过 `datasource.syncError.<code>` 本地化，缺失时回落到英文兜底文案：

| code | 含义 |
|---|---|
| `edoc_permission_denied` | 下载被拒（`nResult=5`） |
| `edoc_not_found` | 文件不存在（`nResult=601`） |
| `edoc_file_too_large` | 超过大小上限（下载前按列表 size 判定） |
| `edoc_empty_file` | 下载得到 0 字节 |
| `edoc_invalid_response` | 响应不是预期 JSON/envelope |
| `edoc_fetch_failed` | 其它下载/接口失败 |

---

## 9. 测试

```bash
# 单元测试（httptest 假 eDoc，覆盖分页/大文件/无权限/删除/令牌过期/清单失败中止）
go test ./internal/datasource/connector/edoc/ -cover

# 真实环境只读冒烟（需要环境变量）
EDOC_BASE_URL=https://edoc.example.com:8866 \
EDOC_LOGIN_NAME=<account> EDOC_INTEGRATION_KEY=<key> \
EDOC_FOLDER_ID=204100 EDOC_MAX_FILES=2 \
go test ./internal/datasource/connector/edoc/ -run TestRealEDocReadOnly -v
```
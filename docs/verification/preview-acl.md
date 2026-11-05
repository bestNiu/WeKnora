# 预览 + 用户×知识库 ACL 验证记录

可复跑脚本：`scripts/verify-preview-acl.sh`（自带 fixture 创建与按 `created_at` 清理；不硬编码密码，需传 `USER_PASSWORD`）。

```bash
$ USER_PASSWORD=... scripts/verify-preview-acl.sh
== 1. per-user visibility ==
PASS  restricted KB without grant -> 404
PASS  restricted KB hidden from the KB list
PASS  restricted KB with grant -> 200
== 2. download permission ==
PASS  viewer without can_download -> 403
PASS  raw /files source file without can_download -> 403
PASS  raw /files source file with grant -> 200
PASS  granted download -> 200 (23550755 bytes, PK)
== 3. machine principal fail-closed ==
PASS  unbound full-access key on restricted KB -> 404
PASS  bound key + grant -> 200
== 4. preview artifact ==
PASS  preview/meta -> pdf
== 5. UI: drawer preview + download button ==
PASS  UI document preview ready
PASS  UI toolbar download saved 23550755 bytes
PASS  failed panel download saved 23550755 bytes
== 5b. PDF preview is a rasterised copy ==
PASS  PDF preview artifact ready
PASS  PDF preview is not the original upload (15441035 vs 6151208)
PASS  PDF download without can_download -> 403
== 6. access management API + UI ==
PASS  PUT access -> 200
PASS  GET access reflects restricted
PASS  auto rows include requested member and admin
PASS  KB access tab renders the member policy
== 7. second viewer gray check ==
PASS  viewer2 restricted without grant -> 404
PASS  viewer2 with grant -> 200
PASS  viewer2 granted download -> 200
== 8. embed file proxy respects the ACL ==
PASS  unbound embed channel source file -> 403
PASS  embed channel bound to an owner with can_download -> 200

passed=25 failed=0
```
脚本覆盖的语义：

- **可见性**：`visibility_mode=restricted` 且无 `kb_user_access` 行时，KB 详情 404 且不出现在列表；写入 `can_view` 后 200。
- **下载**：`can_download=false` 时 `/knowledge/:id/download` 403；置 true 后 200，字节数与源文件一致（`PK` zip 头）。
- **原始文件代理**：`GET /files?file_path=<resource://...>` 对同一文档的 `source_file` 绑定按 `can_download` 判定（无授权 403、有授权 200）；提取图片/生成产物没有 `source_file` 绑定，继续可读；旧式 `local://`/`storage://` 物理路径不是 catalog 句柄，保持原有租户路径校验（单测 `TestServeFilesPhysicalPathKeepsLegacyContract`）。
- **PDF 预览**：PDF 与 Office 一样走派生预览（pypdfium2 + Pillow 逐页栅格化为 image-only PDF），`/preview` 返回的字节与源文件不同（实测 15,441,035 vs 6,151,208），`can_download=false` 时 `/download` 仍 403；`preview/meta` 返回 `kind=pdf`。
- **机器主体**：临时 full-access API Key 在 `owner_user_id` 为空时对 restricted KB 返回 404；绑定 owner 并授权后 200。IM 渠道 `Caller.UserID` 取 `owner_user_id`（`TestWithIMIdentityBindsChannelOwner`），embed/IM 等不经 `/api/v1` 中间件的入口使用进程级 fallback resolver（`TestDefaultResolverAppliesWithoutMiddleware`）。
- **UI**：会话首条清单的文档在抽屉中渲染派生 PDF；工具栏「下载」触发真实 download 事件并保存 23,550,755 字节；把 artifact 置为 `failed` 后错误面板仍提供「下载」，同样保存 23,550,755 字节。
- **权限页签**：临时租户 Admin 通过 `PUT /knowledge-bases/:id/access` 设置 restricted + 成员授权，`GET` 回读一致并包含自动写入的 creator/owner/admin 行；Playwright 打开 KB「成员与权限」页签并渲染策略面板。
- **embed 文件代理**：`/api/v1/embed/:channel_id/files` 现在与租户 `/files` 共用同一 source_file 守卫。未绑定 owner 的公开 embed 渠道取原始 PPTX → 403；渠道绑定 owner 且该 owner 有 `can_download` → 200（脚本第 8 步；`newFileServeHandler` 不再传 `kbService=nil`）。
- 脚本在**任何限制动作之前**记录 KB 的可见性备份；trap 与第 5/6 步的清理都用这份备份恢复。当清单文档与权限页签指向同一个 KB 时，`FINAL_VISIBILITY` 优先，因此 `RESTORE_TENANT=1` 一定以 `tenant` 结束；不带该变量时恢复运行前的状态。已连续验证：带 `RESTORE_TENANT=1` 与不带它各跑一次 23/23，结束后三个 KB 均为 `tenant`、`kb_user_access=0`、无残留 verify 用户。

## 失败态与重试

- `GET /knowledge/:id/preview/meta` 对 `failed` 行**只读返回**，不会自动重跑 LibreOffice；轮询不会造成渲染风暴。
- 用户点击「重试」时前端请求 `?retry=1`，服务端 `Retry` 强制重新渲染（实测：`meta` → `failed`；`meta?retry=1` → `pending`，随后 `ready`）。

## 全量回填结果（主库 BD竞标）

`POST /knowledge/preview/backfill` 对租户全部可渲染文档入队（386 篇），`PREVIEW_RENDER_CONCURRENCY=8` 下完成：

```text
ready=366  failed=22  pending=0   (Office 386 篇；PDF 预览在首次打开/回填时按需栅格化)
```

- 363 篇派生 PDF 已写入对象存储，首开直接返回 PDF。
- 22 篇渲染失败（损坏/超出 LibreOffice），错误写入 artifact；错误面板提供「下载」（受 `can_download` 约束）。
- 入库预生成位于 knowledge service：HTTP 上传、数据源同步、IM 直传、MCP 入库均调用 `enqueueKnowledgePreview`（passage 是文本，不是可渲染类型，不做预渲染）。

## 前置条件

docker compose 运行中（`WeKnora-app`、`WeKnora-postgres`）、`curl`、`python3`、pi-playwright CLI；无 UI 环境可 `SKIP_UI=1` 只跑 API 部分。
环境变量：`USER_PASSWORD`（必填）、`RESTORE_TENANT`（=1 时脚本结束把 KB 恢复为 `tenant`，防止失败运行留下的 restricted 状态）、`KB_ID`、`KNOWLEDGE_ID`、`USER_EMAIL`、`TENANT_ID`、`APP_URL`、`UI_URL`、`PG_CONTAINER`、`PW_CLI`。
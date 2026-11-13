# 企业微信自建应用登录（网页免登）

本文覆盖「工作台点开 WeKnora → 无感登录」和「企业微信内问答机器人」两条接入路线，
以及上线前必须做完的后台准备。

## 1. 为什么不是 OIDC

企微网页授权返回的是 `code`，服务端换到的是**成员 userid**（`/cgi-bin/auth/getuserinfo`），
没有 discovery / JWKS，因此不能复用 `oidc_auth` 配置。实现见 `internal/wecomauth`：

| 步骤 | 端点 |
|---|---|
| 发起 | `GET /api/v1/auth/wecom/authorize?redirect=/` → 302 到 `open.weixin.qq.com` |
| 回调 | `GET /api/v1/auth/wecom/callback?code&state` → 302 回 `#oidc_result=<base64>` |
| 开关 | `GET /api/v1/auth/wecom/config` → `{enabled, display_name}` |

回调复用 `#oidc_result` / `#oidc_error` 两个 hash key（前端 `App.vue` 已有全局处理），
所以前端不需要第二套回调逻辑；登录页只在「普通浏览器」显示按钮，企微内置浏览器
（UA 含 `wxwork`）直接静默跳转。

## 2. 身份映射：不用 email 做键

`users` 只有 `username`/`email` 唯一索引，邮箱改名会断链，企微成员也可能没填邮箱。
因此新增 `user_identities (provider, subject)`，WeCom 的 `subject` 就是 userid：

```text
migrations/versioned/000137_user_identity.up.sql   (PostgreSQL)
migrations/sqlite/000054_user_identity.up.sql      (SQLite)
```

未绑定成员默认**不自动开户**（`WECOM_AUTH_AUTO_PROVISION=false`），登录页提示
「请联系管理员开通」。需要自动开户时置 `true`，会创建 `wecom-<userid>` 账号并标记为 SSO-only。

## 3. 绑定既有账号（P3）

**重要**：先绑定再开登录，否则老用户登录后是新账号，会话和知识库权限接不上。

```bash
curl -X POST https://u-know.unionclin.com/api/v1/system/admin/user-identities/bind \
  -H "Authorization: Bearer <系统管理员 token>" \
  -H 'Content-Type: application/json' \
  -d '{"items":[
        {"user_id":"<本地用户ID>","subject":"zhangsan"},
        {"user_id":"<本地用户ID>","subject":"lisi"}
      ]}'
```

- 单次最多 1000 条；返回 `{bound, failed, failures[]}`，坏行不会中断整批。
- `subject` = 企微成员账号（通讯录里的「账号」，不是姓名、手机号）。
- 本地 user_id 见 `GET /api/v1/system/admin/users`。

## 3.1 批量开通名单（脚本）

`scripts/wecom-roster-import` 把「姓名名单」变成账号 + 成员 + 知识库授权，一个人一个幂等 DO 块：

```bash
# 1) 名单：姓名,权限标注（第二列只作审计）
# 2) 生成 SQL（不碰数据库）
go run ./scripts/wecom-roster-import \
  -roster wecom-roster.csv -kb "BD竞标" \
  -restrict "测试,edoc-connector-smoke" -tenant 10000 -out roster-import.sql
# 3) 审核后执行
docker exec -i WeKnora-postgres psql -U postgres -d WeKnora -v ON_ERROR_STOP=1 -f - < roster-import.sql
```

- 姓名 → userid 靠企微通讯录（`department/list` + 逐部门 `user/simplelist`；根部门会被 `60011` 拒，脚本会跳过并继续）
- 生成结果：`users`（用户名=真实姓名）+ `user_identities` + `tenant_members(viewer)` + `kb_user_access(can_view, can_download)`
- 目标知识库会被置为 `visibility_mode='restricted'`，名单外的人看不到
- **已存在同名本地账号的人会重复开户**：脚本按 userid 建号，不做人名合并。导入后需人工合并（见下）

导入后务必做一次冲突检查（下面三条都应为 0 / 空）：

```sql
-- 同一姓名多账号
select username, count(*) from users where deleted_at is null group by username having count(*) > 1;
-- 同一企微身份挂多个账号
select subject, count(*) from user_identities where provider='wecom' group by subject having count(*) > 1;
-- 中文名账号与同名英文账号并存（人名与 userid 不同步的典型症状）
select u.username, u.email from users u
 where u.email like '%@wecom.invalid' and u.deleted_at is null
   and exists (select 1 from users o where o.deleted_at is null and o.id <> u.id and o.username <> u.username
                and o.email not like '%@wecom.invalid');
```

合并示例（把企微身份挂到既有账号并删掉重复账号）：

```sql
UPDATE user_identities SET user_id = '<既有账号ID>' WHERE provider='wecom' AND subject='<userid>';
DELETE FROM kb_user_access WHERE user_id = '<重复账号ID>';
INSERT INTO kb_user_access (id, tenant_id, knowledge_base_id, user_id, can_view, can_download, created_at, updated_at)
SELECT gen_random_uuid()::text, 10000, k.id, '<既有账号ID>', true, true, now(), now()
  FROM knowledge_bases k WHERE k.name='BD竞标'
  AND NOT EXISTS (SELECT 1 FROM kb_user_access a WHERE a.knowledge_base_id=k.id AND a.user_id='<既有账号ID>' AND a.deleted_at IS NULL);
DELETE FROM tenant_members WHERE user_id='<重复账号ID>';
DELETE FROM users WHERE id='<重复账号ID>';
```

## 4. 配置

见 `.env.example` 的 `F4` 段落。Secret 只写 `.env`（`config/config.yaml` 被 git 跟踪，
不要放密钥），泄露后立即在企微后台重置。

```bash
WECOM_AUTH_ENABLE=true
WECOM_AUTH_CORP_ID=ww********
WECOM_AUTH_AGENT_ID=100****
WECOM_AUTH_AGENT_SECRET=<后台重置后的新值>
WECOM_AUTH_REDIRECT_URI=https://<域名>/api/v1/auth/wecom/callback
WECOM_AUTH_AUTO_PROVISION=false
```

## 5. 企微后台前置条件（P0）

1. **自建应用**：拿 `AgentId` + `Secret`，设置应用可见范围。
2. **网页授权及 JS-SDK 可信域名**：填 `u-know.unionclin.com`，并把企微给出的
   `WW_verify_*.txt` 放到站点根目录（`https://<域名>/WW_verify_xxx.txt` 可访问）。
3. **企业可信 IP**：填**服务器出口 IP**（从 app 容器内 `curl ifconfig.me` 实测，
   本机实测为 `61.152.132.35`）。填错会报 `60020 not allow to access from your ip`。
4. **通讯录读取权限**：仅自动开户/拉取姓名头像时需要；只做登录可不要。
5. **应用主页**：`https://u-know.unionclin.com`（工作台点开即首页）。

## 6. 路线 B：企微内问答机器人（`internal/im/wecom`）

与登录相互独立，走 IM 渠道配置（`credentials` JSON）：

```json
{"corp_id":"ww...","agent_secret":"...","token":"<后台 Token>",
 "encoding_aes_key":"<后台 EncodingAESKey>","corp_agent_id":1000131}
```

- `mode=webhook`：企微回调 `https://<域名>/api/v1/im/wecom/<channel>/callback`
- `mode=websocket`（长连接，默认）：需要智能机器人的 `bot_id` / `bot_secret`

## 7. 常见错误对照

| 现象 | 原因 |
|---|---|
| 授权页报 `redirect_uri 参数错误` | 可信域名未配置或不一致 |
| `60020 not allow to access from your ip` | 企业可信 IP 不是真实出口 IP |
| 回调 302 后登录页提示 `not_member` | 授权者不是本企业成员（换到的是 openid） |
| 提示 `unbound` | 该 userid 未绑定本地账号（见第 3 节） |
| 每次登录都换新 access_token | 多实例部署未共享 token 缓存（当前为单实例内存缓存） |

## 8. 已知限制

- `access_token` 缓存在**进程内存**，多副本部署会各自换 token 互相踢掉；
  水平扩容前需要把缓存挪到 Redis。
- `snsapi_base` 只返回 userid，拿不到邮箱/手机；需要这些字段时用花名册绑定，而不是
  改 `snsapi_privateinfo`（会弹授权确认，破坏免登体验）。
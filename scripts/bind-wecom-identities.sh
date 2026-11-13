#!/usr/bin/env bash
# 把企业微信成员 userid 绑定到既有 WeKnora 账号（P3）。
#
# 用法：
#   ./scripts/bind-wecom-identities.sh mapping.csv            # 预演（不写库）
#   ./scripts/bind-wecom-identities.sh mapping.csv --apply    # 真正写库
#
# mapping.csv 每行 "企微userid,邮箱"，# 开头为注释，空行忽略：
#   zhangsan,zhangsan@unionclin.com
#   lisi,lisi@unionclin.com
#
# 为什么按 email 找账号：企微 userid 是成员账号（通讯录里的「账号」），
# 而本地账号的稳定键是 email —— 两者之间没有公共主键，只能人工对齐一次。
# 绑定后登录不再依赖 email，所以后续改邮箱不会断链。
set -euo pipefail

PG_CONTAINER=${PG_CONTAINER:-WeKnora-postgres}
PG_USER=${PG_USER:-postgres}
PG_DB=${PG_DB:-WeKnora}

CSV=${1:-}
APPLY=false
if [[ "${2:-}" == "--apply" ]]; then APPLY=true; fi

if [[ -z "$CSV" || ! -f "$CSV" ]]; then
  echo "用法: $0 <mapping.csv> [--apply]" >&2
  exit 2
fi

# SQL 走 stdin：psql 的 :'var' 插值只在脚本/stdin 输入时生效，用 -c 传 SQL
# 变量不会被替换（会直接报 syntax error at or near ":"）。
psql_run() {
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -At -v ON_ERROR_STOP=1 "$@" -f -
}

bound=0; skipped=0; failed=0
while IFS=, read -r userid email; do
  userid=$(echo "${userid:-}" | xargs)
  email=$(echo "${email:-}" | xargs)
  [[ -z "$userid" || -z "$email" ]] && continue
  [[ "$userid" == \#* ]] && continue

  local_id=$(echo "SELECT id FROM users WHERE lower(email) = lower(:'mail') AND deleted_at IS NULL LIMIT 1;" \
    | psql_run -v mail="$email")

  if [[ -z "$local_id" ]]; then
    echo "✗ $userid: 本地没有 email=$email 的账号"
    failed=$((failed + 1))
    continue
  fi

  if [[ "$APPLY" != true ]]; then
    echo "· $userid -> $local_id ($email)  [预演]"
    bound=$((bound + 1))
    continue
  fi

  result=$(echo "INSERT INTO user_identities (id, user_id, provider, subject, created_at, updated_at)
     VALUES (gen_random_uuid()::text, :'uid', 'wecom', :'sub', now(), now())
     ON CONFLICT (provider, subject) DO NOTHING
     RETURNING id;" | psql_run -v uid="$local_id" -v sub="$userid" || true)

  if [[ -n "$result" ]]; then
    echo "✓ $userid -> $local_id ($email)"
    bound=$((bound + 1))
  else
    echo "= $userid 已绑定，跳过"
    skipped=$((skipped + 1))
  fi
done < "$CSV"

echo "----"
echo "新增 $bound 条，已存在 $skipped 条，失败 $failed 条"
if [[ "$APPLY" != true ]]; then
  echo "（预演模式，未写库；确认后加 --apply）"
fi
[[ $failed -eq 0 ]]
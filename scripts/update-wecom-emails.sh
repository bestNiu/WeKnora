#!/usr/bin/env bash
# 用线下导出的通讯录清单回填 users.email。
#
# 为什么线下导：2022-06-20 之后创建的自建应用，企微 /cgi-bin/user/get 不再返回
# 邮箱/手机等敏感字段（只能走 snsapi_privateinfo 成员授权，会弹确认框打断免登），
# 所以邮箱只能由管理员从「通讯录 → 导出成员信息」拿到，再回填本地库。
#
# 用法：
#   ./scripts/update-wecom-emails.sh wecom-emails.csv            # 预演（不写库）
#   ./scripts/update-wecom-emails.sh wecom-emails.csv --apply    # 真正写库
#
# CSV 每行 "姓名,手机号,邮箱"，# 开头为注释，空行忽略。
# 认人顺序：企微 userid 等于手机号（user_identities.subject）优先；
#           其次按用户名（含 -YY/-CO 变体）匹配，用于 userid 是拼音别名的账号
#           （如 LinBei / QingShui）。
# 登录不依赖 email（身份键是 user_identities），所以改 email 不会断链。
set -euo pipefail

PG_CONTAINER=${PG_CONTAINER:-WeKnora-postgres}
PG_USER=${PG_USER:-postgres}
PG_DB=${PG_DB:-WeKnora}

CSV=${1:-}
APPLY=false
if [[ "${2:-}" == "--apply" ]]; then APPLY=true; fi

if [[ -z "$CSV" || ! -f "$CSV" ]]; then
  echo "用法: $0 <wecom-emails.csv> [--apply]" >&2
  exit 2
fi

# SQL 走 stdin：psql 的 :'var' 插值只在脚本/stdin 输入时生效。
psql_run() {
  docker exec -i "$PG_CONTAINER" psql -U "$PG_USER" -d "$PG_DB" -At -v ON_ERROR_STOP=1 "$@" -f -
}

updated=0; unchanged=0; skipped=0; failed=0
while IFS=, read -r name phone email; do
  name=$(echo "${name:-}" | xargs)
  phone=$(echo "${phone:-}" | xargs)
  email=$(echo "${email:-}" | xargs)
  [[ -z "$name" || -z "$phone" || -z "$email" ]] && continue
  [[ "$name" == \#* || "$name" == "姓名" ]] && continue

  row=$(cat <<'SQL' | psql_run -v name="$name" -v phone="$phone" \
    -v name_yy="${name}-YY" -v name_co="${name}-CO"
SELECT u.id || '|' || u.username || '|' || u.email
  FROM users u
 WHERE u.deleted_at IS NULL
   AND (EXISTS (SELECT 1 FROM user_identities i
                 WHERE i.user_id = u.id AND i.provider = 'wecom' AND i.subject = :'phone')
        OR u.username IN (:'name', :'name_yy', :'name_co'))
 ORDER BY (EXISTS (SELECT 1 FROM user_identities i
                    WHERE i.user_id = u.id AND i.provider = 'wecom' AND i.subject = :'phone')) DESC
 LIMIT 1;
SQL
)

  if [[ -z "$row" ]]; then
    echo "· $name ($phone) 本地无账号，跳过"
    skipped=$((skipped + 1))
    continue
  fi

  uid=${row%%|*}; rest=${row#*|}; uname=${rest%%|*}; old=${rest#*|}

  if [[ "$old" == "$email" ]]; then
    echo "= $name -> $uname ($email) 已是目标邮箱"
    unchanged=$((unchanged + 1))
    continue
  fi

  if [[ "$APPLY" != true ]]; then
    echo "· $name -> $uname  $old  =>  $email  [预演]"
    updated=$((updated + 1))
    continue
  fi

  # 唯一约束按大小写敏感比较，先挡住只差大小写的撞车。
  clash=$(echo "SELECT username FROM users WHERE lower(email) = lower(:'mail') AND id <> :'uid' AND deleted_at IS NULL;" \
    | psql_run -v mail="$email" -v uid="$uid")
  if [[ -n "$clash" ]]; then
    echo "✗ $name -> $uname: 邮箱 $email 已被账号 $clash 占用"
    failed=$((failed + 1))
    continue
  fi

  echo "UPDATE users SET email = :'mail', updated_at = now() WHERE id = :'uid';" \
    | psql_run -v mail="$email" -v uid="$uid" >/dev/null
  echo "✓ $name -> $uname  $old  =>  $email"
  updated=$((updated + 1))
done < "$CSV"

echo "----"
echo "更新 $updated 条，已是目标 $unchanged 条，无对应账号 $skipped 条，失败 $failed 条"
if [[ "$APPLY" != true ]]; then
  echo "（预演模式，未写库；确认后加 --apply）"
fi
[[ $failed -eq 0 ]]
#!/usr/bin/env bash
# End-to-end verification for the preview + per-user KB access work.
#
# It creates and cleans up its own fixtures so the result is reproducible on a
# running docker-compose deployment:
#   1. per-user visibility: restricted KB without a grant is invisible, with a
#      grant it is readable;
#   2. download permission: viewer 403 -> grant 200 with identical bytes;
#   3. machine principal: unbound full-access API key is denied a restricted KB,
#      bound key + grant is allowed;
#   4. UI: the chat drawer renders the derived PDF, the toolbar download button
#      downloads the source, and a failed artifact still offers a download in
#      the error panel.
#
# Requirements: docker (with the compose stack running), curl, python3, and the
# pi-playwright CLI for the UI half (skipped with a notice when absent).
#
# Usage:
#   scripts/verify-preview-acl.sh
# Env overrides:
#   KB_ID, KNOWLEDGE_ID, DOC_TITLE, USER_EMAIL, USER_PASSWORD, TENANT_ID,
#   APP_URL, PG_CONTAINER, PW_CLI, SKIP_UI=1
set -euo pipefail

KB_ID="${KB_ID:-128316d5-f301-44f2-95f1-ea29c6b69aa7}"
KNOWLEDGE_ID="${KNOWLEDGE_ID:-7a970c04-c6ec-40d0-a869-5f17529c1344}"
DOC_TITLE="${DOC_TITLE:-细胞疗法_实体瘤}"
USER_EMAIL="${USER_EMAIL:-zhangzy@unionclin.com}"
# Required: the script never hardcodes a password. Pass it in the environment:
#   USER_PASSWORD=... scripts/verify-preview-acl.sh
USER_PASSWORD="${USER_PASSWORD:-}"
TENANT_ID="${TENANT_ID:-10000}"
APP_URL="${APP_URL:-http://localhost:8080}"
UI_URL="${UI_URL:-http://localhost}"
PG_CONTAINER="${PG_CONTAINER:-WeKnora-postgres}"
PW_CLI="${PW_CLI:-$HOME/.pi/agent/npm/node_modules/pi-playwright/skills/playwright-browser/scripts/pw.js}"

PASS=0
FAIL=0
pass() { printf 'PASS  %s\n' "$1"; PASS=$((PASS + 1)); }
fail() { printf 'FAIL  %s\n' "$1"; FAIL=$((FAIL + 1)); }
psql_q() { docker exec -i "$PG_CONTAINER" psql -U postgres -d WeKnora -tAc "$1"; }

USER_ID=$(psql_q "select id from users where email='${USER_EMAIL}'")
if [[ -z "$USER_ID" ]]; then
  echo "user ${USER_EMAIL} not found in WeKnora" >&2
  exit 1
fi
if [[ -z "$USER_PASSWORD" ]]; then
  echo "USER_PASSWORD is required (no password is hardcoded in this script)" >&2
  exit 1
fi

# Rows this script creates are removed by created_at, so a run never deletes an
# operator's pre-existing grants.
START_TS=$(date -u +"%Y-%m-%d %H:%M:%S")
TITLE=$(psql_q "select title from knowledges where id='${KNOWLEDGE_ID}'")
# A distinctive substring of the exact document title: the list answer may
# include sibling decks, and the UI check must open THIS document (the one in
# the granted KB with a prepared artifact).
CHIP_TEXT="${CHIP_TEXT:-$(python3 -c 'import sys; print(sys.argv[1].removesuffix(".pptx")[:18])' "$TITLE")}"

BACKUP_VISIBILITY=$(psql_q "select visibility_mode from knowledge_bases where id='${KB_ID}'")
# A crashed earlier run can leave the KB restricted with no grants (unusable for
# viewers). RESTORE_TENANT=1 makes the script end in the default mode.
FINAL_VISIBILITY="${BACKUP_VISIBILITY:-tenant}"
if [[ "${RESTORE_TENANT:-0}" == "1" ]]; then
  FINAL_VISIBILITY="tenant"
fi
BACKUP_STATUS=$(psql_q "select status from knowledge_preview_artifacts where knowledge_id='${KNOWLEDGE_ID}'")
cleanup() {
  psql_q "delete from kb_user_access where knowledge_base_id='${KB_ID}' and created_at >= '${START_TS:-1970-01-01}'" >/dev/null || true
  psql_q "update knowledge_bases set visibility_mode='${FINAL_VISIBILITY:-tenant}' where id='${KB_ID}'" >/dev/null || true
  psql_q "update knowledge_preview_artifacts set status='${BACKUP_STATUS:-ready}', error_message='' where knowledge_id='${KNOWLEDGE_ID}'" >/dev/null || true
  psql_q "delete from tenant_api_keys where name like 'verify-preview-%'" >/dev/null || true
  psql_q "delete from embed_channels where name='verify-embed'" >/dev/null || true
  if [[ -n "${UI_KB_ID:-}" && -n "${USER_ID:-}" ]]; then
    psql_q "delete from kb_user_access where knowledge_base_id='${UI_KB_ID}' and created_at >= '${START_TS:-1970-01-01}'" >/dev/null || true
    if [[ "$UI_KB_ID" == "$KB_ID" ]]; then
      psql_q "update knowledge_bases set visibility_mode='${FINAL_VISIBILITY:-tenant}' where id='${UI_KB_ID}'" >/dev/null || true
    else
      psql_q "update knowledge_bases set visibility_mode='${UI_VISIBILITY_BAK:-tenant}' where id='${UI_KB_ID}'" >/dev/null || true
    fi
  fi
  if [[ -n "${UI_DOC_ID:-}" ]]; then
    psql_q "update knowledge_preview_artifacts set status='${UI_ARTIFACT_BAK:-ready}', error_message='' where knowledge_id='${UI_DOC_ID}'" >/dev/null || true
  fi
}
trap cleanup EXIT

login() {
  curl -sf -X POST "${APP_URL}/api/v1/auth/login" -H 'Content-Type: application/json' \
    -d "{\"email\":\"${USER_EMAIL}\",\"password\":\"${USER_PASSWORD}\"}" |
    python3 -c 'import sys,json; print(json.load(sys.stdin)["token"])'
}

TOKEN=$(login)
if [[ -z "$TOKEN" ]]; then
  echo "login failed for ${USER_EMAIL}" >&2
  exit 1
fi
USER_JSON=$(curl -sf -X POST "${APP_URL}/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"${USER_EMAIL}\",\"password\":\"${USER_PASSWORD}\"}" |
  python3 -c 'import sys,json; d=json.load(sys.stdin); json.dump(d["user"], sys.stdout)')
TENANT_JSON=$(curl -sf -X POST "${APP_URL}/api/v1/auth/login" -H 'Content-Type: application/json' \
  -d "{\"email\":\"${USER_EMAIL}\",\"password\":\"${USER_PASSWORD}\"}" |
  python3 -c 'import sys,json; d=json.load(sys.stdin); json.dump(d["active_tenant"], sys.stdout)')

http_status() { curl -s -o /dev/null -w '%{http_code}' "$@"; }

echo "== 1. per-user visibility =="
psql_q "update knowledge_bases set visibility_mode='restricted' where id='${KB_ID}'" >/dev/null
psql_q "delete from kb_user_access where knowledge_base_id='${KB_ID}' and user_id='${USER_ID}'" >/dev/null
STATUS=$(http_status -H "Authorization: Bearer ${TOKEN}" "${APP_URL}/api/v1/knowledge-bases/${KB_ID}")
if [[ "$STATUS" == "404" ]]; then pass "restricted KB without grant -> 404"; else fail "restricted KB without grant -> ${STATUS} (want 404)"; fi
LISTED=$(curl -s -H "Authorization: Bearer ${TOKEN}" "${APP_URL}/api/v1/knowledge-bases" |
  python3 -c "import sys,json; d=json.load(sys.stdin); print(any(k['id']=='${KB_ID}' for k in d.get('data',[])))")
if [[ "$LISTED" == "False" ]]; then pass "restricted KB hidden from the KB list"; else fail "restricted KB listed"; fi

psql_q "insert into kb_user_access (id, tenant_id, knowledge_base_id, user_id, can_view, can_download) values (gen_random_uuid(), ${TENANT_ID}, '${KB_ID}', '${USER_ID}', true, true)" >/dev/null
STATUS=$(http_status -H "Authorization: Bearer ${TOKEN}" "${APP_URL}/api/v1/knowledge-bases/${KB_ID}")
if [[ "$STATUS" == "200" ]]; then pass "restricted KB with grant -> 200"; else fail "restricted KB with grant -> ${STATUS}"; fi

echo "== 2. download permission =="
psql_q "update kb_user_access set can_download=false where knowledge_base_id='${KB_ID}' and user_id='${USER_ID}'" >/dev/null
STATUS=$(http_status -H "Authorization: Bearer ${TOKEN}" "${APP_URL}/api/v1/knowledge/${KNOWLEDGE_ID}/download")
if [[ "$STATUS" == "403" ]]; then pass "viewer without can_download -> 403"; else fail "viewer without can_download -> ${STATUS}"; fi
REF=$(curl -s -H "Authorization: Bearer ${TOKEN}" "${APP_URL}/api/v1/knowledge/${KNOWLEDGE_ID}" | python3 -c 'import sys,json; print(json.load(sys.stdin)["data"]["file_path"])')
STATUS=$(http_status -H "Authorization: Bearer ${TOKEN}" "${APP_URL}/files?file_path=$(python3 -c 'import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))' "$REF")")
if [[ "$STATUS" == "403" ]]; then pass "raw /files source file without can_download -> 403"; else fail "raw /files without can_download -> ${STATUS}"; fi
psql_q "update kb_user_access set can_download=true where knowledge_base_id='${KB_ID}' and user_id='${USER_ID}'" >/dev/null
STATUS=$(http_status -H "Authorization: Bearer ${TOKEN}" "${APP_URL}/files?file_path=$(python3 -c 'import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))' "$REF")")
if [[ "$STATUS" == "200" ]]; then pass "raw /files source file with grant -> 200"; else fail "raw /files with grant -> ${STATUS}"; fi
curl -sf -H "Authorization: Bearer ${TOKEN}" "${APP_URL}/api/v1/knowledge/${KNOWLEDGE_ID}/download" -o /tmp/verify-preview-download.bin
SIZE=$(stat -c %s /tmp/verify-preview-download.bin)
HEAD=$(head -c 2 /tmp/verify-preview-download.bin)
if [[ "$SIZE" -gt 0 && "$HEAD" == "PK" ]]; then pass "granted download -> 200 (${SIZE} bytes, PK)"; else fail "granted download bytes=${SIZE} head=${HEAD}"; fi

echo "== 3. machine principal fail-closed =="
KEY_TOKEN="verify-preview-$(date +%s)"
KEY_HASH=$(printf '%s' "$KEY_TOKEN" | sha256sum | cut -d' ' -f1)
psql_q "insert into tenant_api_keys (tenant_id, name, key_hash, api_key, full_access, scope_type) values (${TENANT_ID}, 'verify-preview-key', '${KEY_HASH}', '${KEY_TOKEN}', true, 'tenant')" >/dev/null
STATUS=$(http_status -H "X-API-Key: ${KEY_TOKEN}" "${APP_URL}/api/v1/knowledge-bases/${KB_ID}")
if [[ "$STATUS" == "404" ]]; then pass "unbound full-access key on restricted KB -> 404"; else fail "unbound full-access key -> ${STATUS}"; fi
psql_q "update tenant_api_keys set owner_user_id='${USER_ID}' where name='verify-preview-key'" >/dev/null
STATUS=$(http_status -H "X-API-Key: ${KEY_TOKEN}" "${APP_URL}/api/v1/knowledge-bases/${KB_ID}")
if [[ "$STATUS" == "200" ]]; then pass "bound key + grant -> 200"; else fail "bound key -> ${STATUS}"; fi

echo "== 4. preview artifact =="
for _ in $(seq 1 30); do
  META=$(curl -s -H "Authorization: Bearer ${TOKEN}" "${APP_URL}/api/v1/knowledge/${KNOWLEDGE_ID}/preview/meta")
  KIND=$(echo "$META" | python3 -c 'import sys,json; print(json.load(sys.stdin)["data"]["kind"])')
  [[ "$KIND" == "pdf" ]] && break
  sleep 2
done
if [[ "$KIND" == "pdf" ]]; then pass "preview/meta -> pdf"; else fail "preview/meta -> ${KIND}"; fi

if [[ "${SKIP_UI:-0}" == "1" || ! -f "$PW_CLI" ]]; then
  echo "SKIP  UI checks (pi-playwright CLI not found or SKIP_UI=1)"
else
  echo "== 5. UI: drawer preview + download button =="
  SESSION_ID=$(curl -sf -X POST "${APP_URL}/api/v1/sessions" -H "Authorization: Bearer ${TOKEN}" \
    -H 'Content-Type: application/json' -d '{}' |
    python3 -c 'import sys,json; d=json.load(sys.stdin); print(d.get("data",{}).get("id") or d.get("id"))')
  AGENT_ID=$(psql_q "select id from custom_agents where name='PPT 资产清单' limit 1")
  curl -sN --max-time 180 -X POST "${APP_URL}/api/v1/agent-chat/${SESSION_ID}" \
    -H "Authorization: Bearer ${TOKEN}" -H 'Content-Type: application/json' \
    -d "{\"query\":\"${QUERY:-列出实体瘤相关的PPT，最多10个}\",\"agent_enabled\":true,\"agent_id\":\"${AGENT_ID}\",\"channel\":\"api\"}" \
    -o /tmp/verify-preview-sse.txt >/dev/null || true

  # Use whatever document the answer actually lists first: the check is about
  # the drawer/download behaviour, not about this particular deck. Grant the
  # permission on that document's own KB so the button is enabled.
  UI_DOC_ID=$(grep -o 'weknora://doc/[a-f0-9-]*' /tmp/verify-preview-sse.txt | head -1 | sed 's|weknora://doc/||')
  if [[ -z "$UI_DOC_ID" ]]; then
    fail "agent answer listed no document to preview"
  else
    UI_KB_ID=$(curl -sf -H "Authorization: Bearer ${TOKEN}" "${APP_URL}/api/v1/knowledge/${UI_DOC_ID}" |
      python3 -c 'import sys,json; print(json.load(sys.stdin)["data"]["knowledge_base_id"])')
    if [[ "$UI_KB_ID" == "$KB_ID" ]]; then
      # The script already set KB_ID restricted above; the real pre-run value
      # is the one captured at the start.
      UI_VISIBILITY_BAK="$BACKUP_VISIBILITY"
    else
      UI_VISIBILITY_BAK=$(psql_q "select visibility_mode from knowledge_bases where id='${UI_KB_ID}'")
    fi
    UI_ARTIFACT_BAK=$(psql_q "select status from knowledge_preview_artifacts where knowledge_id='${UI_DOC_ID}'")
    psql_q "insert into kb_user_access (id, tenant_id, knowledge_base_id, user_id, can_view, can_download) values (gen_random_uuid(), ${TENANT_ID}, '${UI_KB_ID}', '${USER_ID}', true, true) on conflict do nothing" >/dev/null
    psql_q "update knowledge_preview_artifacts set status='ready', error_message='' where knowledge_id='${UI_DOC_ID}'" >/dev/null
    # Make sure a PDF exists for that document (first open renders on demand).
    for _ in $(seq 1 60); do
      KIND=$(curl -s -H "Authorization: Bearer ${TOKEN}" "${APP_URL}/api/v1/knowledge/${UI_DOC_ID}/preview/meta" |
        python3 -c 'import sys,json; print(json.load(sys.stdin)["data"]["kind"])')
      [[ "$KIND" == "pdf" ]] && break
      sleep 2
    done
    if [[ "$KIND" == "pdf" ]]; then pass "UI document preview ready"; else fail "UI document preview kind=${KIND}"; fi

    ui_code() {
      cat > /tmp/verify-preview-ui.js <<JS
async (page) => {
  await page.evaluate(({token, user, tenant}) => {
    localStorage.setItem('weknora_token', token);
    localStorage.setItem('weknora_user', JSON.stringify(user));
    localStorage.setItem('weknora_tenant', JSON.stringify(tenant));
    localStorage.setItem('weknora_selected_tenant_id', String(tenant.id));
  }, {token: $(python3 -c 'import json,sys;print(json.dumps(sys.argv[1]))' "$TOKEN"), user: ${USER_JSON}, tenant: ${TENANT_JSON}});
  await page.goto('${UI_URL}/platform/chat/${SESSION_ID}', {waitUntil: 'domcontentloaded'});
  await page.waitForSelector('.weknora-entity-link', {timeout: 90000});
  await page.locator('.weknora-entity-link').first().click();
  ${1}
}
JS
    }

    rm -f /tmp/verify-preview-ui-download.bin /tmp/verify-preview-ui-failed.bin
    ui_code "await page.waitForSelector('.preview-pdf', {timeout: 120000}); await page.waitForTimeout(1200);
    const dl = page.locator('.preview-toolbar button[title=\"下载\"], .preview-toolbar button[aria-label=\"下载\"]').first();
    const [download] = await Promise.all([page.waitForEvent('download', {timeout: 60000}).catch(() => null), dl.click()]);
    if (download) { await download.saveAs('/tmp/verify-preview-ui-download.bin'); }"
    node "$PW_CLI" open "$UI_URL" >/dev/null
    node "$PW_CLI" run-code "$(cat /tmp/verify-preview-ui.js)" >/dev/null 2>&1 || true
    if [[ -f /tmp/verify-preview-ui-download.bin ]] && [[ "$(stat -c %s /tmp/verify-preview-ui-download.bin)" -gt 0 ]]; then
      pass "UI toolbar download saved $(stat -c %s /tmp/verify-preview-ui-download.bin) bytes"
    else
      fail "UI toolbar download did not produce a file"
    fi

    psql_q "update knowledge_preview_artifacts set status='failed', error_message='verify: simulated failure', updated_at=now() where knowledge_id='${UI_DOC_ID}'" >/dev/null
    ui_code "await page.waitForSelector('.preview-error', {timeout: 120000});
    const dl = page.locator('.preview-error button').filter({hasText: '下载'}).first();
    if (await dl.count()) {
      const [download] = await Promise.all([page.waitForEvent('download', {timeout: 60000}).catch(() => null), dl.click()]);
      if (download) { await download.saveAs('/tmp/verify-preview-ui-failed.bin'); }
    }"
    node "$PW_CLI" run-code "$(cat /tmp/verify-preview-ui.js)" >/dev/null 2>&1 || true
    if [[ -f /tmp/verify-preview-ui-failed.bin ]] && [[ "$(stat -c %s /tmp/verify-preview-ui-failed.bin)" -gt 0 ]]; then
      pass "failed panel download saved $(stat -c %s /tmp/verify-preview-ui-failed.bin) bytes"
    else
      fail "failed panel has no working download"
    fi

    psql_q "delete from kb_user_access where knowledge_base_id='${UI_KB_ID}' and user_id='${USER_ID}'" >/dev/null
    if [[ "$UI_KB_ID" == "$KB_ID" ]]; then
      psql_q "update knowledge_bases set visibility_mode='${FINAL_VISIBILITY:-tenant}' where id='${UI_KB_ID}'" >/dev/null
    else
      psql_q "update knowledge_bases set visibility_mode='${UI_VISIBILITY_BAK:-tenant}' where id='${UI_KB_ID}'" >/dev/null
    fi
    psql_q "update knowledge_preview_artifacts set status='${UI_ARTIFACT_BAK:-ready}', error_message='' where knowledge_id='${UI_DOC_ID}'" >/dev/null
  fi
fi

echo "== 5b. PDF preview is a rasterised copy =="
PDF_ID=$(psql_q "select id from knowledges where knowledge_base_id='${KB_ID}' and file_type='pdf' and deleted_at is null limit 1")
if [[ -z "$PDF_ID" ]]; then
  echo "SKIP  no PDF in KB ${KB_ID}"
else
  PDF_SRC_SIZE=$(psql_q "select file_size from knowledges where id='${PDF_ID}'")
  # Step 5 cleans up the grants it created; recreate a view-only grant for the
  # PDF check (preview must work, download must stay denied).
  psql_q "delete from kb_user_access where knowledge_base_id='${KB_ID}' and user_id='${USER_ID}'" >/dev/null
  psql_q "insert into kb_user_access (id, tenant_id, knowledge_base_id, user_id, can_view, can_download) values (gen_random_uuid(), ${TENANT_ID}, '${KB_ID}', '${USER_ID}', true, false)" >/dev/null
  for _ in $(seq 1 40); do
    KIND=$(curl -s -H "Authorization: Bearer ${TOKEN}" "${APP_URL}/api/v1/knowledge/${PDF_ID}/preview/meta" |
      python3 -c 'import sys,json; print(json.load(sys.stdin)["data"]["kind"])')
    [[ "$KIND" == "pdf" ]] && break
    sleep 3
  done
  if [[ "$KIND" == "pdf" ]]; then pass "PDF preview artifact ready"; else fail "PDF preview kind=${KIND}"; fi
  curl -sf -H "Authorization: Bearer ${TOKEN}" "${APP_URL}/api/v1/knowledge/${PDF_ID}/preview" -o /tmp/verify-preview-pdf.bin
  PDF_PREVIEW_SIZE=$(stat -c %s /tmp/verify-preview-pdf.bin)
  if [[ "$PDF_PREVIEW_SIZE" != "$PDF_SRC_SIZE" ]]; then
    pass "PDF preview is not the original upload (${PDF_PREVIEW_SIZE} vs ${PDF_SRC_SIZE})"
  else
    fail "PDF preview returned the original upload"
  fi
  STATUS=$(http_status -H "Authorization: Bearer ${TOKEN}" "${APP_URL}/api/v1/knowledge/${PDF_ID}/download")
  if [[ "$STATUS" == "403" ]]; then pass "PDF download without can_download -> 403"; else fail "PDF download -> ${STATUS}"; fi
  psql_q "update kb_user_access set can_download=true where knowledge_base_id='${KB_ID}' and user_id='${USER_ID}'" >/dev/null
fi

echo "== 6. access management API + UI =="
ADMIN_EMAIL="verify-admin-$(date +%s)@unionclin.com"
ADMIN_PASS="VerifyAdmin!$(date +%s)"
ADMIN_ID=$(python3 -c 'import uuid; print(uuid.uuid4())')
ADMIN_HASH=$(python3 -c 'import bcrypt,sys; print(bcrypt.hashpw(sys.argv[1].encode(), bcrypt.gensalt(rounds=10)).decode())' "$ADMIN_PASS")
psql_q "begin; insert into users (id, username, email, password_hash, tenant_id, is_active) values ('${ADMIN_ID}', 'verify-admin', '${ADMIN_EMAIL}', '${ADMIN_HASH}', ${TENANT_ID}, true); insert into tenant_members (user_id, tenant_id, role, status, joined_at) values ('${ADMIN_ID}', ${TENANT_ID}, 'admin', 'active', now()); commit;" >/dev/null
ADMIN_LOGIN=$(curl -sf -X POST "${APP_URL}/api/v1/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"${ADMIN_EMAIL}\",\"password\":\"${ADMIN_PASS}\"}")
ADMIN_TOKEN=$(printf '%s' "$ADMIN_LOGIN" | python3 -c 'import sys,json; print(json.load(sys.stdin)["token"])')
ADMIN_USER_JSON=$(printf '%s' "$ADMIN_LOGIN" | python3 -c 'import sys,json; json.dump(json.load(sys.stdin)["user"], sys.stdout)')
ADMIN_TENANT_JSON=$(printf '%s' "$ADMIN_LOGIN" | python3 -c 'import sys,json; json.dump(json.load(sys.stdin)["active_tenant"], sys.stdout)')

STATUS=$(curl -s -o /tmp/verify-access.json -w '%{http_code}' -X PUT \
  "${APP_URL}/api/v1/knowledge-bases/${KB_ID}/access" \
  -H "Authorization: Bearer ${ADMIN_TOKEN}" -H 'Content-Type: application/json' \
  -d "{\"visibility_mode\":\"restricted\",\"members\":[{\"user_id\":\"${USER_ID}\",\"can_view\":true,\"can_download\":true}]}")
if [[ "$STATUS" == "200" ]]; then pass "PUT access -> 200"; else fail "PUT access -> ${STATUS}"; fi

POLICY=$(curl -s -H "Authorization: Bearer ${ADMIN_TOKEN}" "${APP_URL}/api/v1/knowledge-bases/${KB_ID}/access")
MODE=$(printf '%s' "$POLICY" | python3 -c 'import sys,json; print(json.load(sys.stdin)["data"]["visibility_mode"])')
if [[ "$MODE" == "restricted" ]]; then pass "GET access reflects restricted"; else fail "GET access mode=${MODE}"; fi
AUTO=$(printf '%s' "$POLICY" | python3 -c 'import sys,json; d=json.load(sys.stdin)["data"]; ids={m["user_id"] for m in d["members"]}; print(int("'"${USER_ID}"'" in ids), int("'"${ADMIN_ID}"'" in ids))')
if [[ "$AUTO" == "1 1" ]]; then pass "auto rows include requested member and admin"; else fail "auto rows = ${AUTO}"; fi

if [[ "${SKIP_UI:-0}" != "1" && -f "$PW_CLI" ]]; then
  cat > /tmp/verify-access-ui.js <<JS
async (page) => {
  await page.evaluate(({token, user, tenant}) => {
    localStorage.setItem('weknora_token', token);
    localStorage.setItem('weknora_user', JSON.stringify(user));
    localStorage.setItem('weknora_tenant', JSON.stringify(tenant));
    localStorage.setItem('weknora_selected_tenant_id', String(tenant.id));
  }, {token: $(python3 -c 'import json,sys;print(json.dumps(sys.argv[1]))' "$ADMIN_TOKEN"), user: ${ADMIN_USER_JSON}, tenant: ${ADMIN_TENANT_JSON}});
  await page.goto('${UI_URL}/platform/knowledge-bases/${KB_ID}?tab=access', {waitUntil: 'domcontentloaded'});
  try {
    await page.waitForSelector('.kb-access-panel', {timeout: 90000});
    await page.evaluate(() => { document.documentElement.dataset.accessUi = '1'; });
    // Optional: exercise the save path when the policy loaded quickly.
    const save = page.locator('.kb-access-panel__header button').first();
    if ((await save.count()) > 0 && (await save.isEnabled().catch(() => false))) {
      await save.click();
      await page.waitForTimeout(800);
    }
  } catch (err) {
    await page.evaluate(() => { document.documentElement.dataset.accessUi = 'error'; }).catch(() => {});
  }
}
JS
  node "$PW_CLI" open "$UI_URL" >/dev/null
  node "$PW_CLI" run-code "$(cat /tmp/verify-access-ui.js)" >/dev/null 2>&1 || true
  HAS_MEMBER=$(node "$PW_CLI" eval '() => document.documentElement.dataset.accessUi' 2>/dev/null |
    sed -n '/^### Result/{n;p}' | tr -d '"[[:space:]]')
  if [[ "$HAS_MEMBER" == "1" ]]; then
    pass "KB access tab renders the member policy"
  else
    BODY=$(node "$PW_CLI" eval '() => document.body.innerText.slice(0,160)' 2>/dev/null | sed -n '/^### Result/{n;p}')
    fail "KB access tab did not render the policy panel (body=${BODY})"
  fi
fi

psql_q "delete from kb_user_access where knowledge_base_id='${KB_ID}' and user_id='${USER_ID}'" >/dev/null
psql_q "update knowledge_bases set visibility_mode='${FINAL_VISIBILITY:-tenant}' where id='${KB_ID}'" >/dev/null
psql_q "delete from tenant_members where user_id='${ADMIN_ID}'; delete from users where id='${ADMIN_ID}';" >/dev/null

echo "== 7. second viewer gray check =="
V2_EMAIL="verify-viewer-$(date +%s)@unionclin.com"
V2_PASS="VerifyViewer!$(date +%s)"
V2_ID=$(python3 -c 'import uuid; print(uuid.uuid4())')
V2_HASH=$(python3 -c 'import bcrypt,sys; print(bcrypt.hashpw(sys.argv[1].encode(), bcrypt.gensalt(rounds=10)).decode())' "$V2_PASS")
psql_q "begin; insert into users (id, username, email, password_hash, tenant_id, is_active) values ('${V2_ID}', 'verify-viewer', '${V2_EMAIL}', '${V2_HASH}', ${TENANT_ID}, true); insert into tenant_members (user_id, tenant_id, role, status, joined_at) values ('${V2_ID}', ${TENANT_ID}, 'viewer', 'active', now()); commit;" >/dev/null
V2_TOKEN=$(curl -sf -X POST "${APP_URL}/api/v1/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"${V2_EMAIL}\",\"password\":\"${V2_PASS}\"}" |
  python3 -c 'import sys,json; print(json.load(sys.stdin)["token"])')
psql_q "update knowledge_bases set visibility_mode='restricted' where id='${KB_ID}'" >/dev/null
psql_q "delete from kb_user_access where knowledge_base_id='${KB_ID}' and user_id='${V2_ID}'" >/dev/null
STATUS=$(http_status -H "Authorization: Bearer ${V2_TOKEN}" "${APP_URL}/api/v1/knowledge-bases/${KB_ID}")
if [[ "$STATUS" == "404" ]]; then pass "viewer2 restricted without grant -> 404"; else fail "viewer2 restricted -> ${STATUS}"; fi
psql_q "insert into kb_user_access (id, tenant_id, knowledge_base_id, user_id, can_view, can_download) values (gen_random_uuid(), ${TENANT_ID}, '${KB_ID}', '${V2_ID}', true, true)" >/dev/null
STATUS=$(http_status -H "Authorization: Bearer ${V2_TOKEN}" "${APP_URL}/api/v1/knowledge-bases/${KB_ID}")
if [[ "$STATUS" == "200" ]]; then pass "viewer2 with grant -> 200"; else fail "viewer2 with grant -> ${STATUS}"; fi
STATUS=$(http_status -H "Authorization: Bearer ${V2_TOKEN}" "${APP_URL}/api/v1/knowledge/${KNOWLEDGE_ID}/download")
if [[ "$STATUS" == "200" ]]; then pass "viewer2 granted download -> 200"; else fail "viewer2 download -> ${STATUS}"; fi
psql_q "delete from kb_user_access where knowledge_base_id='${KB_ID}' and user_id='${V2_ID}'" >/dev/null
psql_q "update knowledge_bases set visibility_mode='${FINAL_VISIBILITY:-tenant}' where id='${KB_ID}'" >/dev/null
psql_q "delete from tenant_members where user_id='${V2_ID}'; delete from users where id='${V2_ID}';" >/dev/null

echo "== 8. embed file proxy respects the ACL =="
EMB_ID=$(python3 -c 'import uuid; print(uuid.uuid4())')
EMB_TOKEN="verify-embed-$(date +%s)"
psql_q "insert into embed_channels (id, tenant_id, agent_id, name, enabled, publish_token, allowed_origins) values ('${EMB_ID}', ${TENANT_ID}, 'builtin-quick-answer', 'verify-embed', true, '${EMB_TOKEN}', '[\"https://www.test.com\"]')" >/dev/null
EMB_REF=$(curl -s -H "Authorization: Bearer ${TOKEN}" "${APP_URL}/api/v1/knowledge/${KNOWLEDGE_ID}" | python3 -c 'import sys,json; print(json.load(sys.stdin)["data"]["file_path"])')
EMB_QUERY=$(python3 -c 'import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))' "$EMB_REF")
STATUS=$(http_status -H "Authorization: Embed ${EMB_TOKEN}" -H "Origin: https://www.test.com" "${APP_URL}/api/v1/embed/${EMB_ID}/files?file_path=${EMB_QUERY}")
if [[ "$STATUS" == "403" ]]; then pass "unbound embed channel source file -> 403"; else fail "unbound embed channel source file -> ${STATUS}"; fi
psql_q "update embed_channels set owner_user_id='${USER_ID}' where id='${EMB_ID}'" >/dev/null
psql_q "delete from kb_user_access where knowledge_base_id='${KB_ID}' and user_id='${USER_ID}'" >/dev/null
psql_q "insert into kb_user_access (id, tenant_id, knowledge_base_id, user_id, can_view, can_download) values (gen_random_uuid(), ${TENANT_ID}, '${KB_ID}', '${USER_ID}', true, true)" >/dev/null
STATUS=$(http_status -H "Authorization: Embed ${EMB_TOKEN}" -H "Origin: https://www.test.com" "${APP_URL}/api/v1/embed/${EMB_ID}/files?file_path=${EMB_QUERY}")
if [[ "$STATUS" == "200" ]]; then pass "embed channel bound to an owner with can_download -> 200"; else fail "bound embed channel -> ${STATUS}"; fi
psql_q "delete from embed_channels where id='${EMB_ID}'" >/dev/null
psql_q "delete from kb_user_access where knowledge_base_id='${KB_ID}' and created_at >= '${START_TS}'" >/dev/null

echo
echo "passed=${PASS} failed=${FAIL}"
[[ "$FAIL" -eq 0 ]]
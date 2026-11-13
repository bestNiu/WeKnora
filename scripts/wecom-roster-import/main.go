// Command wecom-roster-import provisions WeCom members from a roster CSV.
//
// It solves one operational problem: a list of names (plus whatever permission
// column the business keeps) has to become local accounts, workspace
// memberships, and per-knowledge-base grants — keyed by WeCom member account
// (userid), which the roster does not carry. So the directory is the source of
// truth for "name -> userid", and everything else is derived from it.
//
// It deliberately does NOT touch the database: it prints one idempotent SQL
// script, which an operator reviews and applies. Rerunning it is safe — every
// statement guards on existing rows.
//
// Usage:
//
//	go run ./scripts/wecom-roster-import \
//	  -roster wecom-roster.csv \
//	  -kb "BD竞标" \
//	  -restrict "测试,edoc-connector-smoke" \
//	  -tenant 10000 \
//	  -out /tmp/roster.sql
//
// Then: docker exec -i WeKnora-postgres psql -U postgres -d WeKnora -f - < /tmp/roster.sql
package main

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const apiBase = "https://qyapi.weixin.qq.com"

type directoryEntry struct {
	UserID string
	Name   string
}

func main() {
	rosterPath := flag.String("roster", "wecom-roster.csv", "roster CSV: 姓名,权限标注")
	kbName := flag.String("kb", "BD竞标", "knowledge base the roster may read and download")
	restrict := flag.String("restrict", "", "comma-separated KB names to switch to restricted")
	tenantID := flag.Uint64("tenant", 10000, "workspace the members join")
	role := flag.String("role", "viewer", "tenant role granted to each member")
	outPath := flag.String("out", "", "SQL output path (default stdout)")
	envFile := flag.String("envfile", ".env", "fallback file for the WeCom credentials")
	flag.Parse()

	// The repo's `go` wrapper runs the tool inside a container that forwards
	// only a fixed env list, so credentials normally come from .env instead of
	// the process environment. (Keeping the secret out of argv is the point.)
	corpID := strings.TrimSpace(os.Getenv("WECOM_AUTH_CORP_ID"))
	secret := strings.TrimSpace(os.Getenv("WECOM_AUTH_AGENT_SECRET"))
	if corpID == "" || secret == "" {
		values, err := readEnvFile(*envFile)
		if err != nil {
			fatalf("read %s: %v", *envFile, err)
		}
		if corpID == "" {
			corpID = values["WECOM_AUTH_CORP_ID"]
		}
		if secret == "" {
			secret = values["WECOM_AUTH_AGENT_SECRET"]
		}
	}
	if corpID == "" || secret == "" {
		fatalf("WECOM_AUTH_CORP_ID / WECOM_AUTH_AGENT_SECRET not found in env or %s", *envFile)
	}

	roster, err := readRoster(*rosterPath)
	if err != nil {
		fatalf("read roster: %v", err)
	}
	dir, err := fetchDirectory(corpID, secret)
	if err != nil {
		fatalf("fetch wecom directory: %v", err)
	}

	byName := map[string][]directoryEntry{}
	for _, entry := range dir {
		key := normalizeName(entry.Name)
		byName[key] = append(byName[key], entry)
	}

	var matched []directoryEntry
	var missing, ambiguous []string
	for _, name := range roster {
		candidates := byName[normalizeName(name)]
		switch len(candidates) {
		case 1:
			matched = append(matched, candidates[0])
		case 0:
			missing = append(missing, name)
		default:
			ids := make([]string, 0, len(candidates))
			for _, c := range candidates {
				ids = append(ids, c.UserID)
			}
			ambiguous = append(ambiguous, fmt.Sprintf("%s (%s)", name, strings.Join(ids, "/")))
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].UserID < matched[j].UserID })

	var out io.Writer = os.Stdout
	if *outPath != "" {
		file, err := os.Create(*outPath)
		if err != nil {
			fatalf("create output: %v", err)
		}
		defer file.Close()
		out = file
	}

	emitSQL(out, matched, *tenantID, *role, *kbName, splitList(*restrict))

	fmt.Fprintf(os.Stderr, "\n名单 %d 人：匹配 %d，通讯录缺失 %d，重名待定 %d\n",
		len(roster), len(matched), len(missing), len(ambiguous))
	if len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "通讯录里找不到：%s\n", strings.Join(missing, "、"))
	}
	if len(ambiguous) > 0 {
		fmt.Fprintf(os.Stderr, "重名（需人工指定 userid）：%s\n", strings.Join(ambiguous, "、"))
	}
	if *outPath != "" {
		fmt.Fprintf(os.Stderr, "SQL 已写入 %s（未执行）\n", *outPath)
	}
}

func fatalf(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// readEnvFile parses the KEY=VALUE lines this tool needs. It is not a general
// dotenv implementation: quotes, exports and multi-line values are not handled,
// which is exactly what the deployment's .env contains.
func readEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return values, scanner.Err()
}

func splitList(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func readRoster(path string) ([]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var names []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name := strings.TrimSpace(strings.Split(line, ",")[0])
		if name != "" {
			names = append(names, name)
		}
	}
	return names, scanner.Err()
}

// normalizeName drops the Latin alias in parentheses ("范晓晴(Bella)") and the
// separators the directory sometimes adds, so the two lists compare cleanly.
var parenRE = regexp.MustCompile(`[（(][^）)]*[）)]`)

func normalizeName(name string) string {
	name = parenRE.ReplaceAllString(name, "")
	name = strings.ReplaceAll(name, " ", "")
	name = strings.ReplaceAll(name, "\u3000", "")
	return strings.TrimSpace(name)
}

func fetchDirectory(corpID, secret string) ([]directoryEntry, error) {
	token, err := accessToken(corpID, secret)
	if err != nil {
		return nil, err
	}

	// user/simplelist is refused for the root department (not in the app's
	// visible scope), so walk the departments the app can actually see.
	var deptResp struct {
		Department []struct {
			ID int `json:"id"`
		} `json:"department"`
	}
	if err := getJSON(fmt.Sprintf("%s/cgi-bin/department/list?access_token=%s",
		apiBase, url.QueryEscape(token)), &deptResp); err != nil {
		return nil, err
	}

	seen := map[string]directoryEntry{}
	for _, dept := range deptResp.Department {
		var listResp struct {
			UserList []struct {
				UserID string `json:"userid"`
				Name   string `json:"name"`
			} `json:"userlist"`
		}
		endpoint := fmt.Sprintf("%s/cgi-bin/user/simplelist?access_token=%s&department_id=%d&fetch_child=1",
			apiBase, url.QueryEscape(token), dept.ID)
		if err := getJSON(endpoint, &listResp); err != nil {
			// Departments outside the visible scope answer 60011; skipping them
			// is expected and keeps the import going.
			fmt.Fprintf(os.Stderr, "跳过部门 %d: %v\n", dept.ID, err)
			continue
		}
		for _, user := range listResp.UserList {
			if user.UserID == "" {
				continue
			}
			seen[user.UserID] = directoryEntry{UserID: user.UserID, Name: user.Name}
		}
	}

	entries := make([]directoryEntry, 0, len(seen))
	for _, entry := range seen {
		entries = append(entries, entry)
	}
	return entries, nil
}

func accessToken(corpID, secret string) (string, error) {
	var resp struct {
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
		AccessToken string `json:"access_token"`
	}
	endpoint := fmt.Sprintf("%s/cgi-bin/gettoken?corpid=%s&corpsecret=%s",
		apiBase, url.QueryEscape(corpID), url.QueryEscape(secret))
	if err := getJSON(endpoint, &resp); err != nil {
		return "", err
	}
	if resp.AccessToken == "" {
		return "", fmt.Errorf("gettoken returned no access_token: %s", resp.ErrMsg)
	}
	return resp.AccessToken, nil
}

func getJSON(endpoint string, out interface{}) error {
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(endpoint)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	var envelope struct {
		ErrCode int    `json:"errcode"`
		ErrMsg  string `json:"errmsg"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return err
	}
	if envelope.ErrCode != 0 {
		return fmt.Errorf("errcode %d: %s", envelope.ErrCode, envelope.ErrMsg)
	}
	return json.Unmarshal(body, out)
}

// emitSQL writes one DO block per member. Every block is idempotent: rerunning
// the script after a partial rollout only fills the gaps.
func emitSQL(w io.Writer, members []directoryEntry, tenantID uint64, role, kbName string, restrict []string) {
	fmt.Fprintf(w, "-- Generated by scripts/wecom-roster-import at %s\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(w, "-- tenant=%d role=%s kb=%s members=%d\n\n", tenantID, role, kbName, len(members))

	for _, member := range members {
		hash, err := bcrypt.GenerateFromPassword([]byte(randomSecret()), bcrypt.DefaultCost)
		if err != nil {
			fatalf("bcrypt: %v", err)
		}
		fmt.Fprintf(w, "-- %s (%s)\n", member.Name, member.UserID)
		fmt.Fprintf(w, `DO $$
DECLARE
  uid     text;
  uname   text := %s;
  uemail  text := %s;
  uhash   text := %s;
  kbid    text;
  attempts int := 0;
BEGIN
  SELECT id INTO kbid FROM knowledge_bases WHERE tenant_id = %d AND name = %s LIMIT 1;
  IF kbid IS NULL THEN
    RAISE EXCEPTION 'knowledge base %% not found', %s;
  END IF;

  SELECT user_id INTO uid FROM user_identities WHERE provider = 'wecom' AND subject = %s;
  IF uid IS NULL THEN
    WHILE EXISTS (SELECT 1 FROM users WHERE username = uname AND deleted_at IS NULL) AND attempts < 5 LOOP
      attempts := attempts + 1;
      uname := %s || '-' || attempts::text;
    END LOOP;
    uid := gen_random_uuid()::text;
    INSERT INTO users (id, username, email, password_hash, tenant_id, is_active, created_at, updated_at, preferences)
    VALUES (uid, uname, uemail, uhash, %d, true, now(), now(), '{}'::jsonb);
    INSERT INTO user_identities (id, user_id, provider, subject, created_at, updated_at)
    VALUES (gen_random_uuid()::text, uid, 'wecom', %s, now(), now());
  END IF;

  IF NOT EXISTS (SELECT 1 FROM tenant_members
                  WHERE user_id = uid AND tenant_id = %d AND deleted_at IS NULL) THEN
    INSERT INTO tenant_members (user_id, tenant_id, role, status, joined_at, created_at, updated_at)
    VALUES (uid, %d, %s, 'active', now(), now(), now());
  END IF;

  IF EXISTS (SELECT 1 FROM kb_user_access
              WHERE knowledge_base_id = kbid AND user_id = uid AND deleted_at IS NULL) THEN
    UPDATE kb_user_access SET can_view = true, can_download = true, updated_at = now()
     WHERE knowledge_base_id = kbid AND user_id = uid AND deleted_at IS NULL;
  ELSE
    INSERT INTO kb_user_access (id, tenant_id, knowledge_base_id, user_id, can_view, can_download, created_at, updated_at)
    VALUES (gen_random_uuid()::text, %d, kbid, uid, true, true, now(), now());
  END IF;
END $$;

`,
			quote(member.Name), quote(member.UserID+"@wecom.invalid"), quote(string(hash)),
			tenantID, quote(kbName), quote(kbName),
			quote(member.UserID), quote(member.Name),
			tenantID, quote(member.UserID),
			tenantID, tenantID, quote(role),
			tenantID)
	}

	fmt.Fprintf(w, "-- 知识库可见性：名单只开放 %s，其余同名库收紧\n", kbName)
	names := append([]string{kbName}, restrict...)
	for _, name := range names {
		fmt.Fprintf(w, "UPDATE knowledge_bases SET visibility_mode = 'restricted' WHERE tenant_id = %d AND name = %s;\n",
			tenantID, quote(name))
	}
}

func quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func randomSecret() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		fatalf("random: %v", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
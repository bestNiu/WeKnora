package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	listDocumentsDefaultPageSize = 20
	listDocumentsMaxPageSize     = 100
)

var listDocumentsTool = BaseTool{
	name: ToolListDocuments,
	description: "List the documents inside one knowledge base, newest first, with pagination and an optional " +
		"title filter.\nUse it to see what a knowledge base contains or to find a document by name; each entry " +
		"carries the doc://N handle that read_document accepts. When you list documents for the user, link " +
		"each one as [title](doc://N) with that exact handle so the reader can open it.\n" +
		"Pass include_folders=true on the first page to see the folder tree with document counts; pass folder " +
		"with a path or a folder name (e.g. \"乳腺癌\") to list only that folder and its subfolders.",
	schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "knowledge_base_id": {
      "type": "string",
      "description": "kb://N knowledge-base handle from the runtime context",
      "minLength": 1
    },
    "keyword": {
      "type": "string",
      "description": "Optional substring to match against document titles and file names"
    },
    "folder": {
      "type": "string",
      "description": "Optional folder path from the folder tree, or a folder name such as \"乳腺癌\"; includes its subfolders"
    },
    "include_folders": {
      "type": "boolean",
      "description": "Include the folder tree with document counts (page 1 only)"
    },
    "page": {
      "type": "integer",
      "description": "1-based page number (default 1)",
      "minimum": 1
    },
    "page_size": {
      "type": "integer",
      "description": "Documents per page (default 20, max 100)",
      "minimum": 1,
      "maximum": 100
    }
  },
  "required": ["knowledge_base_id"]
}`),
}

// ListDocumentsInput defines the input parameters for list_documents.
type ListDocumentsInput struct {
	KnowledgeBaseID string `json:"knowledge_base_id"`
	Keyword         string `json:"keyword,omitempty"`
	Folder          string `json:"folder,omitempty"`
	IncludeFolders  bool   `json:"include_folders,omitempty"`
	Page            int    `json:"page,omitempty"`
	PageSize        int    `json:"page_size,omitempty"`
}

// ListDocumentsTool pages through the documents of one knowledge base.
type ListDocumentsTool struct {
	BaseTool
	knowledgeService interfaces.KnowledgeService
	searchTargets    types.SearchTargets
}

// NewListDocumentsTool creates a new list_documents tool.
func NewListDocumentsTool(
	knowledgeService interfaces.KnowledgeService, searchTargets types.SearchTargets,
) *ListDocumentsTool {
	return &ListDocumentsTool{
		BaseTool:         listDocumentsTool,
		knowledgeService: knowledgeService,
		searchTargets:    searchTargets,
	}
}

// Execute lists documents, honouring any document/tag scope pinned on the KB.
func (t *ListDocumentsTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	var input ListDocumentsInput
	if err := json.Unmarshal(args, &input); err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("Failed to parse args: %v", err)}, err
	}
	kbID := strings.TrimSpace(input.KnowledgeBaseID)
	if kbID == "" {
		return &types.ToolResult{Success: false, Error: "knowledge_base_id is required"},
			fmt.Errorf("missing knowledge_base_id")
	}
	if err := validateKnowledgeBaseIDsInSearchTargets(t.searchTargets, []string{kbID}); err != nil {
		return &types.ToolResult{Success: false, Error: err.Error()}, err
	}
	if t.knowledgeService == nil {
		return &types.ToolResult{Success: false, Error: "knowledge service is unavailable"},
			fmt.Errorf("service unavailable")
	}

	page := input.Page
	if page < 1 {
		page = 1
	}
	pageSize := input.PageSize
	if pageSize < 1 {
		pageSize = listDocumentsDefaultPageSize
	}
	if pageSize > listDocumentsMaxPageSize {
		pageSize = listDocumentsMaxPageSize
	}
	keyword := strings.TrimSpace(input.Keyword)
	folder, folderRows, err := t.resolveFolder(ctx, kbID, input.Folder, input.IncludeFolders && page == 1)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to resolve folder: %v", err)}, err
	}

	filtered, total, err := t.listScoped(ctx, kbID, keyword, folder, page, pageSize)
	if err != nil {
		return &types.ToolResult{Success: false, Error: fmt.Sprintf("failed to list documents: %v", err)}, err
	}

	documents := make([]map[string]interface{}, 0, len(filtered))
	var b strings.Builder
	includeFolders := input.IncludeFolders && page == 1
	if includeFolders {
		b.WriteString(renderFolderTree(folderRows))
	}
	fmt.Fprintf(&b, "<documents knowledge_base_id=\"%s\" total=\"%d\" page=\"%d\" page_size=\"%d\"",
		xmlEscape(kbID), total, page, pageSize)
	if keyword != "" {
		fmt.Fprintf(&b, " keyword=\"%s\"", xmlEscape(keyword))
	}
	if folder != "" {
		fmt.Fprintf(&b, " folder=\"%s\"", xmlEscape(folder))
	}
	b.WriteString(">\n")
	for _, k := range filtered {
		fmt.Fprintf(&b, "<document knowledge_id=\"%s\" title=\"%s\"", xmlEscape(k.ID), xmlEscape(k.Title))
		if k.FileType != "" {
			fmt.Fprintf(&b, " file_type=\"%s\"", xmlEscape(k.FileType))
		}
		if k.FolderPath != "" {
			fmt.Fprintf(&b, " folder=\"%s\"", xmlEscape(k.FolderPath))
		}
		// App URL for the document (drawer + preview). A relative link survives the
		// chat sanitizer, opens in a new tab, and lands on the KB page with this
		// document opened, which is what "出处" needs to be clickable.
		fmt.Fprintf(&b, " link=\"/platform/knowledge-bases/%s?knowledge_id=%s\"",
			xmlEscape(kbID), xmlEscape(k.ID))
		if k.ParseStatus != "" {
			fmt.Fprintf(&b, " parse_status=\"%s\"", xmlEscape(formatParseStatus(k.ParseStatus)))
		}
		if !k.UpdatedAt.IsZero() {
			fmt.Fprintf(&b, " updated_at=\"%s\"", k.UpdatedAt.Format("2006-01-02"))
		}
		if k.Description != "" {
			fmt.Fprintf(&b, ">%s</document>\n", xmlEscape(k.Description))
		} else {
			b.WriteString(" />\n")
		}
		doc := map[string]interface{}{
			"knowledge_id": k.ID,
			"weknora_uri":  "weknora://doc/" + k.ID,
			"app_url":      "/platform/knowledge-bases/" + kbID + "?knowledge_id=" + k.ID,
			"title":        k.Title,
			"description":  k.Description,
			"type":         k.Type,
			"source":       k.Source,
			"file_name":    k.FileName,
			"file_type":    k.FileType,
			"file_size":    k.FileSize,
			"folder_path":  k.FolderPath,
			"parse_status": k.ParseStatus,
			"is_faq":       false,
		}
		if !k.UpdatedAt.IsZero() {
			doc["updated_at"] = k.UpdatedAt.Format("2006-01-02T15:04:05Z07:00")
		}
		documents = append(documents, doc)
	}
	b.WriteString("</documents>")

	data := map[string]interface{}{
		"display_type":      "document_info",
		"knowledge_base_id": kbID,
		"documents":         documents,
		"total_docs":        total,
		"page":              page,
		"page_size":         pageSize,
	}
	if keyword != "" {
		data["keyword"] = keyword
	}
	if folder != "" {
		data["folder"] = folder
	}
	if includeFolders {
		data["folders"] = folderRows
	}
	if int64(page*pageSize) < total {
		data["next_page"] = page + 1
	}
	output := b.String()
	if len(filtered) == 0 {
		switch {
		case folder != "" && keyword != "":
			output = fmt.Sprintf("No documents matching %q in folder %q.", keyword, folder)
		case folder != "":
			output = fmt.Sprintf("No documents in folder %q.", folder)
		case keyword != "":
			output = fmt.Sprintf("No documents matching %q in knowledge base %s.", keyword, kbID)
		case total == 0:
			output = fmt.Sprintf("Knowledge base %s has no documents.", kbID)
		}
		if hint := folderHint(folderRows); hint != "" {
			output += " Available folders: " + hint + "."
		}
	}
	return &types.ToolResult{Success: true, Output: output, Data: data}, nil
}

// resolveFolder maps the model's folder argument onto a canonical folder path
// and also loads the folder tree for hints/rendering. An empty argument means
// the whole knowledge base and returns the tree only when it was asked for.
func (t *ListDocumentsTool) resolveFolder(
	ctx context.Context, kbID, raw string, needTree bool,
) (string, []knowledgeFolderRow, error) {
	if strings.TrimSpace(raw) == "" && !needTree {
		return "", nil, nil
	}
	listCtx := contextForKB(ctx, t.searchTargets, kbID)
	if _, ok := types.TenantIDFromContext(listCtx); !ok {
		return types.NormalizeKnowledgeFolderPath(raw), nil, nil
	}
	tree, err := t.knowledgeService.ListKnowledgeFolderTree(listCtx, kbID)
	if err != nil {
		return "", nil, err
	}
	path, rows := resolveKnowledgeFolder(tree, raw)
	return path, rows, nil
}

// renderFolderTree renders the folder projection the model uses to choose a
// folder. It is emitted once, before the first page of documents.
func renderFolderTree(rows []knowledgeFolderRow) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<folders total=\"%d\">\n", len(rows))
	for _, row := range rows {
		fmt.Fprintf(&b, "<folder path=\"%s\" name=\"%s\" direct=\"%d\" total=\"%d\" />\n",
			xmlEscape(row.Path), xmlEscape(row.Name), row.Direct, row.Total)
	}
	b.WriteString("</folders>\n")
	return b.String()
}

// listDocumentsScopeCap bounds how many tagged documents are loaded when a
// turn pins both explicit documents and tags on the same knowledge base, the
// one case where the union has to be paged in memory.
const listDocumentsScopeCap = 1000

// listScoped returns one page of the documents the turn may read, with a
// total that counts only those documents. A pinned @tag scope is pushed into
// the database filter; pinned @file documents are loaded directly. Filtering
// after paging would report totals for the whole base and leave holes in the
// page.
func (t *ListDocumentsTool) listScoped(
	ctx context.Context, kbID, keyword, folder string, page, pageSize int,
) ([]*types.Knowledge, int64, error) {
	tenantID := t.searchTargets.GetTenantIDForKB(kbID)
	// Documents live under the knowledge base owner, which may differ from the
	// caller's tenant for organization-shared bases.
	listCtx := contextForKB(ctx, t.searchTargets, kbID)
	folderFilter := types.KnowledgeListFilter{FolderPath: folder, FolderScope: types.FolderScopeSubtree}
	listPage := func(filter types.KnowledgeListFilter, p, size int) ([]*types.Knowledge, int64, error) {
		filter.Keyword = keyword
		result, err := t.knowledgeService.ListPagedKnowledgeByKnowledgeBaseID(
			listCtx, kbID, &types.Pagination{Page: p, PageSize: size}, filter,
		)
		if err != nil || result == nil {
			return nil, 0, err
		}
		rows, _ := result.Data.([]*types.Knowledge)
		return rows, result.Total, nil
	}

	var explicitIDs, tagIDs []string
	for _, target := range t.searchTargets {
		if target == nil || target.KnowledgeBaseID != kbID {
			continue
		}
		if searchTargetIsWholeKB(target) {
			return listPage(folderFilter, page, pageSize)
		}
		ids, tags := searchTargetScope(target)
		explicitIDs = append(explicitIDs, ids...)
		tagIDs = append(tagIDs, tags...)
	}
	explicitIDs = dedupNonEmptyStrings(explicitIDs)
	tagIDs = dedupNonEmptyStrings(tagIDs)

	if len(explicitIDs) == 0 {
		if len(tagIDs) == 0 {
			return nil, 0, nil
		}
		filter := folderFilter
		filter.TagIDs = tagIDs
		return listPage(filter, page, pageSize)
	}

	// Explicit documents (plus any tagged ones) form a small union that is
	// paged in memory with the same newest-first order as the database.
	union := make(map[string]*types.Knowledge)
	explicit, err := t.knowledgeService.GetKnowledgeBatch(listCtx, tenantID, explicitIDs)
	if err != nil {
		return nil, 0, err
	}
	for _, k := range explicit {
		if k != nil && k.KnowledgeBaseID == kbID && knowledgeMatchesKeyword(k, keyword) &&
			knowledgeInFolder(k.FolderPath, folder) {
			union[k.ID] = k
		}
	}
	if len(tagIDs) > 0 {
		filter := folderFilter
		filter.TagIDs = tagIDs
		tagged, _, err := listPage(filter, 1, listDocumentsScopeCap)
		if err != nil {
			return nil, 0, err
		}
		for _, k := range tagged {
			if k != nil {
				union[k.ID] = k
			}
		}
	}
	all := make([]*types.Knowledge, 0, len(union))
	for _, k := range union {
		all = append(all, k)
	}
	sort.Slice(all, func(i, j int) bool {
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID < all[j].ID
	})
	total := int64(len(all))
	start := (page - 1) * pageSize
	if start >= len(all) {
		return nil, total, nil
	}
	end := start + pageSize
	if end > len(all) {
		end = len(all)
	}
	return all[start:end], total, nil
}

// knowledgeMatchesKeyword mirrors the list filter's title / file name match.
func knowledgeMatchesKeyword(k *types.Knowledge, keyword string) bool {
	if keyword == "" {
		return true
	}
	needle := strings.ToLower(keyword)
	return strings.Contains(strings.ToLower(k.Title), needle) ||
		strings.Contains(strings.ToLower(k.FileName), needle)
}

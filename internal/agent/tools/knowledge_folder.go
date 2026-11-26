package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	// folderListMaxPaths bounds the folder tree a tool echoes back to the
	// model. A knowledge base with hundreds of leaf folders would otherwise
	// spend the whole tool budget on navigation.
	folderListMaxPaths = 80
	// folderIDPageSize / folderIDMaxPages bound the folder-to-document
	// resolution used to scope a search. The folder filter is pushed into the
	// list query, so this only pages through the IDs, not the chunks.
	folderIDPageSize = 200
	folderIDMaxPages = 10
)

// knowledgeFolderRow is one folder in the flat projection tools render. Direct
// counts only the documents stored in this folder; Total includes descendants.
type knowledgeFolderRow struct {
	Path   string
	Name   string
	Direct int64
	Total  int64
}

// flattenKnowledgeFolders walks a folder tree depth-first, parents before
// children. Folders with no documents anywhere below them are dropped.
func flattenKnowledgeFolders(tree *types.KnowledgeFolderTree, limit int) []knowledgeFolderRow {
	if tree == nil {
		return nil
	}
	rows := make([]knowledgeFolderRow, 0, len(tree.Folders))
	var walk func(nodes []*types.KnowledgeFolderNode)
	walk = func(nodes []*types.KnowledgeFolderNode) {
		for _, node := range nodes {
			if node == nil || node.TotalCount <= 0 {
				continue
			}
			if limit > 0 && len(rows) >= limit {
				return
			}
			rows = append(rows, knowledgeFolderRow{
				Path:   node.Path,
				Name:   node.Name,
				Direct: node.DocumentCount,
				Total:  node.TotalCount,
			})
			walk(node.Children)
		}
	}
	walk(tree.Folders)
	return rows
}

// resolveKnowledgeFolder maps what the model typed ("乳腺癌", "募小助摘要",
// or a full path) onto a canonical folder_path. It prefers, in order: an exact
// path, a folder whose display name matches, and finally the shallowest folder
// whose path contains the term. The second return is every folder, for hints.
func resolveKnowledgeFolder(
	tree *types.KnowledgeFolderTree, raw string,
) (string, []knowledgeFolderRow) {
	rows := flattenKnowledgeFolders(tree, folderListMaxPaths)
	normalized := types.NormalizeKnowledgeFolderPath(raw)
	if normalized == "" {
		return "", rows
	}
	// Exact path first: the model is expected to copy a path from the folder
	// tree, and that must win over a looser name match.
	for _, row := range rows {
		if row.Path == normalized {
			return row.Path, rows
		}
	}
	lower := strings.ToLower(normalized)
	for _, row := range rows {
		if strings.ToLower(row.Name) == lower ||
			strings.HasSuffix(strings.ToLower(row.Path), "/"+lower) {
			return row.Path, rows
		}
	}
	for _, row := range rows {
		if strings.Contains(strings.ToLower(row.Path), lower) {
			return row.Path, rows
		}
	}
	// Unknown: keep the normalized term so the empty result can name it.
	return normalized, rows
}

// contextForKB re-scopes the context to the tenant that owns the knowledge
// base. Organization-shared bases run under their owner's tenant.
func contextForKB(ctx context.Context, targets types.SearchTargets, kbID string) context.Context {
	if tenantID := targets.GetTenantIDForKB(kbID); tenantID != 0 {
		return context.WithValue(ctx, types.TenantIDContextKey, tenantID)
	}
	return ctx
}

// folderHint is a short, bounded list of folder paths for an empty result.
func folderHint(rows []knowledgeFolderRow) string {
	if len(rows) == 0 {
		return ""
	}
	paths := make([]string, 0, 12)
	for _, row := range rows {
		if len(paths) >= 12 {
			break
		}
		paths = append(paths, fmt.Sprintf("%s(%d)", row.Path, row.Total))
	}
	return strings.Join(paths, "、")
}

// knowledgeInFolder reports whether one document sits in the folder subtree.
func knowledgeInFolder(folderPath, folder string) bool {
	if folder == "" {
		return true
	}
	return folderPath == folder || strings.HasPrefix(folderPath, folder+"/")
}

// intersectStrings keeps the order and values of left that also appear in
// right. Both sides are deduplicated.
func intersectStrings(left, right []string) []string {
	if len(left) == 0 || len(right) == 0 {
		return nil
	}
	want := make(map[string]struct{}, len(right))
	for _, value := range right {
		if value != "" {
			want[value] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(left))
	out := make([]string, 0, len(left))
	for _, value := range left {
		if value == "" {
			continue
		}
		if _, ok := want[value]; !ok {
			continue
		}
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

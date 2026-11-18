package repository

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// Labels are normalised once, on write: trimmed, de-duplicated case-insensitively,
// length-capped and count-capped. The panel then never shows " 医学事务" twice.
func TestNormalizeTagsTrimsDeduplicatesAndCaps(t *testing.T) {
	got := normalizeTags([]string{" 医学事务 ", "医学事务", "BD", "", "bd", "   "})
	require.Equal(t, []string{"BD", "医学事务"}, got, "case-insensitive dedupe keeps the first spelling")

	long := make([]rune, types.KBMemberTagMaxLength+20)
	for i := range long {
		long[i] = '标'
	}
	got = normalizeTags([]string{string(long)})
	require.Len(t, []rune(got[0]), types.KBMemberTagMaxLength)

	many := make([]string, 0, types.KBMemberTagMaxPerMember+5)
	for i := 0; i < types.KBMemberTagMaxPerMember+5; i++ {
		many = append(many, string(rune('a'+i)))
	}
	require.Len(t, normalizeTags(many), types.KBMemberTagMaxPerMember)
}

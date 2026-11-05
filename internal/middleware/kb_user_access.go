package middleware

import (
	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/gin-gonic/gin"
)

// AttachKBUserVisibilityResolver installs the per-user knowledge-base ACL
// resolver on the request context.
//
// It must run after Auth (which installs the caller) and before any route that
// resolves a KB, so access.ResolveKB and access.KBPermissions see the caller's
// grants. A nil resolver is a no-op: the access layer then keeps the pre-ACL
// behaviour, which is what background and test contexts rely on.
func AttachKBUserVisibilityResolver(resolver access.KBUserVisibilityResolver) gin.HandlerFunc {
	return func(c *gin.Context) {
		if resolver != nil {
			ctx := access.WithKBUserVisibilityResolver(c.Request.Context(), resolver)
			c.Request = c.Request.WithContext(ctx)
		}
		c.Next()
	}
}

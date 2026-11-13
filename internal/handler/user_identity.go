package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
)

// userIdentityBindItem is one binding in a bulk request.
type userIdentityBindItem struct {
	UserID   string `json:"user_id"`
	Subject  string `json:"subject"`
	Provider string `json:"provider"`
}

// BindUserIdentities links WeCom member userids (or any provider subject) to
// existing local accounts. Binding is what lets an already-active account keep
// its sessions and knowledge-base access when WeCom login is switched on.
//
// Bulk by design: the realistic operation is "import the HR roster once", not
// "link one person through the UI". Per-item failures are reported instead of
// aborting the batch, so a single typo does not discard 200 good rows.
func (h *SystemHandler) BindUserIdentities(c *gin.Context) {
	ctx := c.Request.Context()

	var body struct {
		Items []userIdentityBindItem `json:"items"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.Error(errors.NewValidationError("请求体格式错误"))
		return
	}
	if len(body.Items) == 0 {
		c.Error(errors.NewValidationError("items 不能为空"))
		return
	}
	if len(body.Items) > 1000 {
		c.Error(errors.NewValidationError("单次最多绑定 1000 条"))
		return
	}

	type failure struct {
		UserID  string `json:"user_id"`
		Subject string `json:"subject"`
		Error   string `json:"error"`
	}
	failures := make([]failure, 0)
	bound := 0

	for _, item := range body.Items {
		provider := strings.TrimSpace(item.Provider)
		if provider == "" {
			provider = types.IdentityProviderWeCom
		}
		userID := strings.TrimSpace(item.UserID)
		subject := strings.TrimSpace(item.Subject)
		if userID == "" || subject == "" {
			failures = append(failures, failure{UserID: userID, Subject: subject, Error: "user_id 与 subject 均不能为空"})
			continue
		}
		if err := h.userSvc.BindUserIdentity(ctx, userID, provider, subject); err != nil {
			failures = append(failures, failure{UserID: userID, Subject: subject, Error: err.Error()})
			continue
		}
		bound++
	}

	c.JSON(http.StatusOK, gin.H{
		"success":  len(failures) == 0,
		"bound":    bound,
		"failed":   len(failures),
		"failures": failures,
	})
}

package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

// IdentityStore is the external-identity surface of the user repository. It is
// declared here rather than on interfaces.UserRepository so that adding an
// identity backend does not force every existing repository fake to grow three
// methods; userService asserts against it (see userService.identities).
type IdentityStore interface {
	GetUserIdentity(ctx context.Context, provider, subject string) (*types.UserIdentity, error)
	CreateUserIdentity(ctx context.Context, identity *types.UserIdentity) error
	ListUserIdentities(ctx context.Context, userID string) ([]*types.UserIdentity, error)
}

// ErrIdentityAlreadyBound reports a duplicate (provider, subject) binding.
var ErrIdentityAlreadyBound = errors.New("identity already bound to another user")

// GetUserIdentity resolves one external identity to its local binding. It
// returns (nil, nil) when the subject is not bound yet: "not linked" is a
// normal state for a WeCom member who has never signed in, not an error.
func (r *userRepository) GetUserIdentity(
	ctx context.Context, provider, subject string,
) (*types.UserIdentity, error) {
	provider = strings.TrimSpace(provider)
	subject = strings.TrimSpace(subject)
	if provider == "" || subject == "" {
		return nil, nil
	}
	var identity types.UserIdentity
	err := r.db.WithContext(ctx).
		Where("provider = ? AND subject = ?", provider, subject).
		First(&identity).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &identity, nil
}

// CreateUserIdentity binds an external identity to a local user. A duplicate
// (provider, subject) returns ErrIdentityAlreadyBound so the caller can retry
// the lookup instead of surfacing a raw constraint error.
func (r *userRepository) CreateUserIdentity(
	ctx context.Context, identity *types.UserIdentity,
) error {
	if identity == nil {
		return errors.New("identity is required")
	}
	if strings.TrimSpace(identity.ID) == "" {
		identity.ID = uuid.NewString()
	}
	now := time.Now()
	if identity.CreatedAt.IsZero() {
		identity.CreatedAt = now
	}
	identity.UpdatedAt = now
	// Pre-check inside the transaction so the common "second login raced the
	// first" case returns the sentinel instead of a raw 23505 /
	// SQLITE_CONSTRAINT string. The unique index stays authoritative.
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var probe types.UserIdentity
		err := tx.Where("provider = ? AND subject = ?", identity.Provider, identity.Subject).
			First(&probe).Error
		switch {
		case err == nil:
			return ErrIdentityAlreadyBound
		case !errors.Is(err, gorm.ErrRecordNotFound):
			return err
		}
		return tx.Create(identity).Error
	})
}

// ListUserIdentities returns every external identity bound to one local user.
func (r *userRepository) ListUserIdentities(
	ctx context.Context, userID string,
) ([]*types.UserIdentity, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, nil
	}
	var identities []*types.UserIdentity
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("provider, subject").
		Find(&identities).Error; err != nil {
		return nil, err
	}
	return identities, nil
}
package identity

import "github.com/a-jay98/stockpot/internal/settings"

// Identity settings, edited in the admin panel.
var (
	SessionDays = settings.Define(settings.Def[int]{
		Key:         "auth.session_days",
		Default:     30,
		Validate:    settings.Between(1, 365),
		Description: "Days a session stays signed in without use; each use extends it.",
	})
	InviteRequiresApproval = settings.Define(settings.Def[bool]{
		Key:         "auth.invite_requires_approval",
		Default:     true,
		Description: "New users who accept an invite wait for an admin to approve them.",
	})
	InviteDays = settings.Define(settings.Def[int]{
		Key:         "auth.invite_days",
		Default:     7,
		Validate:    settings.Between(1, 90),
		Description: "Days an invite link stays valid.",
	})
	RetentionExpiredAuthDays = settings.Define(settings.Def[int]{
		Key:         "retention.expired_auth_days",
		Default:     30,
		Validate:    settings.Between(1, 3650),
		Description: "Days to keep expired or revoked sessions and expired invites before deleting them.",
	})
)

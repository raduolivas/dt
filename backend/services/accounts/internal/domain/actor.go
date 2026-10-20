package domain

// Role is a console role within a tenant.
type Role string

const (
	RoleOwner     Role = "owner"
	RoleAdmin     Role = "admin"
	RoleDeveloper Role = "developer"
	RoleSupport   Role = "support"
)

// Actor is who performs an action.
type Actor struct {
	Type string // "console_user" | "system"
	ID   string
	Role Role
}

// CanManage reports whether the actor may create, roll or revoke keys in env.
// Owner and admin: both environments; developer: test only; support: never.
func CanManage(a Actor, env Environment) error {
	switch env {
	case EnvTest:
		if a.Role == RoleOwner || a.Role == RoleAdmin || a.Role == RoleDeveloper {
			return nil
		}
	case EnvLive:
		if a.Role == RoleOwner || a.Role == RoleAdmin {
			return nil
		}
	}
	return ErrForbiddenRole
}

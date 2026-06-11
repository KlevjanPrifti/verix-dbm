package web

// Per-connection access resolution. In the default (non-scoped) mode a user's
// global role applies to every connection, exactly as before. In scoped mode
// (DBM_SCOPED_ACCESS=true) a non-admin user reaches a connection only through a
// grant, and a grant scopes WHERE they act without ever raising WHAT they may do
// above their global capability. Global admins bypass scoping entirely.

import (
	"context"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/store"
)

// ConnAccess is a user's effective capability on one connection.
type ConnAccess struct {
	Read  bool
	Write bool
}

// ResolveConnAccess computes effective access from the user's global capability
// and the highest-ranked grant they hold on the connection (nil if none). It is
// pure so the policy can be unit-tested exhaustively.
//
//   - not scoped, or global admin -> global capability applies (legacy behaviour)
//   - scoped, no grant            -> no access
//   - scoped, read grant          -> read, iff the user has global read
//   - scoped, write grant         -> read + write, each capped by global capability
func ResolveConnAccess(u auth.User, grant *store.Grant, scoped bool) ConnAccess {
	if !scoped || u.Admin {
		return ConnAccess{Read: u.Read, Write: u.Write}
	}
	if grant == nil {
		return ConnAccess{}
	}
	out := ConnAccess{Read: u.Read}
	if grant.Level == store.GrantWrite {
		out.Write = u.Write
	}
	return out
}

// access resolves the caller's effective access to a connection, querying grants
// only when scoping is on and the user is not a global admin.
func (s *Server) access(ctx context.Context, u auth.User, c store.Connection) ConnAccess {
	scoped := s.cfg.ScopedAccess
	var grant *store.Grant
	if scoped && !u.Admin {
		grant, _ = s.st.GrantForSubjects(ctx, c.ID, u.Subjects())
	}
	return ResolveConnAccess(u, grant, scoped)
}

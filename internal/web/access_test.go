package web

import (
	"testing"

	"verix-dbm/internal/auth"
	"verix-dbm/internal/store"
)

// TestResolveConnAccess exhaustively covers the per-connection access policy:
// the (scoped, global capability, grant) combinations that decide read/write.
func TestResolveConnAccess(t *testing.T) {
	read := func() auth.User { return auth.User{Read: true} }
	write := func() auth.User { return auth.User{Read: true, Write: true} }
	admin := func() auth.User { return auth.User{Read: true, Write: true, Admin: true} }
	gRead := &store.Grant{Level: store.GrantRead}
	gWrite := &store.Grant{Level: store.GrantWrite}

	cases := []struct {
		name             string
		u                auth.User
		grant            *store.Grant
		scoped           bool
		wantRead, wantWr bool
	}{
		// Non-scoped: global capability applies regardless of grants.
		{"unscoped read user", read(), nil, false, true, false},
		{"unscoped write user", write(), nil, false, true, true},
		{"unscoped grant ignored", read(), gWrite, false, true, false},

		// Scoped admin: bypasses grants entirely.
		{"scoped admin no grant", admin(), nil, true, true, true},

		// Scoped non-admin, no grant: no access at all.
		{"scoped read no grant", read(), nil, true, false, false},
		{"scoped write no grant", write(), nil, true, false, false},

		// Scoped read grant: read only, capped by global capability.
		{"scoped read grant + global read", read(), gRead, true, true, false},
		{"scoped read grant + global write", write(), gRead, true, true, false},

		// Scoped write grant: write requires the user to also have global write.
		{"scoped write grant + global read only", read(), gWrite, true, true, false},
		{"scoped write grant + global write", write(), gWrite, true, true, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ResolveConnAccess(c.u, c.grant, c.scoped)
			if got.Read != c.wantRead || got.Write != c.wantWr {
				t.Errorf("ResolveConnAccess = {Read:%v Write:%v}, want {Read:%v Write:%v}",
					got.Read, got.Write, c.wantRead, c.wantWr)
			}
		})
	}
}

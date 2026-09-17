package archive

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/go-webauthn/webauthn/webauthn"
)

func TestRegistrationInviteIsHashedBoundedSingleUseAndAudited(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "invite-root", "invite root password")
	if err != nil {
		t.Fatal(err)
	}

	invite, err := s.CreateRegistrationInvite(ctx, root, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if invite.Token == "" || invite.ID == 0 || !invite.ExpiresAt.After(time.Now()) {
		t.Fatalf("invalid invite: %+v", invite)
	}
	var stored string
	if err = s.DB.QueryRowContext(ctx, `SELECT token_hash FROM registration_invites WHERE id=?`, invite.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == invite.Token || stored == hashToken(invite.Token)[:8] || len(stored) != 64 {
		t.Fatalf("invite secret was stored unsafely: %q", stored)
	}

	created, err := s.RegisterWithInvite(ctx, invite.Token, "invited-viewer", "invited viewer password")
	if err != nil {
		t.Fatal(err)
	}
	if created.Role != "viewer" {
		t.Fatalf("invite account role=%q, want viewer", created.Role)
	}
	if _, err = s.RegisterWithInvite(ctx, invite.Token, "second-viewer", "second viewer password"); !errors.Is(err, ErrInvalidRegistrationInvite) {
		t.Fatalf("reused invite error=%v, want invalid invite", err)
	}
	var consumedBy string
	if err = s.DB.QueryRowContext(ctx, `SELECT consumed_by_username FROM registration_invites WHERE id=?`, invite.ID).Scan(&consumedBy); err != nil {
		t.Fatal(err)
	}
	if consumedBy != created.Username {
		t.Fatalf("consumed invite identity=%q, want %q", consumedBy, created.Username)
	}
	var count int
	if err = s.DB.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE event_type='invite_consumed' AND target_user_id=?`, created.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("invite consumption audit count=%d, want 1", count)
	}
}

func TestRegistrationInviteAuthorizationExpiryRevocationAndRace(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "invite-policy-root", "invite policy root password")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := s.Register(ctx, "invite-policy-admin", "invite policy admin password")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetUserRole(ctx, root, admin.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	viewer, err := s.Register(ctx, "invite-policy-viewer", "invite policy viewer password")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateRegistrationInvite(ctx, admin, time.Hour); !errors.Is(err, ErrPermission) {
		t.Fatalf("ordinary admin created invite: %v", err)
	}
	if _, err = s.CreateRegistrationInvite(ctx, viewer, time.Hour); !errors.Is(err, ErrPermission) {
		t.Fatalf("viewer created invite: %v", err)
	}

	expired, err := s.CreateRegistrationInvite(ctx, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.ExecContext(ctx, `UPDATE registration_invites SET expires_at=? WHERE id=?`, time.Now().UTC().Add(-time.Second).Format(time.RFC3339Nano), expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RegisterWithInvite(ctx, expired.Token, "expired-viewer", "expired viewer password"); !errors.Is(err, ErrInvalidRegistrationInvite) {
		t.Fatalf("expired invite error=%v, want invalid invite", err)
	}
	revoked, err := s.CreateRegistrationInvite(ctx, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RevokeRegistrationInvite(ctx, root, revoked.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RegisterWithInvite(ctx, revoked.Token, "revoked-viewer", "revoked viewer password"); !errors.Is(err, ErrInvalidRegistrationInvite) {
		t.Fatalf("revoked invite error=%v, want invalid invite", err)
	}
	if err = s.RevokeRegistrationInvite(ctx, admin, revoked.ID); !errors.Is(err, ErrPermission) {
		t.Fatalf("ordinary admin revoked invite: %v", err)
	}

	traced, err := s.CreateRegistrationInvite(ctx, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, username := range []string{"race-one", "race-two"} {
		wg.Add(1)
		go func(username string) {
			defer wg.Done()
			_, consumeErr := s.RegisterWithInvite(ctx, traced.Token, username, username+" password")
			results <- consumeErr
		}(username)
	}
	wg.Wait()
	close(results)
	var successes, invalid int
	for consumeErr := range results {
		switch {
		case consumeErr == nil:
			successes++
		case errors.Is(consumeErr, ErrInvalidRegistrationInvite):
			invalid++
		default:
			t.Fatalf("unexpected concurrent invite result: %v", consumeErr)
		}
	}
	if successes != 1 || invalid != 1 {
		t.Fatalf("concurrent invite results successes=%d invalid=%d", successes, invalid)
	}
}

func TestRegistrationInvitePasskeyAccountSupportsOptionalPassword(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, err := s.BootstrapSuperAdmin(ctx, "invite-passkey-root", "invite passkey root password")
	if err != nil {
		t.Fatal(err)
	}
	invite, err := s.CreateRegistrationInvite(ctx, root, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	credential := webauthn.Credential{ID: []byte("invite-passkey-credential"), PublicKey: []byte("invite-passkey-public-key")}
	created, recovery, err := s.CreatePasskeyAccountWithInvite(ctx, invite.Token, "invite-passkey", "Invite Mac", make([]byte, 32), credential, "")
	if err != nil {
		t.Fatal(err)
	}
	if created.Role != "viewer" || recovery == "" {
		t.Fatalf("passkey invite account=%+v recovery=%q", created, recovery)
	}
	status, err := s.SecurityStatus(ctx, created.ID)
	if err != nil || status.PasswordEnabled {
		t.Fatalf("passkey-only invited account status=%+v err=%v", status, err)
	}
}

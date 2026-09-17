package web

import (
	"net/http/httptest"
	"strings"
	"testing"

	"kura/internal/archive"
)

func TestPasskeyDefaultNameUsesAccountUsername(t *testing.T) {
	if got := passkeyDefaultName("viewer_one"); got != "Kura passkey for viewer_one" {
		t.Fatalf("passkey default = %q", got)
	}
	if got := passkeyDefaultName("  "); got != "Kura passkey" {
		t.Fatalf("blank passkey default = %q", got)
	}
}

func TestRecoveryDetailsIdentifyAccountPurposeAndRotation(t *testing.T) {
	details := recoveryDetails("viewer_one", "ABCD-EFGH")
	for _, want := range []string{
		"Kura account recovery",
		"Username: viewer_one",
		"Purpose: Recover this account or replace its sign-in methods.",
		"Recovery code: ABCD-EFGH",
		"Replacing or recovering this account rotates the recovery code; the previous code is no longer valid.",
	} {
		if !strings.Contains(details, want) {
			t.Fatalf("recovery details missing %q: %q", want, details)
		}
	}
}

func TestRecoveryDownloadFilenameIsAccountSpecificAndSafe(t *testing.T) {
	if got := recoveryDownloadFilename("viewer_one"); got != "kura-recovery-viewer_one.txt" {
		t.Fatalf("recovery filename = %q", got)
	}
	got := recoveryDownloadFilename(`../../viewer/one<script>`)
	if got != "kura-recovery-______viewer_one_script_.txt" {
		t.Fatalf("sanitized recovery filename = %q", got)
	}
	if strings.ContainsAny(got, `/\\<>:"|?*`) {
		t.Fatalf("unsafe recovery filename = %q", got)
	}
}

func TestAuthFormsUseOneUsernameIdentityAndAccountAwarePasskeyLabels(t *testing.T) {
	server, store := testServer(t)
	user, err := store.Register(t.Context(), "viewer_one", "a sufficiently long password")
	if err != nil {
		t.Fatal(err)
	}

	render := func(name string, data viewData) string {
		recorder := httptest.NewRecorder()
		server.render(recorder, httptest.NewRequest("GET", "/", nil), name, data)
		return recorder.Body.String()
	}

	setup := render("setup", viewData{Title: "Set up Kura — Kura", SetupToken: "setup-token"})
	registration := render("auth", viewData{Title: "Register — Kura"})
	login := render("auth", viewData{Title: "Sign in — Kura"})
	recovery := render("recovery", viewData{Title: "Recover account — Kura"})
	account := render("account", viewData{Title: "viewer_one — Kura", User: &user})

	for name, body := range map[string]string{
		"setup":        setup,
		"registration": registration,
		"login":        login,
		"recovery":     recovery,
	} {
		if !strings.Contains(body, `name="username" autocomplete="username"`) {
			t.Errorf("%s form does not expose the standard username identity: %s", name, body)
		}
	}
	if !strings.Contains(account, `id="account-username" name="username"`) || !strings.Contains(account, `id="account-username"`) || !strings.Contains(account, `autocomplete="username" readonly`) {
		t.Errorf("account page does not identify the account username: %s", account)
	}
	if !strings.Contains(account, `value="Kura passkey for viewer_one"`) {
		t.Errorf("account page does not identify the passkey account: %s", account)
	}
	if !strings.Contains(recovery, `name="recovery_code" type="text" autocomplete="off"`) || strings.Contains(recovery, `name="recovery_code" type="password"`) {
		t.Errorf("recovery code is not kept distinct from password fields: %s", recovery)
	}
	for _, body := range []string{setup, registration, recovery, account} {
		for _, generic := range []string{"My passkey", "Super admin passkey", "Recovered passkey"} {
			if strings.Contains(body, generic) {
				t.Errorf("generic passkey label %q remains in rendered form: %s", generic, body)
			}
		}
	}
}

func TestRecoveryOutputEscapesAccountAndCodeDetails(t *testing.T) {
	server, _ := testServer(t)
	recorder := httptest.NewRecorder()
	server.render(recorder, httptest.NewRequest("GET", "/", nil), "recovery", viewData{
		Title:        "Recovery complete — Kura",
		User:         &archive.User{Username: "viewer_one"},
		RecoveryCode: `<script>alert("x")</script>`,
	})
	body := recorder.Body.String()
	if strings.Contains(body, `<script>alert("x")</script>`) || !strings.Contains(body, "&lt;script&gt;") {
		t.Fatalf("recovery details were not escaped: %s", body)
	}
}

func TestRecoveryDownloadScriptUsesLabeledDetailsAndSafeAccountFilename(t *testing.T) {
	body, err := assets.ReadFile("static/passkeys.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	for _, want := range []string{
		"Kura account recovery",
		"Replacing or recovering this account rotates the recovery code",
		"kura-recovery-",
		"panel?.dataset.recoveryFilename",
		"text/plain",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("passkey script missing recovery download contract %q", want)
		}
	}
}

func TestPasskeyScriptRefreshesGeneratedNameBeforeCeremony(t *testing.T) {
	body, err := assets.ReadFile("static/passkeys.js")
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	for _, want := range []string{
		"const passkeyNameSyncs = new WeakMap()",
		"passkeyNameSyncs.set(button, update)",
		"passkeyNameSyncs.get(button)?.()",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("passkey script missing autofill refresh contract %q", want)
		}
	}
	if strings.Index(script, "passkeyNameSyncs.get(button)?.()") > strings.Index(script, "const input = {}") {
		t.Error("passkey name refresh occurs after ceremony input is collected")
	}
}

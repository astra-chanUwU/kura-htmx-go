package web

import "strings"

func passkeyDefaultName(username string) string {
	username = strings.TrimSpace(username)
	if username == "" {
		return "Kura passkey"
	}
	return "Kura passkey for " + username
}

func recoveryDetails(username, code string) string {
	return strings.Join([]string{
		"Kura account recovery",
		"Username: " + strings.TrimSpace(username),
		"Purpose: Recover this account or replace its sign-in methods.",
		"Recovery code: " + code,
		"Replacing or recovering this account rotates the recovery code; the previous code is no longer valid.",
	}, "\n")
}

func recoveryDownloadFilename(username string) string {
	var safe strings.Builder
	for _, r := range strings.TrimSpace(username) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			safe.WriteRune(r)
		default:
			safe.WriteByte('_')
		}
	}
	if safe.Len() == 0 {
		return "kura-recovery-account.txt"
	}
	return "kura-recovery-" + safe.String() + ".txt"
}

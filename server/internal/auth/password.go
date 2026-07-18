package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/crypto/pbkdf2"
)

// Django-compatible PBKDF2-SHA256 hashing: `pbkdf2_sha256$<iters>$<salt>$<b64hash>`.
// Using the same format means these hashes are interchangeable with Django's, so
// the reference data could be imported later without a password reset.
const pbkdf2Iterations = 260000

func HashPassword(password string) string {
	salt := randString(16)
	sum := pbkdf2.Key([]byte(password), []byte(salt), pbkdf2Iterations, 32, sha256.New)
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", pbkdf2Iterations, salt, base64.StdEncoding.EncodeToString(sum))
}

func VerifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	want, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got := pbkdf2.Key([]byte(password), []byte(parts[2]), iter, len(want), sha256.New)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// commonPasswords is a small blocklist of obviously-weak passwords. The frozen
// contract only requires rejecting "password"; this keeps the strength gate
// honest without importing a full zxcvbn dictionary.
var commonPasswords = map[string]bool{
	"password": true, "12345678": true, "123456789": true, "qwerty123": true,
	"password1": true, "11111111": true, "iloveyou": true, "admin123": true,
}

// PasswordTooWeak reports whether a password should be rejected. Rule: >= 8 chars,
// not in the common-password blocklist, and at least two character classes.
func PasswordTooWeak(pw string) bool {
	if len(pw) < 8 {
		return true
	}
	if commonPasswords[strings.ToLower(pw)] {
		return true
	}
	var lower, upper, digit, sym bool
	for _, r := range pw {
		switch {
		case unicode.IsLower(r):
			lower = true
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsDigit(r):
			digit = true
		default:
			sym = true
		}
	}
	classes := 0
	for _, ok := range []bool{lower, upper, digit, sym} {
		if ok {
			classes++
		}
	}
	return classes < 2
}

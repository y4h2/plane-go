package auth

import (
	"crypto/rand"
)

const alphanum = "abcdefghijklmnopqrstuvwxyz0123456789"

// randString returns a cryptographically-random string of length n over
// [a-z0-9] — matches the character set Plane uses for session keys.
func randString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	for i := range b {
		b[i] = alphanum[int(b[i])%len(alphanum)]
	}
	return string(b)
}

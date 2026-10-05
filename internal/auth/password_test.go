package auth_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/17xande/budget/internal/auth"
)

// cheapParams keeps the suite fast. DefaultParams costs ~0.1s and 64 MiB per
// verification, which is the point in production and minutes of wasted CPU here.
var cheapParams = auth.Params{Memory: 64, Time: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32}

func TestHashAndCheck(t *testing.T) {
	const password = "correct horse battery staple"

	hash, err := auth.HashPassword(password, cheapParams)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Errorf("hash is not a PHC argon2id string: %q", hash)
	}
	if strings.Contains(hash, password) {
		t.Fatal("the hash contains the password")
	}
	if !auth.CheckPassword(hash, password) {
		t.Error("the correct password did not verify")
	}
	if auth.CheckPassword(hash, password+"x") {
		t.Error("a wrong password verified")
	}
	// Same password, different salt: two hashes of one password must not match,
	// or the table becomes a lookup for which accounts share a password.
	other, err := auth.HashPassword(password, cheapParams)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if other == hash {
		t.Error("hashing the same password twice produced the same string")
	}
}

func TestHashPasswordRejectsBadInput(t *testing.T) {
	if _, err := auth.HashPassword("", cheapParams); err == nil {
		t.Error("an empty password was hashed")
	}
	// A zero-value Params would otherwise produce a hash with no work behind it.
	if _, err := auth.HashPassword("pw", auth.Params{}); err == nil {
		t.Error("incomplete parameters were accepted")
	}
	if _, err := auth.HashPassword("pw", auth.Params{Memory: 64, Time: 1, Parallelism: 1, KeyLength: 32}); err == nil {
		t.Error("a zero salt length was accepted")
	}
}

// Every failure is a plain false: a login form has nothing to do with the
// distinction, and telling an attacker which went wrong is worse than useless.
func TestCheckPasswordRejectsMalformedHashes(t *testing.T) {
	tests := []struct{ name, hash string }{
		{"empty", ""},
		{"not a hash", "hunter2"},
		{"bcrypt is not accepted", "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"},
		{"wrong algorithm", "$argon2i$v=19$m=64,t=1,p=1$c2FsdHNhbHRzYWx0c2E$aGFzaA"},
		{"wrong version", "$argon2id$v=18$m=64,t=1,p=1$c2FsdHNhbHRzYWx0c2E$aGFzaA"},
		{"zero parameter", "$argon2id$v=19$m=0,t=1,p=1$c2FsdHNhbHRzYWx0c2E$aGFzaA"},
		{"too few fields", "$argon2id$v=19$m=64,t=1,p=1$c2FsdA"},
		{"undecodable salt", "$argon2id$v=19$m=64,t=1,p=1$!!!!$aGFzaA"},
		// The DoS guard: without the cap this would try to allocate 4 GiB.
		{"absurd memory", "$argon2id$v=19$m=4194304,t=1,p=1$c2FsdHNhbHRzYWx0c2E$aGFzaA"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if auth.CheckPassword(tc.hash, "anything") {
				t.Error("a malformed hash verified")
			}
			if err := auth.ParsePasswordHash(tc.hash); err == nil {
				t.Error("ParsePasswordHash accepted it")
			} else if !errors.Is(err, auth.ErrPasswordFormat) {
				t.Errorf("error does not wrap ErrPasswordFormat: %v", err)
			}
		})
	}
}

// The cost parameters are encoded in the hash, so raising them needs no migration:
// an old hash keeps verifying at its own settings.
func TestOldParametersStillVerify(t *testing.T) {
	hash, err := auth.HashPassword("pw", cheapParams)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := auth.ParsePasswordHash(hash); err != nil {
		t.Fatalf("ParsePasswordHash: %v", err)
	}
	if !auth.CheckPassword(hash, "pw") {
		t.Error("a hash written at different parameters did not verify")
	}
}

// DefaultParams is what production writes, so at least one test has to pay for it.
func TestDefaultParamsRoundTrip(t *testing.T) {
	hash, err := auth.HashPassword("pw", auth.DefaultParams)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !auth.CheckPassword(hash, "pw") {
		t.Error("a DefaultParams hash did not verify")
	}
}

// Package auth authenticates the people who use the budget.
//
// Passwords are hashed with argon2id, which OWASP puts first. The KDF is
// x/crypto's; what is written here is the PHC string encoding, which is string
// formatting rather than cryptography, and whose failure mode is "the hash does
// not verify" rather than a quiet weakness.
//
// Sessions are opaque random tokens in a database table -- see store.go. There is
// deliberately no signed, self-describing cookie: it would still need the table to
// support disabling a user or invalidating their sessions on a password change, so
// signing would add a key, a rotation runbook and a payload format for nothing.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are argon2id's cost parameters.
//
// They are exported because tests need cheap ones: the real parameters cost about
// a tenth of a second and 64 MiB per verification, which is the point in
// production and minutes of wasted CPU across a test suite.
type Params struct {
	// Memory in KiB, and the parameter that actually does the work: argon2's
	// resistance to custom hardware comes from needing this much memory per
	// guess.
	Memory uint32
	// Time is the number of passes over that memory.
	Time uint32
	// Parallelism is the number of lanes. More lanes on fewer cores simply
	// serialise; they do not weaken the hash.
	Parallelism uint8

	SaltLength uint32
	KeyLength  uint32
}

// DefaultParams is RFC 9106's second recommended configuration: 64 MiB, three
// passes, four lanes.
//
// The parameters are encoded in each hash, so raising them later needs no
// migration: old hashes keep verifying at their own settings and the next password
// change writes one at the new cost.
var DefaultParams = Params{
	Memory:      64 * 1024,
	Time:        3,
	Parallelism: 4,
	SaltLength:  16,
	KeyLength:   32,
}

// maxConcurrentHashes bounds how many argon2 computations run at once, across
// every caller. Each takes DefaultParams.Memory (64 MiB) for its duration, and the
// per-IP limiter admits a burst of attempts at once: ten parallel logins measured
// ~650 MiB against a 512 MiB container, so one client could take the server down.
// Excess callers wait their turn rather than fail, which only costs them latency.
const maxConcurrentHashes = 2

var hashSlots = make(chan struct{}, maxConcurrentHashes)

// deriveKey is the argon2 computation itself; a variable so a test can observe
// how many run at once without allocating real argon2 memory.
var deriveKey = argon2.IDKey

// idKey is argon2.IDKey, held to maxConcurrentHashes at a time.
func idKey(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	return deriveKey(password, salt, time, memory, threads, keyLen)
}

// maxVerifyMemory caps the memory a *stored* hash may ask for at verification
// time. Without it a corrupted row claiming m=4194304 would try to allocate four
// gibibytes on the first login attempt and take the server with it.
const maxVerifyMemory = 1 << 20 // 1 GiB in KiB

// ErrPasswordFormat means a stored hash is not in a format this package can read.
var ErrPasswordFormat = errors.New("auth: unrecognised password hash format")

// HashPassword returns an argon2id PHC string to store in users.
func HashPassword(password string, p Params) (string, error) {
	if password == "" {
		return "", errors.New("auth: password must not be empty")
	}
	if p.Memory == 0 || p.Time == 0 || p.Parallelism == 0 || p.SaltLength == 0 || p.KeyLength == 0 {
		// Rejected rather than defaulted: a zero-value Params would otherwise
		// produce a hash with no work behind it and no sign that anything is wrong.
		return "", fmt.Errorf("auth: argon2id parameters are incomplete: %+v", p)
	}

	salt := make([]byte, p.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generate salt: %w", err)
	}

	key := idKey([]byte(password), salt, p.Time, p.Memory, p.Parallelism, p.KeyLength)

	// The standard PHC encoding, which is what makes this hash readable by any
	// other argon2 implementation. base64 without padding, as the format specifies.
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// CheckPassword reports whether password matches the stored hash.
//
// Every failure -- wrong password, malformed hash, absurd parameters -- is a plain
// false. A login form has nothing useful to do with the distinction, and telling
// an attacker which of the two went wrong is worse than useless. A malformed hash
// is worth an operator's attention, so ParsePasswordHash exists for the checks
// that provide it, on the way *in* to the database rather than on the way out.
func CheckPassword(hash, password string) bool {
	if !strings.HasPrefix(hash, "$argon2id$") {
		return false
	}
	p, salt, want, err := parseArgon2id(hash)
	if err != nil {
		return false
	}
	got := idKey([]byte(password), salt, p.Time, p.Memory, p.Parallelism, p.KeyLength)
	// Constant time, because the comparison is against a secret-derived value and
	// an early return would leak how much of it matched.
	return subtle.ConstantTimeCompare(got, want) == 1
}

// ParsePasswordHash checks that a hash is one this package can verify. The store
// calls it before writing, so a bad hash is rejected at the point it is created
// rather than becoming a user who can never sign in and no explanation of why.
func ParsePasswordHash(hash string) error {
	if !strings.HasPrefix(hash, "$argon2id$") {
		return fmt.Errorf("%w: expected a $argon2id$ hash (set one with `budget reset-password`)",
			ErrPasswordFormat)
	}
	_, _, _, err := parseArgon2id(hash)
	return err
}

// parseArgon2id reads a PHC string back into its parts. It is deliberately strict:
// anything unexpected is an error rather than a default, because a hash is not a
// place to be forgiving.
func parseArgon2id(hash string) (p Params, salt, key []byte, err error) {
	// "$argon2id$v=19$m=65536,t=3,p=4$<salt>$<key>" splits into six, the first
	// empty.
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return p, nil, nil, fmt.Errorf("%w: not a well-formed argon2id string", ErrPasswordFormat)
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return p, nil, nil, fmt.Errorf("%w: unreadable version %q", ErrPasswordFormat, parts[2])
	}
	if version != argon2.Version {
		return p, nil, nil, fmt.Errorf("%w: argon2 version %d, want %d",
			ErrPasswordFormat, version, argon2.Version)
	}

	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Parallelism); err != nil {
		return p, nil, nil, fmt.Errorf("%w: unreadable parameters %q", ErrPasswordFormat, parts[3])
	}
	if p.Memory == 0 || p.Time == 0 || p.Parallelism == 0 {
		return p, nil, nil, fmt.Errorf("%w: zero parameter in %q", ErrPasswordFormat, parts[3])
	}
	if p.Memory > maxVerifyMemory {
		return p, nil, nil, fmt.Errorf("%w: hash asks for %d KiB of memory, more than the %d KiB limit",
			ErrPasswordFormat, p.Memory, maxVerifyMemory)
	}

	if salt, err = base64.RawStdEncoding.DecodeString(parts[4]); err != nil {
		return p, nil, nil, fmt.Errorf("%w: undecodable salt", ErrPasswordFormat)
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[5]); err != nil {
		return p, nil, nil, fmt.Errorf("%w: undecodable hash", ErrPasswordFormat)
	}
	if len(salt) == 0 || len(key) == 0 {
		return p, nil, nil, fmt.Errorf("%w: empty salt or hash", ErrPasswordFormat)
	}

	p.SaltLength, p.KeyLength = uint32(len(salt)), uint32(len(key))
	return p, salt, key, nil
}

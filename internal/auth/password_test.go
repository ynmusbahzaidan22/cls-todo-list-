package auth

import (
	"testing"
	"time"
)

func TestPasswordHashAndVerification(t *testing.T) {
	password := "a secure example password"
	encoded, err := hashPassword(password)
	if err != nil {
		t.Fatalf("hashPassword returned error: %v", err)
	}
	if !verifyPassword(encoded, password) {
		t.Fatal("the correct password did not verify")
	}
	if verifyPassword(encoded, password+"x") {
		t.Fatal("an incorrect password verified")
	}
	if verifyPassword("$argon2id$v=19$m=999999999,t=2,p=1$bad$bad", password) {
		t.Fatal("unsafe or malformed parameters verified")
	}
}

func TestValidatePasswordBounds(t *testing.T) {
	for _, password := range []string{"short", string(make([]rune, 129))} {
		if validatePassword(password) == nil {
			t.Fatalf("validatePassword accepted invalid password with %d bytes", len(password))
		}
	}
	if err := validatePassword("long enough password"); err != nil {
		t.Fatalf("validatePassword rejected a valid password: %v", err)
	}
}

func testTime() time.Time { return time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC) }

func TestAttemptLimiterUsesSlidingWindowAndBoundsKeys(t *testing.T) {
	limiter := newAttemptLimiter(2)
	now := testTime()
	if !limiter.allow("first", now, 2, time.Minute) || !limiter.allow("first", now.Add(time.Second), 2, time.Minute) {
		t.Fatal("limiter rejected attempts below the threshold")
	}
	if limiter.allow("first", now.Add(2*time.Second), 2, time.Minute) {
		t.Fatal("limiter accepted an attempt beyond the threshold")
	}
	if !limiter.allow("second", now.Add(2*time.Minute), 2, time.Minute) {
		t.Fatal("limiter did not allow a request after the window expired")
	}
	if !limiter.allow("third", now.Add(2*time.Minute), 2, time.Minute) {
		t.Fatal("limiter did not reclaim stale keys")
	}
	if limiter.allow("fourth", now.Add(2*time.Minute), 2, time.Minute) {
		t.Fatal("limiter exceeded its key bound")
	}
}

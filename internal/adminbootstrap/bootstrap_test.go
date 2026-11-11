package adminbootstrap

import (
	"context"
	"errors"
	"testing"
)

type fakeRepository struct {
	status         Status
	statusCalls    int
	bootstrapCalls int
	bootstrapErr   error
}

func (f *fakeRepository) Status(_ context.Context, _ string) (Status, error) {
	f.statusCalls++
	return f.status, nil
}
func (f *fakeRepository) Bootstrap(_ context.Context, _ string) error {
	f.bootstrapCalls++
	return f.bootstrapErr
}

func TestValidateEmailRequiresExactNormalizedAddress(t *testing.T) {
	for _, input := range []string{" owner@example.test", "Owner@example.test", "owner@example.test ", "owner@"} {
		if _, err := ValidateEmail(input); err == nil {
			t.Errorf("ValidateEmail(%q) unexpectedly succeeded", input)
		}
	}
	if got, err := ValidateEmail("owner@example.test"); err != nil || got != "owner@example.test" {
		t.Fatalf("ValidateEmail valid address = %q, %v", got, err)
	}
}

func TestValidateTargetFailsClosed(t *testing.T) {
	cases := []struct {
		target            Target
		testMode, wantErr bool
	}{
		{Target{Host: "127.0.0.1", Port: "15432", Database: "todo_dev", User: "dev_admin"}, false, false},
		{Target{Host: "127.0.0.1", Port: "25432", Database: "todo_test", User: "test_admin"}, true, false},
		{Target{Host: "127.0.0.1", Port: "15432", Database: "todo_dev", User: "dev_admin"}, true, true},
		{Target{Host: "localhost", Port: "25432", Database: "todo_test", User: "test_admin"}, true, true},
		{Target{Host: "127.0.0.1", Port: "25432", Database: "todo_dev", User: "test_admin"}, true, true},
		{Target{Host: "127.0.0.1", Port: "15432", Database: "todo_test", User: "test_admin"}, false, true},
	}
	for _, tc := range cases {
		err := ValidateTarget(tc.target, tc.testMode)
		if (err != nil) != tc.wantErr {
			t.Errorf("ValidateTarget(%+v, %t) error=%v", tc.target, tc.testMode, err)
		}
	}
}

func TestURLTargetRejectsAmbiguousConnectionOptions(t *testing.T) {
	got, err := URLTarget("postgres://admin:secret@127.0.0.1:25432/todo_test?sslmode=disable")
	if err != nil || got.Host != "127.0.0.1" || got.Port != "25432" || got.Database != "todo_test" || got.User != "admin" {
		t.Fatalf("valid test URL target=%+v err=%v", got, err)
	}
	for _, raw := range []string{
		"postgres://admin:secret@127.0.0.1:25432/todo_test?sslmode=disable&host=other",
		"postgres://admin:secret@127.0.0.1:25432/todo_test?sslmode=disable&sslmode=require",
		"postgres://admin:secret@127.0.0.1:25432/todo_test?dbname=todo_dev",
		"postgres://admin:secret@127.0.0.1:25432/%74odo_test",
	} {
		if _, err := URLTarget(raw); err == nil {
			t.Errorf("URLTarget accepted unsupported form")
		}
	}
}

func TestServiceCallsBootstrapOnlyForValidatedEmail(t *testing.T) {
	fake := &fakeRepository{}
	service := New(fake)
	if err := service.Bootstrap(context.Background(), "Owner@example.test"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid email error = %v", err)
	}
	if fake.bootstrapCalls != 0 {
		t.Fatal("invalid input reached repository")
	}
	if err := service.Bootstrap(context.Background(), "owner@example.test"); err != nil {
		t.Fatal(err)
	}
	if fake.bootstrapCalls != 1 {
		t.Fatalf("bootstrap calls=%d", fake.bootstrapCalls)
	}
}

func TestStatusIsReadOnlyServiceDelegation(t *testing.T) {
	fake := &fakeRepository{status: Status{ActiveAdmins: 0, GuardAdmins: 0}}
	status, err := New(fake).Status(context.Background(), "")
	if err != nil || status.ActiveAdmins != 0 || fake.statusCalls != 1 || fake.bootstrapCalls != 0 {
		t.Fatalf("status=%+v err=%v calls=%d/%d", status, err, fake.statusCalls, fake.bootstrapCalls)
	}
}

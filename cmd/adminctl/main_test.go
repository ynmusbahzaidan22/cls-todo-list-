package main

import "testing"

func TestParseInvocationRequiresExplicitTargetAndConfirmation(t *testing.T) {
	cases := [][]string{
		{"bootstrap", "--email", "owner@example.test"},
		{"bootstrap", "--database", "other", "--email", "owner@example.test", "--confirm-database", "other"},
		{"bootstrap", "--database", "todo_dev", "--email", "Owner@example.test", "--confirm-database", "todo_dev"},
		{"status"},
	}
	for _, args := range cases {
		if _, err := parseInvocation(args); err == nil {
			t.Errorf("parseInvocation(%q) unexpectedly succeeded", args)
		}
	}
	if _, err := parseInvocation([]string{"bootstrap", "--database", "todo_dev", "--email", "owner@example.test", "--confirm-database", "todo_dev"}); err != nil {
		t.Fatalf("valid guarded invocation rejected: %v", err)
	}
}

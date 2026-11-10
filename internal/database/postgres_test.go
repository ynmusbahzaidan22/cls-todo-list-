package database

import "testing"

func TestValidateRuntimeRolePrivileges(t *testing.T) {
	valid := runtimeRolePrivileges{}
	t.Run("accepts least-privilege role", func(t *testing.T) {
		if err := validateRuntimeRolePrivileges("todo_runtime", "todo_runtime", valid); err != nil {
			t.Fatalf("valid role rejected: %v", err)
		}
	})

	for name, mutate := range map[string]func(*runtimeRolePrivileges){
		"wrong role":              func(p *runtimeRolePrivileges) {},
		"activity update":         func(p *runtimeRolePrivileges) { p.canUpdateActivity = true },
		"activity delete":         func(p *runtimeRolePrivileges) { p.canDeleteActivity = true },
		"activity truncate":       func(p *runtimeRolePrivileges) { p.canTruncateActivity = true },
		"task truncate":           func(p *runtimeRolePrivileges) { p.canTruncateTasks = true },
		"database create":         func(p *runtimeRolePrivileges) { p.canCreateDatabase = true },
		"schema create":           func(p *runtimeRolePrivileges) { p.canCreateSchema = true },
		"role membership":         func(p *runtimeRolePrivileges) { p.hasRoleMembership = true },
		"application table owner": func(p *runtimeRolePrivileges) { p.ownsApplicationTables = true },
	} {
		t.Run(name, func(t *testing.T) {
			privileges := valid
			mutate(&privileges)
			expected := "todo_runtime"
			current := expected
			if name == "wrong role" {
				current = "todo_dev"
			}
			if err := validateRuntimeRolePrivileges(current, expected, privileges); err == nil {
				t.Fatal("unsafe role was accepted")
			}
		})
	}
}

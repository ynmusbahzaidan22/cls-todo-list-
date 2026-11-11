package database

import "testing"

func TestValidateRuntimeRoleBase(t *testing.T) {
	valid := runtimeRoleBase{canLogin: true, canConnectDatabase: true, canUseSchema: true}
	if err := validateRuntimeRoleBase("todo_runtime", "todo_runtime", valid); err != nil {
		t.Fatalf("valid role rejected: %v", err)
	}

	unsafeCases := map[string]func(*runtimeRoleBase){
		"wrong current role":        func(*runtimeRoleBase) {},
		"cannot login":              func(v *runtimeRoleBase) { v.canLogin = false },
		"superuser":                 func(v *runtimeRoleBase) { v.superuser = true },
		"createdb":                  func(v *runtimeRoleBase) { v.createdb = true },
		"createrole":                func(v *runtimeRoleBase) { v.createrole = true },
		"inherit":                   func(v *runtimeRoleBase) { v.inherit = true },
		"replication":               func(v *runtimeRoleBase) { v.replication = true },
		"bypassrls":                 func(v *runtimeRoleBase) { v.bypassRLS = true },
		"database owner":            func(v *runtimeRoleBase) { v.ownsDatabase = true },
		"database create":           func(v *runtimeRoleBase) { v.canCreateDatabase = true },
		"database temporary tables": func(v *runtimeRoleBase) { v.canUseTemporaryTable = true },
		"missing database connect":  func(v *runtimeRoleBase) { v.canConnectDatabase = false },
		"schema owner":              func(v *runtimeRoleBase) { v.ownsSchema = true },
		"schema create":             func(v *runtimeRoleBase) { v.canCreateSchema = true },
		"missing schema usage":      func(v *runtimeRoleBase) { v.canUseSchema = false },
		"owns public schema":        func(v *runtimeRoleBase) { v.ownsPublicSchema = true },
		"public schema create":      func(v *runtimeRoleBase) { v.canCreatePublicSchema = true },
		"owns another schema":       func(v *runtimeRoleBase) { v.ownsOtherSchema = true },
		"create in another schema":  func(v *runtimeRoleBase) { v.canCreateOtherSchema = true },
		"role membership":           func(v *runtimeRoleBase) { v.hasRoleMembership = true },
		"owns relation or sequence": func(v *runtimeRoleBase) { v.ownsApplicationRelations = true },
		"owns function":             func(v *runtimeRoleBase) { v.ownsApplicationFunctions = true },
		"owns type":                 func(v *runtimeRoleBase) { v.ownsApplicationTypes = true },
		"grant option":              func(v *runtimeRoleBase) { v.canGrantPrivileges = true },
		"PUBLIC app ACL":            func(v *runtimeRoleBase) { v.hasPublicAppGrants = true },
		"unexpected default ACL":    func(v *runtimeRoleBase) { v.hasUnexpectedDefaultPrivileges = true },
	}
	for name, mutate := range unsafeCases {
		t.Run(name, func(t *testing.T) {
			gotRole := "todo_runtime"
			if name == "wrong current role" {
				gotRole = "todo_dev"
			}
			state := valid
			mutate(&state)
			if err := validateRuntimeRoleBase(gotRole, "todo_runtime", state); err == nil {
				t.Fatal("unsafe role state was accepted")
			}
		})
	}
}

func TestValidateRuntimeTablePrivileges(t *testing.T) {
	for table, policy := range runtimeTablePolicies {
		t.Run(table+" accepts exact policy", func(t *testing.T) {
			actual := effectiveTablePrivileges{
				selectRows: policy.selectRows, insertRows: policy.insertRows,
				updateRows: policy.updateRows, deleteRows: policy.deleteRows,
			}
			if err := validateRuntimeTablePrivileges(table, actual); err != nil {
				t.Fatalf("approved table policy rejected: %v", err)
			}
		})
		for _, privilege := range []struct {
			name   string
			change func(*effectiveTablePrivileges)
		}{
			{"unexpected SELECT", func(p *effectiveTablePrivileges) { p.selectRows = !policy.selectRows }},
			{"unexpected INSERT", func(p *effectiveTablePrivileges) { p.insertRows = !policy.insertRows }},
			{"unexpected UPDATE", func(p *effectiveTablePrivileges) { p.updateRows = !policy.updateRows }},
			{"unexpected DELETE", func(p *effectiveTablePrivileges) { p.deleteRows = !policy.deleteRows }},
			{"TRUNCATE", func(p *effectiveTablePrivileges) { p.truncateRows = true }},
			{"REFERENCES", func(p *effectiveTablePrivileges) { p.referencesRows = true }},
			{"TRIGGER", func(p *effectiveTablePrivileges) { p.triggerRows = true }},
			{"MAINTAIN", func(p *effectiveTablePrivileges) { p.maintainRows = true }},
		} {
			t.Run(table+" rejects "+privilege.name, func(t *testing.T) {
				actual := effectiveTablePrivileges{
					selectRows: policy.selectRows, insertRows: policy.insertRows,
					updateRows: policy.updateRows, deleteRows: policy.deleteRows,
				}
				privilege.change(&actual)
				if err := validateRuntimeTablePrivileges(table, actual); err == nil {
					t.Fatal("unexpected effective table privilege was accepted")
				}
			})
		}
	}
	if err := validateRuntimeTablePrivileges("new_table", effectiveTablePrivileges{}); err == nil {
		t.Fatal("table without explicit policy was accepted")
	}
}

func TestValidateRuntimeColumnPrivileges(t *testing.T) {
	users := runtimeTablePolicies["users"]
	validUserColumn := effectiveColumnPrivileges{name: "email", selectRows: true, insertRows: true}
	if err := validateRuntimeColumnPrivileges("users", users, validUserColumn); err != nil {
		t.Fatalf("valid users column rejected: %v", err)
	}
	for _, columnName := range []string{"time_zone", "role", "status", "updated_at"} {
		approved := effectiveColumnPrivileges{name: columnName, selectRows: true, insertRows: true, updateRows: true}
		if err := validateRuntimeColumnPrivileges("users", users, approved); err != nil {
			t.Errorf("approved users.%s UPDATE rejected: %v", columnName, err)
		}
	}

	for name, column := range map[string]effectiveColumnPrivileges{
		"unexpected UPDATE on email": {name: "email", selectRows: true, insertRows: true, updateRows: true},
		"unexpected REFERENCES":      {name: "email", selectRows: true, insertRows: true, referencesRows: true},
		"missing time_zone UPDATE":   {name: "time_zone", selectRows: true, insertRows: true},
		"unexpected column SELECT":   {name: "email", insertRows: true},
		"unexpected column INSERT":   {name: "email", selectRows: true},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateRuntimeColumnPrivileges("users", users, column); err == nil {
				t.Fatal("unexpected effective column privilege state was accepted")
			}
		})
	}

	activity := runtimeTablePolicies["task_activity"]
	if err := validateRuntimeColumnPrivileges("task_activity", activity,
		effectiveColumnPrivileges{name: "event_type", selectRows: true, insertRows: true, updateRows: true}); err == nil {
		t.Fatal("column-level UPDATE on activity was accepted")
	}
}

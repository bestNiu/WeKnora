package types

import "testing"

func TestDecideAgentAccess(t *testing.T) {
	tenantAgent := &CustomAgent{ID: "a1", TenantID: 7, CreatedBy: "creator"}
	foreignAgent := &CustomAgent{ID: "a2", TenantID: 9, CreatedBy: "someone"}
	grant := &AgentUserAccess{AgentID: "a1", UserID: "u1", CanView: true}

	cases := []struct {
		name        string
		agent       *CustomAgent
		mode        AgentVisibilityMode
		grant       *AgentUserAccess
		tenantID    uint64
		userID      string
		role        TenantRole
		systemAdmin bool
		want        bool
	}{
		{"system admin sees allowlist", tenantAgent, AgentVisibilityAllowlist, nil, 7, "u9", TenantRoleViewer, true, true},
		{"owner sees allowlist", tenantAgent, AgentVisibilityAllowlist, nil, 7, "u9", TenantRoleOwner, false, true},
		{"admin sees allowlist", tenantAgent, AgentVisibilityAllowlist, nil, 7, "u9", TenantRoleAdmin, false, true},
		{"creator sees allowlist", tenantAgent, AgentVisibilityAllowlist, nil, 7, "creator", TenantRoleContributor, false, true},
		{"contributor without grant hidden", tenantAgent, AgentVisibilityAllowlist, nil, 7, "u1", TenantRoleContributor, false, false},
		{"viewer without grant hidden", tenantAgent, AgentVisibilityAllowlist, nil, 7, "u1", TenantRoleViewer, false, false},
		{"granted viewer visible", tenantAgent, AgentVisibilityAllowlist, grant, 7, "u1", TenantRoleViewer, false, true},
		{"tenant mode visible", tenantAgent, AgentVisibilityTenant, nil, 7, "u1", TenantRoleViewer, false, true},
		{"unknown mode defaults to tenant", tenantAgent, AgentVisibilityMode("bogus"), nil, 7, "u1", TenantRoleViewer, false, true},
		{"foreign shared agent visible", foreignAgent, AgentVisibilityAllowlist, nil, 7, "u1", TenantRoleViewer, false, true},
		{"nil agent hidden for allowlist", nil, AgentVisibilityAllowlist, nil, 7, "u1", TenantRoleViewer, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DecideAgentAccess(tc.agent, tc.mode, tc.grant, tc.tenantID, tc.userID, tc.role, tc.systemAdmin)
			if got.Visible != tc.want {
				t.Fatalf("Visible = %v, want %v", got.Visible, tc.want)
			}
		})
	}
}

func TestAgentVisibilityModeNormalize(t *testing.T) {
	if got := AgentVisibilityMode("").Normalize(); got != AgentVisibilityTenant {
		t.Fatalf("empty mode normalized to %q, want tenant", got)
	}
	if got := AgentVisibilityMode("allowlist").Normalize(); got != AgentVisibilityAllowlist {
		t.Fatalf("allowlist mode normalized to %q", got)
	}
	if got := AgentVisibilityMode("nope").Normalize(); got != AgentVisibilityTenant {
		t.Fatalf("unknown mode normalized to %q, want tenant", got)
	}
}
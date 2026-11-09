package main

import (
	"testing"

	"dshgo/internal/modes"
	"dshgo/internal/sandbox"
	"dshgo/internal/transport"
)

func TestPermissionModeResponseKeepsApprovalAndSandboxSeparate(t *testing.T) {
	home := t.TempDir()
	reg := newSandboxTestRegistry(t)
	store := mustSandboxStore(t, home)
	approvals := newPermissionOverrideStore(home)
	sid := createSandboxSession(t, home, modes.DefaultID)

	for _, approval := range []string{"deny", "ask", "allow", "auto"} {
		res, err := sandboxSetPermission(home, sid, approval, reg, store, approvals)
		if err != nil {
			t.Fatalf("%s: %v", approval, err)
		}
		body := permissionModeResponse(res)
		if body["permission"] != approval || body["mode"] != sandbox.ModeWorkspaceWrite || body["effective_mode"] != sandbox.ModeWorkspaceWrite {
			t.Fatalf("%s 响应混淆：%+v", approval, body)
		}
		// 回读必须来自同一个 permissionOverrideStore。
		src := newSessionSource(home, "")
		src.perms = approvals
		loaded, err := transport.LoadSession(src, sid, 10)
		if err != nil {
			t.Fatalf("%s load: %v", approval, err)
		}
		if loaded.Permission != approval {
			t.Fatalf("%s 回读 permission=%q", approval, loaded.Permission)
		}
	}

	res, err := sandboxSetPermission(home, sid, "read-only", reg, store, approvals)
	if err != nil {
		t.Fatal(err)
	}
	body := permissionModeResponse(res)
	if body["permission"] != "read-only" || body["mode"] != sandbox.ModeReadOnly || body["effective_mode"] != sandbox.ModeReadOnly {
		t.Fatalf("sandbox 档响应=%+v", body)
	}
	if _, err := sandboxSetPermission(home, sid, "danger-full-access", reg, store, approvals); err == nil {
		t.Fatal("default preset 不允许放宽到 danger")
	}
}

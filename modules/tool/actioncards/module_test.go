package actioncards

import (
	"testing"

	"dshgo/internal/kernel"
	"dshgo/internal/spi"
	"dshgo/modules/tools"
)

func TestModuleRegistersProposeAction(t *testing.T) {
	k := kernel.New("t")
	defer k.Root().Dispose()
	if err := tools.Module(tools.Options{}).Register(k.Root()); err != nil {
		t.Fatalf("tools.Module: %v", err)
	}
	if err := Module(t.TempDir()).Register(k.Root()); err != nil {
		t.Fatalf("Module: %v", err)
	}
	reg, err := kernel.Inject(k.Root(), spi.ToolsKey)
	if err != nil {
		t.Fatalf("Inject: %v", err)
	}
	tool, ok := reg.Get("propose_action")
	if !ok {
		t.Fatal("工具表里没有 propose_action")
	}
	if tool.Caps().NeedsApproval() {
		t.Fatal("propose_action 只登记卡片，不应要求审批")
	}
}

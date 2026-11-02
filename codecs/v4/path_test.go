package v4

import "testing"

func TestProjectKeyMatchesRealDirectories(t *testing.T) {
	// 这些名字是从 $DSH_HOME/sessions 的真实目录抄下来的——它们就是契约。
	cases := []struct{ cwd, want string }{
		{`D:\DeepSeek Harness\default-workspace`, "--D-DeepSeek~0020Harness-default-workspace--"},
		{`D:\DeepSeek Harness\default-workspace\DSH-GO`, "--D-DeepSeek~0020Harness-default-workspace-DSH-GO--"},
		{`/tmp/hstest`, "--tmp-hstest--"},
		{`D:\主工作区\projects\magpie`, "--D-~4E3B~5DE5~4F5C~533A-projects-magpie--"},
	}
	for _, c := range cases {
		if got := ProjectKey(c.cwd); got != c.want {
			t.Errorf("ProjectKey(%q) = %q，期望 %q", c.cwd, got, c.want)
		}
	}
}

func TestProjectKeyEdgeCases(t *testing.T) {
	if got := ProjectKey(""); got != "--root--" {
		t.Errorf("空 cwd = %q，期望 --root--", got)
	}
	if got := ProjectKey(`C:\a\\b`); got != "--C-a-b--" {
		t.Errorf("连续分隔符未折叠: %q", got)
	}
	if got := ProjectKey(`~` + "x"); got != "--~007Ex--" {
		t.Errorf("裸 ~ 必须转义: %q", got)
	}
	long := ProjectKey("/" + string(make([]byte, 0)) + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if len(long) != len("----")+251 {
		t.Errorf("截断后总长 = %d，期望 %d", len(long), 255)
	}
}

func TestEncodeSegment(t *testing.T) {
	cases := []struct{ in, want string }{
		{"session-22d3bd16", "session-22d3bd16"},
		{".", "~002E"},
		{"..", "~002E~002E"},
		{"a/b", "a~002Fb"},
		{"a~b", "a~007Eb"},
	}
	for _, c := range cases {
		got, err := EncodeSegment(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("EncodeSegment(%q) = %q，期望 %q", c.in, got, c.want)
		}
	}
	if _, err := EncodeSegment(""); err == nil {
		t.Fatal("空 id 必须报错")
	}
}

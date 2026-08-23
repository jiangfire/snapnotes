package parser

import (
	"testing"
	"time"
)

func TestParseExtractsAndNormalizesLightweightSyntax(t *testing.T) {
	body := "#Idea #golang #idea\n@date 2026-08-18\n@remind 2026-08-20T09:00:00+08:00\n@repeat every:3d\n- [ ] verify parser\n- [x] read spec"

	parsed := Parse(body)

	if parsed.Body != body {
		t.Fatalf("body changed during parsing: %q", parsed.Body)
	}
	if len(parsed.Tags) != 2 || parsed.Tags[0] != "idea" || parsed.Tags[1] != "golang" {
		t.Fatalf("tags = %#v, want [idea golang]", parsed.Tags)
	}
	if parsed.Date != "2026-08-18" {
		t.Fatalf("date = %q, want %q", parsed.Date, "2026-08-18")
	}
	wantReminder := time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC)
	if parsed.Reminder == nil || !parsed.Reminder.Equal(wantReminder) {
		t.Fatalf("reminder = %v, want %v", parsed.Reminder, wantReminder)
	}
	if parsed.Repeat != "every:3d" {
		t.Fatalf("repeat = %q, want %q", parsed.Repeat, "every:3d")
	}
	if len(parsed.CheckItems) != 2 || parsed.CheckItems[0] != (CheckItem{Text: "verify parser"}) || parsed.CheckItems[1] != (CheckItem{Text: "read spec", Checked: true}) {
		t.Fatalf("check items = %#v, want two parsed items", parsed.CheckItems)
	}
}

func TestParseIgnoresInvalidSyntaxAndPreservesBody(t *testing.T) {
	body := "#valid #bad.tag\n@date 2026-02-30\n@remind tomorrow\n@repeat yearly\nplain text"

	parsed := Parse(body)

	if parsed.Body != body {
		t.Fatalf("body changed during parsing: %q", parsed.Body)
	}
	if len(parsed.Tags) != 1 || parsed.Tags[0] != "valid" {
		t.Fatalf("tags = %#v, want [valid]", parsed.Tags)
	}
	if parsed.Date != "" {
		t.Fatalf("invalid date = %q, want empty", parsed.Date)
	}
	if parsed.Reminder != nil {
		t.Fatalf("invalid reminder = %v, want nil", parsed.Reminder)
	}
	if parsed.Repeat != "" {
		t.Fatalf("invalid repeat = %q, want empty", parsed.Repeat)
	}
}

func TestParseSupportsUnicodeTagsAndReportsUncheckedItems(t *testing.T) {
	parsed := Parse("#闪念 #GoLang\n- [ ] 未完成")

	if len(parsed.Tags) != 2 || parsed.Tags[0] != "闪念" || parsed.Tags[1] != "golang" {
		t.Fatalf("tags = %#v, want normalized Unicode tags", parsed.Tags)
	}
	if !parsed.HasUncheckedItems() {
		t.Fatal("HasUncheckedItems() = false, want true")
	}
}

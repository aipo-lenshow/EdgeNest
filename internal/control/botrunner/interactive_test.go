package botrunner

import (
	"strconv"
	"testing"
)

func TestSplitPrefix(t *testing.T) {
	cases := []struct {
		in        string
		pfx, rest string
		ok        bool
	}{
		{"act:create", "act", "create", true},
		{"pick:12", "pick", "12", true},
		{"val:10g", "val", "10g", true},
		{"nw:confirm", "nw", "confirm", true},
		{"cmd:summary", "cmd", "summary", true},
		{"plainnocolon", "", "", false},
	}
	for _, c := range cases {
		pfx, rest, ok := splitPrefix(c.in)
		if pfx != c.pfx || rest != c.rest || ok != c.ok {
			t.Errorf("splitPrefix(%q)=(%q,%q,%v) want (%q,%q,%v)", c.in, pfx, rest, ok, c.pfx, c.rest, c.ok)
		}
	}
}

func TestQuotaTokenBytes(t *testing.T) {
	cases := []struct {
		tok  string
		want int64
		ok   bool
	}{
		{"0", 0, true},
		{"10g", 10 << 30, true},
		{"50g", 50 << 30, true},
		{"100g", 100 << 30, true},
		{"500g", 500 << 30, true},
		{"bogus", 0, false},
	}
	for _, c := range cases {
		got, ok := quotaTokenBytes(c.tok)
		if got != c.want || ok != c.ok {
			t.Errorf("quotaTokenBytes(%q)=(%d,%v) want (%d,%v)", c.tok, got, ok, c.want, c.ok)
		}
	}
}

func TestPickerViewPagination(t *testing.T) {
	emails := make([]string, 20)
	for i := range emails {
		emails[i] = string(rune('a'+i)) + "@x"
	}
	// page 0: pageSize user rows + a nav row + a cancel row.
	_, rows := pickerView("en", "disable", emails, 0)
	userRows := 0
	for _, row := range rows {
		if len(row) == 1 && len(row[0][1]) > 5 && row[0][1][:5] == "pick:" {
			userRows++
		}
	}
	if userRows != pickerPageSize {
		t.Fatalf("page 0 user rows = %d, want %d", userRows, pickerPageSize)
	}
	// Picker index must be GLOBAL: first row on page 1 is index pickerPageSize.
	_, rows1 := pickerView("en", "disable", emails, 1)
	if rows1[0][0][1] != "pick:"+strconv.Itoa(pickerPageSize) {
		t.Errorf("page 1 first row callback = %q, want pick:%d", rows1[0][0][1], pickerPageSize)
	}
}

func TestTrunc(t *testing.T) {
	if got := trunc("short", 10); got != "short" {
		t.Errorf("trunc kept-short = %q", got)
	}
	long := "abcdefghijklmnop"
	if got := trunc(long, 5); len([]rune(got)) != 5 {
		t.Errorf("trunc(%q,5) len = %d, want 5", got, len([]rune(got)))
	}
}

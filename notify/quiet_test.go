package notify

import (
	"testing"
	"time"
)

func TestQuietHoursCrossesMidnightInBeijing(t *testing.T) {
	q, err := ParseQuietHours("22:00", "07:00")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		utc  string
		want bool
	}{
		{"2026-11-01T13:59:00Z", false}, // 21:59
		{"2026-11-01T14:00:00Z", true},  // 22:00
		{"2026-11-01T22:59:00Z", true},  // 06:59
		{"2026-11-01T23:00:00Z", false}, // 07:00
	} {
		at, err := time.Parse(time.RFC3339, tc.utc)
		if err != nil {
			t.Fatal(err)
		}
		if got := q.Contains(at); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.utc, got, tc.want)
		}
	}
}

func TestQuietHoursValidation(t *testing.T) {
	for _, pair := range [][2]string{{"22:00", ""}, {"12:00", "12:00"}, {"24:00", "07:00"}, {"7:00", "08:00"}} {
		if _, err := ParseQuietHours(pair[0], pair[1]); err == nil {
			t.Errorf("expected invalid window %q-%q", pair[0], pair[1])
		}
	}
	q, err := ParseQuietHours("", "")
	if err != nil || q.Contains(time.Now()) {
		t.Fatalf("empty window must be disabled: %v", err)
	}
}

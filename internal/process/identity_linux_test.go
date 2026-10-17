package process

import "testing"

func TestParseStatStartTicksCountsFromTheLastParenthesis(t *testing.T) {
	line := []byte("4242 (a) b (c) d) S 1 4242 4242 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 987654 1234567 89 18446744073709551615\n")
	ticks, err := parseStatStartTicks(line)
	if err != nil || ticks != 987654 {
		t.Fatalf("parseStatStartTicks = %d, %v; want 987654", ticks, err)
	}
	for _, bad := range []string{"", "4242 no parenthesis", "4242 (sh) S 1 2"} {
		if _, err := parseStatStartTicks([]byte(bad)); err == nil {
			t.Fatalf("parseStatStartTicks(%q) succeeded", bad)
		}
	}
}

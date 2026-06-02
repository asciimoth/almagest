package main

import "testing"

func TestSanitizeLogMessageDropsNonASCIIAndControlBytes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "printable ascii unchanged",
			in:   "abc XYZ 123 !@#$%^&*()[]{}",
			want: "abc XYZ 123 !@#$%^&*()[]{}",
		},
		{
			name: "line breaks dropped",
			in:   "first\nsecond\rthird\r\nfourth",
			want: "firstsecondthirdfourth",
		},
		{
			name: "controls and unicode dropped",
			in:   "ok\t\x1b[31m bad Привет \u202e end\x7f",
			want: "ok[31m bad   end",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeLogMessage(tt.in); got != tt.want {
				t.Fatalf("sanitizeLogMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

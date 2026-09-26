package ui

import "testing"

func TestParseBuildInfo(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{
			name: "full",
			in:   `{"built_at": "2026-09-25T17:00:00+08:00", "git": "560087c"}`,
			want: "560087c@2026-09-25T17:00:00+08:00",
		},
		{
			name: "git only",
			in:   `{"git":"v0.13.0-4-gf2891909","built_at":""}`,
			want: "v0.13.0-4-gf2891909",
		},
		{
			name: "built_at only",
			in:   `{"built_at":"2026-09-25T09:00:00Z"}`,
			want: "2026-09-25T09:00:00Z",
		},
		{
			name: "missing fields",
			in:   `{"foo":1}`,
			want: "",
		},
		{
			name: "garbage",
			in:   `not json at all`,
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseBuildInfo([]byte(c.in))
			if c.want == "" {
				if err == nil && got != "" {
					t.Fatalf("expected error/empty, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// SPABuildInfo must never panic and always return a non-empty string — the
// embedded tree in a bare checkout may legitimately have no build-info.json,
// which maps to "unknown".
func TestSPABuildInfoNonEmpty(t *testing.T) {
	if got := SPABuildInfo(); got == "" {
		t.Fatal("SPABuildInfo returned empty string")
	}
}

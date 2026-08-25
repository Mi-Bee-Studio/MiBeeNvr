package onvif

import "testing"

func TestSelectSubProfile(t *testing.T) {
	cases := []struct {
		name      string
		profiles  []DeviceProfile
		mainToken string
		want      string
	}{
		{
			name: "main+sub picks sub",
			profiles: []DeviceProfile{
				{Token: "main", Width: 2560, Height: 1440},
				{Token: "sub", Width: 704, Height: 576},
			},
			mainToken: "main",
			want:      "sub",
		},
		{
			name: "three profiles picks the largest below main",
			profiles: []DeviceProfile{
				{Token: "main", Width: 2688, Height: 1520},
				{Token: "sub1", Width: 1280, Height: 720},
				{Token: "sub2", Width: 640, Height: 360},
			},
			mainToken: "main",
			want:      "sub1",
		},
		{
			name: "same-resolution duplicate is not a sub stream (Amcrest pattern)",
			profiles: []DeviceProfile{
				{Token: "profile_1", Width: 2560, Height: 1440},
				{Token: "profile_2", Width: 2560, Height: 1440},
			},
			mainToken: "profile_1",
			want:      "",
		},
		{
			name: "single profile → none",
			profiles: []DeviceProfile{
				{Token: "only", Width: 1920, Height: 1080},
			},
			mainToken: "only",
			want:      "",
		},
		{
			name: "unknown main token → none",
			profiles: []DeviceProfile{
				{Token: "a", Width: 1920, Height: 1080},
				{Token: "b", Width: 640, Height: 360},
			},
			mainToken: "missing",
			want:      "",
		},
		{
			name: "resolution-less profiles are skipped",
			profiles: []DeviceProfile{
				{Token: "main", Width: 1920, Height: 1080},
				{Token: "mystery"},
				{Token: "sub", Width: 640, Height: 360},
			},
			mainToken: "main",
			want:      "sub",
		},
		{
			name: "equal-pixel candidates tiebreak on sub cues",
			profiles: []DeviceProfile{
				{Token: "main", Width: 2560, Height: 1440},
				{Token: "videoEncoder_2", Width: 704, Height: 576},
				{Token: "subStreamToken", Width: 704, Height: 576},
			},
			mainToken: "main",
			want:      "subStreamToken",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SelectSubProfile(tc.profiles, tc.mainToken); got != tc.want {
				t.Fatalf("SelectSubProfile = %q, want %q", got, tc.want)
			}
		})
	}
}

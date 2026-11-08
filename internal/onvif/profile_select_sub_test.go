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

// TestSelectSubProfile_StaysOnMainVideoSource pins the dual-spectrum field
// case (Hikvision DS-2TB2617): visible VideoSource_1 (101 main / 102 sub) and
// thermal VideoSource_2 (201 main / 202 sub). The thermal camera's sub must be
// 202 even when the visible sub (704×576) ranks higher than it and below the
// thermal main (1280×720).
func TestSelectSubProfile_StaysOnMainVideoSource(t *testing.T) {
	profiles := []DeviceProfile{
		{Token: "Profile_101", Width: 2688, Height: 1520, VideoSourceToken: "VideoSource_1"},
		{Token: "Profile_102", Width: 704, Height: 576, VideoSourceToken: "VideoSource_1"},
		{Token: "Profile_201", Width: 1280, Height: 720, VideoSourceToken: "VideoSource_2"},
		{Token: "Profile_202", Width: 320, Height: 240, VideoSourceToken: "VideoSource_2"},
	}
	if got := SelectSubProfile(profiles, "Profile_201"); got != "Profile_202" {
		t.Errorf("thermal main → sub = %q, want Profile_202 (same video source)", got)
	}
	if got := SelectSubProfile(profiles, "Profile_101"); got != "Profile_102" {
		t.Errorf("visible main → sub = %q, want Profile_102", got)
	}
	// Devices that do not report source tokens keep the resolution-only rule.
	for i := range profiles {
		profiles[i].VideoSourceToken = ""
	}
	if got := SelectSubProfile(profiles, "Profile_201"); got != "Profile_102" {
		t.Errorf("sourceless → sub = %q, want Profile_102 (legacy ranking)", got)
	}
}

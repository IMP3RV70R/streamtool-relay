package config

import "testing"

func TestStandardTwitchMediaProfiles(t *testing.T) {
	for _, p := range []MediaProfile{{1920, 1080, 60, 1, 6000, 160}, {1920, 1080, 30000, 1001, 4500, 160}, {1280, 720, 60000, 1001, 4500, 160}, {640, 360, 30, 1, 1000, 128}, {1080, 1920, 30, 1, 6000, 160}, {1600, 900, 60, 1, 6000, 160}} {
		if err := p.Validate(); err != nil {
			t.Fatalf("supported profile %+v: %v", p, err)
		}
	}
	for _, p := range []MediaProfile{{2560, 1440, 60, 1, 7500, 160}, {1921, 1080, 60, 1, 6000, 160}, {1920, 1080, 120, 1, 6000, 160}, {1920, 1080, 60, 0, 6000, 160}, {1920, 1080, 60, 1, 9000, 160}, {1920, 1080, 60, 1, 6000, 500}} {
		if p.Validate() == nil {
			t.Fatalf("unsupported profile accepted: %+v", p)
		}
	}
}

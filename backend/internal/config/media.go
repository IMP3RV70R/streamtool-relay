package config

import "errors"

// MediaProfile is the stable output contract for a whole broadcast, including
// fallback and source reconnects. Standard Twitch ingest uses H.264/AAC.
type MediaProfile struct {
	Width     int `json:"width"`
	Height    int `json:"height"`
	FPSNum    int `json:"fps_num"`
	FPSDen    int `json:"fps_den"`
	VideoKbps int `json:"video_kbps"`
	AudioKbps int `json:"audio_kbps"`
}

func DefaultMediaProfile() MediaProfile { return MediaProfile{1280, 720, 30, 1, 3000, 160} }
func (p MediaProfile) Validate() error {
	if p.Width < 160 || p.Height < 90 || p.Width > 1920 || p.Height > 1920 || p.Width*p.Height > 1920*1080 || p.Width%2 != 0 || p.Height%2 != 0 ||
		p.FPSNum < 1 || p.FPSNum > 60000 || p.FPSDen < 1 || p.FPSDen > 1001 || p.FPSNum < 15*p.FPSDen || p.FPSNum > 60*p.FPSDen ||
		p.VideoKbps < 100 || p.VideoKbps > 8000 || p.AudioKbps < 64 || p.AudioKbps > 320 {
		return errors.New("invalid media profile")
	}
	return nil
}

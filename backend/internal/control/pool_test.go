package control

import (
	"os"
	"path/filepath"
	"streamtool-relay/internal/config"
	"testing"
)

func TestInventoryValidation(t *testing.T) {
	valid := `[{"id":"00000000-0000-4000-8000-000000000101","agent_url":"https://media-a:8443","slots":1,"instance_id":"vm-a"}]`
	for _, tc := range []struct {
		name, body string
		ok         bool
	}{
		{"valid", valid, true},
		{"empty", `[]`, false},
		{"http", `[{"id":"00000000-0000-4000-8000-000000000101","agent_url":"http://media-a","slots":1}]`, false},
		{"credentials", `[{"id":"00000000-0000-4000-8000-000000000101","agent_url":"https://secret@media-a","slots":1}]`, false},
		{"zero-slots", `[{"id":"00000000-0000-4000-8000-000000000101","agent_url":"https://media-a","slots":0}]`, false},
		{"unknown-field", `[{"id":"00000000-0000-4000-8000-000000000101","agent_url":"https://media-a","slots":1,"secret":"no"}]`, false},
		{"trailing", valid + ` {}`, false},
		{"duplicate-vm", `[{"id":"00000000-0000-4000-8000-000000000101","agent_url":"https://media-a","slots":1,"instance_id":"vm"},{"id":"00000000-0000-4000-8000-000000000102","agent_url":"https://media-b","slots":1,"instance_id":"vm"}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "nodes.json")
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadNodes(path)
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestCompressedWorkerBudgetAccountsForBitrate(t *testing.T) {
	c := new(Controller)
	p := config.DefaultMediaProfile()
	low := c.resources(p, 1)
	p.Width = 1920
	p.Height = 1080
	p.FPSNum = 60
	p.VideoKbps = 8000
	p.AudioKbps = 320
	high := c.resources(p, 8)
	if high.CPUMillis != low.CPUMillis || high.MemoryBytes != low.MemoryBytes || high.EgressBPS < int64(p.VideoKbps+p.AudioKbps)*1200*8 || high.Slots != 1 {
		t.Fatal("incorrect media admission budget")
	}
}

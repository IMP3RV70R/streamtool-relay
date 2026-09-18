package control

import (
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
)

type NodeConfig struct {
	ID         string `json:"id"`
	AgentURL   string `json:"agent_url"`
	Slots      int    `json:"slots"`
	InstanceID string `json:"instance_id"`
}

func LoadNodes(path string) ([]NodeConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var nodes []NodeConfig
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err = d.Decode(&nodes); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF {
		return nil, errors.New("trailing inventory data")
	}
	if len(nodes) != 1 {
		return nil, errors.New("self-hosted deployment requires one local node")
	}
	n := nodes[0]
	u, e := url.Parse(n.AgentURL)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || n.ID == "" || n.Slots != 1 {
		return nil, errors.New("invalid local node inventory")
	}
	return nodes, nil
}

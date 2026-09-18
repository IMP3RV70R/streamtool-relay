// Package updaterclient implements the bounded local host protocol, without URLs
// or administrative command/path arguments supplied by the owner.
package updaterclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"streamtool-relay/internal/release"
	"time"
)

type Release struct {
	Digest  string    `json:"digest"`
	Version string    `json:"version"`
	Notes   string    `json:"notes"`
	Expires time.Time `json:"expires"`
}
type Status struct {
	ID                         string `json:"id,omitempty"`
	Digest                     string `json:"digest,omitempty"`
	Phase                      string `json:"phase"`
	InstalledVersion           string `json:"installed_version,omitempty"`
	TargetVersion              string `json:"target_version,omitempty"`
	Error                      string `json:"error,omitempty"`
	RestoredCredentialsRevoked bool   `json:"restored_credentials_revoked,omitempty"`
}
type Service interface {
	Catalog(context.Context) (*Release, error)
	Status(context.Context, string) (Status, error)
	Start(context.Context, string, string) (Status, error)
}
type Client struct{ Path string }
type response struct {
	Protocol int      `json:"protocol"`
	OK       bool     `json:"ok"`
	Release  *Release `json:"release,omitempty"`
	Status
}
type RemoteError struct{ Code string }

func (e *RemoteError) Error() string { return "host updater unavailable" }

func (c *Client) call(ctx context.Context, request map[string]any) (response, error) {
	var result response
	request["protocol"] = 1
	data, err := json.Marshal(request)
	if err != nil || len(data) > 4095 {
		return result, errors.New("invalid updater request")
	}
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", c.Path)
	if err != nil {
		return result, errors.New("host updater unavailable")
	}
	defer connection.Close()
	if err = checkRootPeer(connection); err != nil {
		return result, err
	}
	deadline := time.Now().Add(8 * time.Second)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	if err = connection.SetDeadline(deadline); err != nil {
		return result, errors.New("host updater unavailable")
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			connection.Close()
		case <-done:
		}
	}()
	if _, err = io.Copy(connection, bytes.NewReader(append(data, '\n'))); err != nil {
		return result, errors.New("host updater unavailable")
	}
	body, err := io.ReadAll(io.LimitReader(connection, 32769))
	if err != nil || len(body) > 32768 {
		return result, errors.New("invalid updater response")
	}
	if err = release.Decode(body, &result); err != nil || result.Protocol != 1 {
		return result, errors.New("invalid updater response")
	}
	if !result.OK {
		return result, &RemoteError{Code: result.Error}
	}
	return result, nil
}
func (c *Client) Catalog(ctx context.Context) (*Release, error) {
	r, e := c.call(ctx, map[string]any{"op": "catalog"})
	return r.Release, e
}
func (c *Client) Status(ctx context.Context, id string) (Status, error) {
	q := map[string]any{"op": "status"}
	if id != "" {
		q["id"] = id
	}
	r, e := c.call(ctx, q)
	return r.Status, e
}
func (c *Client) Start(ctx context.Context, id, digest string) (Status, error) {
	r, e := c.call(ctx, map[string]any{"op": "start", "id": id, "digest": digest})
	if e == nil && (r.ID != id || r.Digest != digest || r.Phase == "") {
		e = errors.New("invalid updater receipt")
	}
	return r.Status, e
}

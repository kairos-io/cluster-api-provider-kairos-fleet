/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultTimeout = 30 * time.Second

// httpClient is the default net/http-backed Client implementation. It authenticates
// with the AuroraBoot admin bearer token and speaks the /api/v1 REST surface.
type httpClient struct {
	baseURL   string
	token     string
	userAgent string
	http      *http.Client
}

// Option configures a httpClient.
type Option func(*httpClient)

// WithHTTPClient overrides the underlying *http.Client (e.g. custom transport).
func WithHTTPClient(h *http.Client) Option {
	return func(c *httpClient) {
		if h != nil {
			c.http = h
		}
	}
}

// WithUserAgent sets the User-Agent header on outbound requests.
func WithUserAgent(ua string) Option {
	return func(c *httpClient) {
		if ua != "" {
			c.userAgent = ua
		}
	}
}

// New constructs a Client for the AuroraBoot instance at baseURL, authenticating with
// the given admin token (sent as "Authorization: Bearer <token>"). baseURL must not
// include a trailing slash or path; the client adds /api/v1/... itself.
func New(baseURL, token string, opts ...Option) Client {
	c := &httpClient{
		baseURL:   strings.TrimRight(baseURL, "/"),
		token:     token,
		userAgent: "cluster-api-provider-kairos-fleet",
		http:      &http.Client{Timeout: defaultTimeout},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// APIError is a structured AuroraBoot API error.
type APIError struct {
	StatusCode int    `json:"-"`
	ErrorMsg   string `json:"error"`
	Detail     string `json:"detail,omitempty"`
	// Code is the server's stable, machine-readable discriminator (e.g. "NoCapacity")
	// when several conditions share one HTTP status.
	Code string `json:"code,omitempty"`
}

func (e *APIError) Error() string {
	switch {
	case e.Detail != "":
		return fmt.Sprintf("auroraboot api: %d %s: %s", e.StatusCode, e.ErrorMsg, e.Detail)
	case e.ErrorMsg != "":
		return fmt.Sprintf("auroraboot api: %d %s", e.StatusCode, e.ErrorMsg)
	default:
		return fmt.Sprintf("auroraboot api: %d", e.StatusCode)
	}
}

// IsNoCapacity reports whether err is a claim rejected because the group has no
// unclaimed node (server code "NoCapacity"). Callers wait and retry rather than fail.
func IsNoCapacity(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.Code == "NoCapacity"
}

// IsNotFound reports whether err is an APIError with a 404 status.
func IsNotFound(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.StatusCode == http.StatusNotFound
}

// IsConflict reports whether err is an APIError with a 409 status.
func IsConflict(err error) bool {
	var e *APIError
	return errors.As(err, &e) && e.StatusCode == http.StatusConflict
}

func (c *httpClient) Claim(ctx context.Context, groupID, claimKey string) (*Node, error) {
	var out nodeDTO
	body := map[string]string{"claimKey": claimKey}
	if err := c.do(ctx, http.MethodPost, "/api/v1/groups/"+groupID+"/claim", body, &out); err != nil {
		return nil, err
	}
	return out.toNode(), nil
}

func (c *httpClient) GetNode(ctx context.Context, nodeID string) (*Node, error) {
	var out nodeDTO
	if err := c.do(ctx, http.MethodGet, "/api/v1/nodes/"+nodeID, nil, &out); err != nil {
		return nil, err
	}
	return out.toNode(), nil
}

func (c *httpClient) ApplyCloudConfig(ctx context.Context, nodeID, cloudConfig string) (*Command, error) {
	return c.sendCommand(ctx, nodeID, CommandApplyCloudConfig, map[string]string{ApplyCloudConfigArg: cloudConfig})
}

func (c *httpClient) Reboot(ctx context.Context, nodeID string) (*Command, error) {
	return c.sendCommand(ctx, nodeID, CommandReboot, nil)
}

func (c *httpClient) sendCommand(ctx context.Context, nodeID, command string, args map[string]string) (*Command, error) {
	var out commandDTO
	body := createCommandDTO{Command: command, Args: args}
	if err := c.do(ctx, http.MethodPost, "/api/v1/nodes/"+nodeID+"/commands", body, &out); err != nil {
		return nil, err
	}
	return out.toCommand(), nil
}

func (c *httpClient) GetCommands(ctx context.Context, nodeID string) ([]Command, error) {
	var out []commandDTO
	if err := c.do(ctx, http.MethodGet, "/api/v1/nodes/"+nodeID+"/commands", nil, &out); err != nil {
		return nil, err
	}
	cmds := make([]Command, 0, len(out))
	for i := range out {
		cmds = append(cmds, *out[i].toCommand())
	}
	return cmds, nil
}

func (c *httpClient) Release(ctx context.Context, nodeID, claimKey string) (bool, error) {
	body := map[string]string{"claimKey": claimKey}
	var out struct {
		Released bool `json:"released"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/nodes/"+nodeID+"/release", body, &out); err != nil {
		return false, err
	}
	return out.Released, nil
}

// do performs a JSON request/response against the AuroraBoot API. A non-2xx response
// is decoded into an *APIError.
func (c *httpClient) do(ctx context.Context, method, path string, in, out interface{}) error {
	var reqBody io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("fleet: marshal request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reqBody)
	if err != nil {
		return fmt.Errorf("fleet: build request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("fleet: %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return decodeError(resp)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return fmt.Errorf("fleet: decode response: %w", err)
		}
	}
	return nil
}

func decodeError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	ae := &APIError{StatusCode: resp.StatusCode}
	if len(body) > 0 {
		_ = json.Unmarshal(body, ae)
		if ae.ErrorMsg == "" {
			ae.ErrorMsg = strings.TrimSpace(string(body))
		}
	}
	return ae
}

// nodeDTO mirrors the AuroraBoot /api/v1/nodes JSON shape.
type nodeDTO struct {
	ID            string     `json:"id"`
	MachineID     string     `json:"machineID"`
	Hostname      string     `json:"hostname"`
	GroupID       string     `json:"groupID,omitempty"`
	Phase         string     `json:"phase"`
	ClaimKey      *string    `json:"claimKey,omitempty"`
	LastHeartbeat *time.Time `json:"lastHeartbeat,omitempty"`
}

func (d *nodeDTO) toNode() *Node {
	return &Node{
		ID:            d.ID,
		MachineID:     d.MachineID,
		Hostname:      d.Hostname,
		GroupID:       d.GroupID,
		Phase:         d.Phase,
		ClaimKey:      d.ClaimKey,
		LastHeartbeat: d.LastHeartbeat,
	}
}

// commandDTO mirrors the AuroraBoot NodeCommand JSON shape.
type commandDTO struct {
	ID      string `json:"id"`
	Command string `json:"command"`
	Phase   string `json:"phase"`
	Result  string `json:"result,omitempty"`
}

func (d *commandDTO) toCommand() *Command {
	return &Command{ID: d.ID, Command: d.Command, Phase: d.Phase, Result: d.Result}
}

// createCommandDTO is the POST body for queuing a node command.
type createCommandDTO struct {
	Command string            `json:"command"`
	Args    map[string]string `json:"args,omitempty"`
}

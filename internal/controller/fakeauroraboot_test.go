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

package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

// fakeAuroraBoot is an in-memory implementation of the AuroraBoot fleet /api/v1
// surface the provider drives, backed by an httptest server. It lets the lifecycle
// e2e exercise the *real* net/http fleet client (fleet.New) end to end: idempotent
// group claim, node get, per-node commands, and release. It also simulates the node
// coming back Online with a fresh heartbeat after a reboot, so the controller's
// rejoin gate can be satisfied deterministically.
type fakeAuroraBoot struct {
	mu  sync.Mutex
	srv *httptest.Server

	// nodes indexed by node ID; groupPool is the queue of unclaimed node IDs per group.
	nodes     map[string]*fakeNode
	groupPool map[string][]string
	byClaim   map[string]string // claimKey -> node ID (idempotent claim)

	// Recorded calls, for assertions.
	applied  map[string]string // node ID -> cloud-config handed to apply-cloud-config
	rebooted map[string]bool
	released map[string]string // node ID -> claimKey used to release
}

type fakeNode struct {
	ID            string
	MachineID     string
	Hostname      string
	GroupID       string
	Phase         string
	ClaimKey      *string
	LastHeartbeat *time.Time
	commands      []fakeCommand
}

type fakeCommand struct {
	ID      string
	Command string
	Phase   string
}

// newFakeAuroraBoot starts a server seeded with the given nodes in their groups.
func newFakeAuroraBoot(seed map[string][]fakeNode) *fakeAuroraBoot {
	f := &fakeAuroraBoot{
		nodes:     map[string]*fakeNode{},
		groupPool: map[string][]string{},
		byClaim:   map[string]string{},
		applied:   map[string]string{},
		rebooted:  map[string]bool{},
		released:  map[string]string{},
	}
	for group, ns := range seed {
		for i := range ns {
			n := ns[i]
			n.GroupID = group
			if n.Phase == "" {
				n.Phase = "Online"
			}
			f.nodes[n.ID] = &n
			f.groupPool[group] = append(f.groupPool[group], n.ID)
		}
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.route))
	return f
}

func (f *fakeAuroraBoot) URL() string { return f.srv.URL }
func (f *fakeAuroraBoot) Close()      { f.srv.Close() }

func (f *fakeAuroraBoot) route(w http.ResponseWriter, r *http.Request) {
	// /api/v1/groups/{id}/claim
	// /api/v1/nodes/{id}
	// /api/v1/nodes/{id}/commands
	// /api/v1/nodes/{id}/release
	p := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	parts := strings.Split(p, "/")
	f.mu.Lock()
	defer f.mu.Unlock()

	switch {
	case len(parts) == 3 && parts[0] == "groups" && parts[2] == "claim" && r.Method == http.MethodPost:
		f.handleClaim(w, r, parts[1])
	case len(parts) == 2 && parts[0] == "nodes" && r.Method == http.MethodGet:
		f.handleGetNode(w, parts[1])
	case len(parts) == 3 && parts[0] == "nodes" && parts[2] == "commands" && r.Method == http.MethodPost:
		f.handleCreateCommand(w, r, parts[1])
	case len(parts) == 3 && parts[0] == "nodes" && parts[2] == "commands" && r.Method == http.MethodGet:
		f.handleGetCommands(w, parts[1])
	case len(parts) == 3 && parts[0] == "nodes" && parts[2] == "release" && r.Method == http.MethodPost:
		f.handleRelease(w, r, parts[1])
	default:
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
	}
}

func (f *fakeAuroraBoot) handleClaim(w http.ResponseWriter, r *http.Request, group string) {
	var body struct {
		ClaimKey string `json:"claimKey"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	// Idempotent: the same claimKey re-finds its node.
	if id, ok := f.byClaim[body.ClaimKey]; ok {
		writeJSON(w, http.StatusOK, f.nodeDTO(f.nodes[id]))
		return
	}
	pool := f.groupPool[group]
	if len(pool) == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no capacity", "code": "NoCapacity"})
		return
	}
	id := pool[0]
	f.groupPool[group] = pool[1:]
	n := f.nodes[id]
	ck := body.ClaimKey
	n.ClaimKey = &ck
	f.byClaim[ck] = id
	writeJSON(w, http.StatusOK, f.nodeDTO(n))
}

func (f *fakeAuroraBoot) handleGetNode(w http.ResponseWriter, id string) {
	n, ok := f.nodes[id]
	if !ok {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, f.nodeDTO(n))
}

func (f *fakeAuroraBoot) handleCreateCommand(w http.ResponseWriter, r *http.Request, id string) {
	n, ok := f.nodes[id]
	if !ok {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	var body struct {
		Command string            `json:"command"`
		Args    map[string]string `json:"args"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	cmd := fakeCommand{ID: "cmd-" + body.Command, Command: body.Command, Phase: "Pending"}
	switch body.Command {
	case "apply-cloud-config":
		// The agent writes the config and marks the command completed.
		f.applied[id] = body.Args["config"]
		cmd.Phase = "Completed"
	case "reboot":
		// The node reboots and comes back Online with a heartbeat clearly newer
		// than the reboot request, so the controller detects the rejoin.
		f.rebooted[id] = true
		n.Phase = "Online"
		hb := time.Now().Add(time.Minute)
		n.LastHeartbeat = &hb
		cmd.Phase = "Completed"
	}
	n.commands = append(n.commands, cmd)
	writeJSON(w, http.StatusOK, map[string]string{"id": cmd.ID, "command": cmd.Command, "phase": cmd.Phase})
}

func (f *fakeAuroraBoot) handleGetCommands(w http.ResponseWriter, id string) {
	n, ok := f.nodes[id]
	if !ok {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	out := make([]map[string]string, 0, len(n.commands))
	for _, c := range n.commands {
		out = append(out, map[string]string{"id": c.ID, "command": c.Command, "phase": c.Phase})
	}
	writeJSON(w, http.StatusOK, out)
}

func (f *fakeAuroraBoot) handleRelease(w http.ResponseWriter, r *http.Request, id string) {
	var body struct {
		ClaimKey string `json:"claimKey"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	n, ok := f.nodes[id]
	if !ok {
		http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		return
	}
	released := false
	if n.ClaimKey != nil && *n.ClaimKey == body.ClaimKey {
		f.released[id] = body.ClaimKey
		delete(f.byClaim, body.ClaimKey)
		n.ClaimKey = nil
		released = true
	}
	writeJSON(w, http.StatusOK, map[string]bool{"released": released})
}

func (f *fakeAuroraBoot) nodeDTO(n *fakeNode) map[string]any {
	dto := map[string]any{
		"id":        n.ID,
		"machineID": n.MachineID,
		"hostname":  n.Hostname,
		"groupID":   n.GroupID,
		"phase":     n.Phase,
	}
	if n.ClaimKey != nil {
		dto["claimKey"] = *n.ClaimKey
	}
	if n.LastHeartbeat != nil {
		dto["lastHeartbeat"] = n.LastHeartbeat.Format(time.RFC3339Nano)
	}
	return dto
}

// wasReleased reports whether the node was released with the given claim key.
func (f *fakeAuroraBoot) wasReleased(nodeID, claimKey string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.released[nodeID] == claimKey
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

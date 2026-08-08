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

// Package fleet is a small, self-contained client for the subset of the AuroraBoot
// fleet API (/api/v1) that the provider drives: claim a node from a group, apply a
// cloud-config, read node/command state, and release. It is deliberately narrow and
// dependency-free (net/http only) rather than importing github.com/kairos-io/AuroraBoot,
// whose module pins k8s.io and controller-runtime ahead of the Cluster API v1.13
// toolchain this provider targets. See ADR 0001 §8.
package fleet

import (
	"context"
	"time"
)

// Node lifecycle phases reported by AuroraBoot.
const (
	PhasePending    = "Pending"
	PhaseRegistered = "Registered"
	PhaseOnline     = "Online"
	PhaseOffline    = "Offline"
)

// Node command names recognised by the AuroraBoot server / phone-home agent.
// Mirrors AuroraBoot pkg/store CmdApplyCloudConfig / CmdReboot / CmdReset.
const (
	CommandApplyCloudConfig = "apply-cloud-config"
	CommandReboot           = "reboot"
	CommandReset            = "reset"

	// ApplyCloudConfigArg is the command argument key the agent reads the
	// cloud-config from (kairos-agent internal/phonehome/handlers.go).
	ApplyCloudConfigArg = "config"
)

// Command execution phases reported by AuroraBoot.
const (
	CommandPhasePending   = "Pending"
	CommandPhaseDelivered = "Delivered"
	CommandPhaseRunning   = "Running"
	CommandPhaseCompleted = "Completed"
	CommandPhaseFailed    = "Failed"
	CommandPhaseExpired   = "Expired"
)

// Node is the subset of an AuroraBoot managed node the provider needs. AuroraBoot's
// node representation does not (yet) expose structured addresses or a boot state; the
// provider derives machine addresses from Hostname and readiness from Phase.
type Node struct {
	ID            string
	MachineID     string
	Hostname      string
	GroupID       string
	Phase         string
	ClaimKey      *string
	LastHeartbeat *time.Time
}

// Command is a queued node command and its execution state.
type Command struct {
	ID      string
	Command string
	Phase   string
	Result  string
}

// Client is the fleet API surface the controllers depend on. It is intentionally
// small so it can be faked in tests (see FakeClient).
type Client interface {
	// Claim atomically assigns one unclaimed node in the group to claimKey and
	// returns it. It is idempotent: the same claimKey re-finds the same node. When
	// the group has no unclaimed node the error satisfies IsNoCapacity.
	Claim(ctx context.Context, groupID, claimKey string) (*Node, error)

	// GetNode returns the current state of a node by ID. A missing node yields an
	// error satisfying IsNotFound.
	GetNode(ctx context.Context, nodeID string) (*Node, error)

	// ApplyCloudConfig queues an apply-cloud-config command carrying the (unmodified)
	// bootstrap cloud-config. The agent writes it to /oem and does NOT reboot, so the
	// caller must issue Reboot afterwards for the config to take effect.
	ApplyCloudConfig(ctx context.Context, nodeID, cloudConfig string) (*Command, error)

	// Reboot queues a reboot command so the node processes a previously applied
	// cloud-config (which is staged under /oem and only applied on boot).
	Reboot(ctx context.Context, nodeID string) (*Command, error)

	// GetCommands returns the commands queued/executed for a node, newest state
	// included, so the controller can observe an apply-cloud-config reaching
	// CommandPhaseCompleted.
	GetCommands(ctx context.Context, nodeID string) ([]Command, error)

	// Release clears the node's claim if (and only if) it is held by claimKey,
	// returning it to the group's pool. released reports whether a claim was actually
	// cleared. A claim held by a different key yields an error satisfying IsConflict.
	Release(ctx context.Context, nodeID, claimKey string) (released bool, err error)
}

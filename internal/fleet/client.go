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

// Node address types reported by AuroraBoot. They mirror Cluster API's
// MachineAddressType values so a reported address can be surfaced on
// Machine.status.addresses unchanged. AuroraBoot stores the type as a free-form
// string so that a future type needs no server change, which means the provider
// must treat any other value as unknown rather than pass it through: Cluster API
// constrains MachineAddress.type to an enum, and the apiserver rejects a status
// carrying anything else.
const (
	AddressInternalIP = "InternalIP"
	AddressExternalIP = "ExternalIP"
	AddressHostname   = "Hostname"
)

// NodeAddress is one network address a node reports, mirroring AuroraBoot's
// store.NodeAddress and Cluster API's MachineAddress: a {type, address} pair.
// Nodes report a list, because multi-NIC is normal.
type NodeAddress struct {
	Type    string
	Address string
}

// Node is the subset of an AuroraBoot managed node the provider needs. AuroraBoot's
// node representation does not (yet) expose a boot state; the provider derives
// readiness from Phase.
type Node struct {
	ID            string
	MachineID     string
	Hostname      string
	GroupID       string
	Phase         string
	ClaimKey      *string
	LastHeartbeat *time.Time
	// Addresses are the network addresses the node itself reported at register or
	// heartbeat time. The field is optional: an agent that does not collect them,
	// or one older than the field, sends none, so a caller must still be able to
	// fall back to Hostname.
	Addresses []NodeAddress
}

// Command is a queued node command and its execution state. CreatedAt orders a
// node's commands: AuroraBoot keeps every command it has ever queued for a node,
// so a caller looking for "the outcome of the apply I just issued" must select by
// ID, or failing that by recency — never by first match.
type Command struct {
	ID        string
	Command   string
	Phase     string
	Result    string
	CreatedAt *time.Time
}

// Group is the subset of an AuroraBoot group the provider needs to resolve a
// human-friendly name (as used in KairosFleetMachine.spec.group) to the group's ID.
type Group struct {
	ID   string
	Name string
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

	// ResolveGroupID resolves an AuroraBoot group reference — a human name (as used in
	// KairosFleetMachine.spec.group) or an already-resolved UUID — to the group's ID.
	// AuroraBoot's claim endpoint keys on the ID; spec.group is a name. A reference that
	// matches no group yields an error satisfying IsNotFound.
	ResolveGroupID(ctx context.Context, ref string) (groupID string, err error)
}

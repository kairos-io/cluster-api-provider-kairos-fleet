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
	"strings"
	"testing"

	clusterv1 "sigs.k8s.io/cluster-api/api/core/v1beta2"

	"github.com/kairos-io/cluster-api-provider-kairos-fleet/internal/fleet"
)

func TestAddressesFromNode(t *testing.T) {
	tests := []struct {
		name string
		node *fleet.Node
		want []clusterv1.MachineAddress
	}{
		{
			name: "reported addresses are surfaced alongside the hostname",
			node: &fleet.Node{Hostname: "worker-1", Addresses: []fleet.NodeAddress{
				{Type: fleet.AddressInternalIP, Address: "10.0.0.7"},
				{Type: fleet.AddressExternalIP, Address: "203.0.113.9"},
			}},
			want: []clusterv1.MachineAddress{
				{Type: clusterv1.MachineHostName, Address: "worker-1"},
				{Type: clusterv1.MachineInternalIP, Address: "10.0.0.7"},
				{Type: clusterv1.MachineExternalIP, Address: "203.0.113.9"},
			},
		},
		{
			name: "multi-NIC: every reported InternalIP is kept, in report order",
			node: &fleet.Node{Hostname: "worker-1", Addresses: []fleet.NodeAddress{
				{Type: fleet.AddressInternalIP, Address: "10.0.0.7"},
				{Type: fleet.AddressInternalIP, Address: "192.168.1.7"},
			}},
			want: []clusterv1.MachineAddress{
				{Type: clusterv1.MachineHostName, Address: "worker-1"},
				{Type: clusterv1.MachineInternalIP, Address: "10.0.0.7"},
				{Type: clusterv1.MachineInternalIP, Address: "192.168.1.7"},
			},
		},
		{
			name: "an agent that reports nothing still yields the hostname",
			node: &fleet.Node{Hostname: "worker-1"},
			want: []clusterv1.MachineAddress{
				{Type: clusterv1.MachineHostName, Address: "worker-1"},
			},
		},
		{
			name: "a hostname the node reports itself is not duplicated",
			node: &fleet.Node{Hostname: "worker-1", Addresses: []fleet.NodeAddress{
				{Type: fleet.AddressHostname, Address: "worker-1"},
				{Type: fleet.AddressInternalIP, Address: "10.0.0.7"},
			}},
			want: []clusterv1.MachineAddress{
				{Type: clusterv1.MachineHostName, Address: "worker-1"},
				{Type: clusterv1.MachineInternalIP, Address: "10.0.0.7"},
			},
		},
		{
			name: "a reported hostname that differs from the node's is kept as well",
			node: &fleet.Node{Hostname: "worker-1", Addresses: []fleet.NodeAddress{
				{Type: fleet.AddressHostname, Address: "worker-1.example.com"},
			}},
			want: []clusterv1.MachineAddress{
				{Type: clusterv1.MachineHostName, Address: "worker-1"},
				{Type: clusterv1.MachineHostName, Address: "worker-1.example.com"},
			},
		},
		{
			name: "an exact duplicate is reported once",
			node: &fleet.Node{Hostname: "worker-1", Addresses: []fleet.NodeAddress{
				{Type: fleet.AddressInternalIP, Address: "10.0.0.7"},
				{Type: fleet.AddressInternalIP, Address: "10.0.0.7"},
			}},
			want: []clusterv1.MachineAddress{
				{Type: clusterv1.MachineHostName, Address: "worker-1"},
				{Type: clusterv1.MachineInternalIP, Address: "10.0.0.7"},
			},
		},
		{
			name: "a node with neither a hostname nor addresses reports none",
			node: &fleet.Node{},
			want: nil,
		},
		{
			name: "a node with addresses but no hostname reports only the addresses",
			node: &fleet.Node{Addresses: []fleet.NodeAddress{
				{Type: fleet.AddressInternalIP, Address: "10.0.0.7"},
			}},
			want: []clusterv1.MachineAddress{
				{Type: clusterv1.MachineInternalIP, Address: "10.0.0.7"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := addressesFromNode(tt.node)
			assertAddresses(t, got, tt.want)
		})
	}
}

// Cluster API constrains MachineAddress.type to an enum and MachineAddress.address
// to 1..256 characters, so anything AuroraBoot passed through that does not satisfy
// both would make the apiserver reject the whole status update. A node that reports
// one unusable address must not cost the machine the addresses that are usable, nor
// its providerID and Ready condition.
func TestAddressesFromNode_DropsWhatClusterAPIWouldReject(t *testing.T) {
	tests := []struct {
		name string
		addr fleet.NodeAddress
	}{
		{"a type Cluster API does not define", fleet.NodeAddress{Type: "LinkLocalIP", Address: "169.254.0.1"}},
		{"an empty type", fleet.NodeAddress{Type: "", Address: "10.0.0.8"}},
		{"a type differing only in case", fleet.NodeAddress{Type: "internalip", Address: "10.0.0.8"}},
		{"an empty address", fleet.NodeAddress{Type: fleet.AddressInternalIP, Address: ""}},
		// InternalDNS is in Cluster API's enum but is not one of the three types
		// AuroraBoot documents; the address, not the type, is what fails here.
		{"an address past the 256 character limit", fleet.NodeAddress{
			Type: "InternalDNS", Address: strings.Repeat("a", 257)}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &fleet.Node{Hostname: "worker-1", Addresses: []fleet.NodeAddress{
				tt.addr,
				{Type: fleet.AddressInternalIP, Address: "10.0.0.7"},
			}}
			assertAddresses(t, addressesFromNode(node), []clusterv1.MachineAddress{
				{Type: clusterv1.MachineHostName, Address: "worker-1"},
				{Type: clusterv1.MachineInternalIP, Address: "10.0.0.7"},
			})
		})
	}
}

// A hostname longer than Cluster API accepts is dropped like any other address,
// rather than making the status unwritable.
func TestAddressesFromNode_DropsAnOverlongHostname(t *testing.T) {
	node := &fleet.Node{Hostname: strings.Repeat("a", 257)}
	assertAddresses(t, addressesFromNode(node), nil)
}

func assertAddresses(t *testing.T, got, want []clusterv1.MachineAddress) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("addresses = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("addresses[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

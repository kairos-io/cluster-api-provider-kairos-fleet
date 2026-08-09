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
	"context"
	"net/http"
	"sync"
)

// FakeClient is an in-memory Client for tests. Set the *Func fields to control
// behaviour; unset methods return zero values / nil errors. Calls are recorded.
type FakeClient struct {
	mu sync.Mutex

	ClaimFunc            func(ctx context.Context, groupID, claimKey string) (*Node, error)
	GetNodeFunc          func(ctx context.Context, nodeID string) (*Node, error)
	ApplyCloudConfigFunc func(ctx context.Context, nodeID, cloudConfig string) (*Command, error)
	RebootFunc           func(ctx context.Context, nodeID string) (*Command, error)
	GetCommandsFunc      func(ctx context.Context, nodeID string) ([]Command, error)
	ReleaseFunc          func(ctx context.Context, nodeID, claimKey string) (bool, error)
	ResolveGroupIDFunc   func(ctx context.Context, ref string) (string, error)

	Claims   []ClaimCall
	Applies  []ApplyCall
	Reboots  []string
	Releases []ReleaseCall
}

// ClaimCall records a Claim invocation.
type ClaimCall struct{ GroupID, ClaimKey string }

// ApplyCall records an ApplyCloudConfig invocation.
type ApplyCall struct{ NodeID, CloudConfig string }

// ReleaseCall records a Release invocation.
type ReleaseCall struct{ NodeID, ClaimKey string }

var _ Client = (*FakeClient)(nil)

// NoCapacityError returns an error that satisfies IsNoCapacity, for tests.
func NoCapacityError() error {
	return &APIError{StatusCode: http.StatusConflict, ErrorMsg: "no capacity", Code: "NoCapacity"}
}

// NotFoundError returns an error that satisfies IsNotFound, for tests.
func NotFoundError() error {
	return &APIError{StatusCode: http.StatusNotFound, ErrorMsg: "not found"}
}

func (f *FakeClient) Claim(ctx context.Context, groupID, claimKey string) (*Node, error) {
	f.mu.Lock()
	f.Claims = append(f.Claims, ClaimCall{GroupID: groupID, ClaimKey: claimKey})
	f.mu.Unlock()
	if f.ClaimFunc != nil {
		return f.ClaimFunc(ctx, groupID, claimKey)
	}
	return nil, nil
}

func (f *FakeClient) GetNode(ctx context.Context, nodeID string) (*Node, error) {
	if f.GetNodeFunc != nil {
		return f.GetNodeFunc(ctx, nodeID)
	}
	return nil, nil
}

func (f *FakeClient) ApplyCloudConfig(ctx context.Context, nodeID, cloudConfig string) (*Command, error) {
	f.mu.Lock()
	f.Applies = append(f.Applies, ApplyCall{NodeID: nodeID, CloudConfig: cloudConfig})
	f.mu.Unlock()
	if f.ApplyCloudConfigFunc != nil {
		return f.ApplyCloudConfigFunc(ctx, nodeID, cloudConfig)
	}
	return &Command{ID: "fake-cmd", Command: CommandApplyCloudConfig, Phase: CommandPhasePending}, nil
}

func (f *FakeClient) Reboot(ctx context.Context, nodeID string) (*Command, error) {
	f.mu.Lock()
	f.Reboots = append(f.Reboots, nodeID)
	f.mu.Unlock()
	if f.RebootFunc != nil {
		return f.RebootFunc(ctx, nodeID)
	}
	return &Command{ID: "fake-reboot", Command: CommandReboot, Phase: CommandPhasePending}, nil
}

func (f *FakeClient) GetCommands(ctx context.Context, nodeID string) ([]Command, error) {
	if f.GetCommandsFunc != nil {
		return f.GetCommandsFunc(ctx, nodeID)
	}
	return nil, nil
}

func (f *FakeClient) Release(ctx context.Context, nodeID, claimKey string) (bool, error) {
	f.mu.Lock()
	f.Releases = append(f.Releases, ReleaseCall{NodeID: nodeID, ClaimKey: claimKey})
	f.mu.Unlock()
	if f.ReleaseFunc != nil {
		return f.ReleaseFunc(ctx, nodeID, claimKey)
	}
	return true, nil
}

func (f *FakeClient) ResolveGroupID(ctx context.Context, ref string) (string, error) {
	if f.ResolveGroupIDFunc != nil {
		return f.ResolveGroupIDFunc(ctx, ref)
	}
	// Default: identity, so existing fake-based tests that pass group ids directly
	// keep passing unchanged.
	return ref, nil
}

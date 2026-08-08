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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClaim_ParsesNodeAndSendsClaimKey(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"n1","machineID":"mid","hostname":"h1","phase":"Online"}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	node, err := c.Claim(context.Background(), "grp", "ck")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("Authorization = %q, want Bearer tok", gotAuth)
	}
	if gotPath != "/api/v1/groups/grp/claim" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["claimKey"] != "ck" {
		t.Errorf("claimKey = %q", gotBody["claimKey"])
	}
	if node.ID != "n1" || node.Hostname != "h1" || node.Phase != PhaseOnline {
		t.Errorf("node = %+v", node)
	}
}

func TestClaim_NoCapacityClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":"group empty","code":"NoCapacity"}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "tok").Claim(context.Background(), "grp", "ck")
	if err == nil {
		t.Fatal("expected error")
	}
	if !IsNoCapacity(err) {
		t.Errorf("IsNoCapacity(%v) = false, want true", err)
	}
}

func TestApplyCloudConfig_SendsConfigArg(t *testing.T) {
	var gotPath string
	var gotBody createCommandDTO
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c1","command":"apply-cloud-config","phase":"Pending"}`))
	}))
	defer srv.Close()

	cmd, err := New(srv.URL, "tok").ApplyCloudConfig(context.Background(), "n1", "#cloud-config\n")
	if err != nil {
		t.Fatalf("ApplyCloudConfig: %v", err)
	}
	if gotPath != "/api/v1/nodes/n1/commands" {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody.Command != CommandApplyCloudConfig || gotBody.Args[ApplyCloudConfigArg] != "#cloud-config\n" {
		t.Errorf("body = %+v", gotBody)
	}
	if cmd.ID != "c1" {
		t.Errorf("cmd = %+v", cmd)
	}
}

func TestRelease_ParsesReleased(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"released":true}`))
	}))
	defer srv.Close()

	released, err := New(srv.URL, "tok").Release(context.Background(), "n1", "ck")
	if err != nil {
		t.Fatalf("Release: %v", err)
	}
	if gotPath != "/api/v1/nodes/n1/release" {
		t.Errorf("path = %q", gotPath)
	}
	if !released {
		t.Errorf("released = false, want true")
	}
}

func TestGetNode_NotFoundClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer srv.Close()

	_, err := New(srv.URL, "tok").GetNode(context.Background(), "nope")
	if !IsNotFound(err) {
		t.Errorf("IsNotFound(%v) = false, want true", err)
	}
}

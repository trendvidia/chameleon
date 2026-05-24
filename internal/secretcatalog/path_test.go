// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package secretcatalog

import "testing"

func TestSchemaPath(t *testing.T) {
	cases := []struct {
		name string
		path []PathStep
		want string
	}{
		{
			name: "top-level scalar",
			path: []PathStep{{Name: "db_password", Container: ContainerScalar}},
			want: "db_password",
		},
		{
			name: "nested scalar",
			path: []PathStep{
				{Name: "primary", Container: ContainerScalar},
				{Name: "auth_token", Container: ContainerScalar},
			},
			want: "primary.auth_token",
		},
		{
			name: "repeated of secret",
			path: []PathStep{{Name: "backup_keys", Container: ContainerRepeated}},
			want: "backup_keys",
		},
		{
			name: "map of secret",
			path: []PathStep{{Name: "tenant_keys", Container: ContainerMap}},
			want: "tenant_keys",
		},
		{
			name: "map of message with nested secret",
			path: []PathStep{
				{Name: "sites", Container: ContainerMap},
				{Name: "auth_token", Container: ContainerScalar},
			},
			want: "sites.auth_token",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Site{Path: tc.path}.SchemaPath()
			if got != tc.want {
				t.Errorf("SchemaPath = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDottedPath(t *testing.T) {
	cases := []struct {
		name string
		path []PathStep
		want string
	}{
		{
			name: "top-level scalar",
			path: []PathStep{{Name: "db_password", Container: ContainerScalar}},
			want: "db_password",
		},
		{
			name: "repeated secret",
			path: []PathStep{{Name: "backup_keys", Container: ContainerRepeated}},
			want: "backup_keys[i]",
		},
		{
			name: "map secret",
			path: []PathStep{{Name: "tenant_keys", Container: ContainerMap}},
			want: "tenant_keys[key]",
		},
		{
			name: "repeated-of-message with nested scalar secret",
			path: []PathStep{
				{Name: "replicas", Container: ContainerRepeated},
				{Name: "auth_token", Container: ContainerScalar},
			},
			want: "replicas[i].auth_token",
		},
		{
			name: "map-of-message with nested repeated secret",
			path: []PathStep{
				{Name: "sites", Container: ContainerMap},
				{Name: "backup_tokens", Container: ContainerRepeated},
			},
			// Repeated and map counters are independent —
			// the first repeated is `i` regardless of how many
			// map containers preceded it.
			want: "sites[key].backup_tokens[i]",
		},
		{
			name: "two repeats — placeholders advance i, j",
			path: []PathStep{
				{Name: "regions", Container: ContainerRepeated},
				{Name: "shards", Container: ContainerRepeated},
				{Name: "auth_token", Container: ContainerScalar},
			},
			want: "regions[i].shards[j].auth_token",
		},
		{
			name: "two maps — placeholders advance key, key2",
			path: []PathStep{
				{Name: "tenants", Container: ContainerMap},
				{Name: "envs", Container: ContainerMap},
				{Name: "api_key", Container: ContainerScalar},
			},
			want: "tenants[key].envs[key2].api_key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Site{Path: tc.path}.DottedPath()
			if got != tc.want {
				t.Errorf("DottedPath = %q, want %q", got, tc.want)
			}
		})
	}
}

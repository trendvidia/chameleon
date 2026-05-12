// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package chain

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"sync"

	"github.com/bufbuild/protocompile"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// chainProtoSrc is the canonical chameleon.v1.ChainSpec definition,
// compiled at runtime by [LoadSpec] via bufbuild/protocompile. Kept
// here as a string so chameleon doesn't need a codegen step on its
// own .proto files. When P2 #6 lands (typed bindings), this becomes
// dead and is replaced with the generated descriptor.
const chainProtoSrc = `
syntax = "proto3";
package chameleon.v1;

message ChainSpec {
  string version = 1;
  string default_environment = 2;
  repeated Environment environments = 3;
  repeated LayerEntry layers = 4;
  repeated ContextKey context_keys = 5;
  map<string, SourceRef> sources = 6;
}

message Environment {
  string name = 1;
  string extends = 2;
  map<string, string> labels = 3;
}

message LayerEntry {
  string id = 1;
  string path = 2;
  ForEach for_each = 3;
  bool required = 4;
  bool encrypted = 5;
  string source = 6;

  enum ForEach {
    ONCE = 0;
    PER_ENV = 1;
  }
}

message SourceRef {
  string kind = 1;
  map<string, string> options = 2;
}

message ContextKey {
  string name = 1;
  string description = 2;
  repeated string allowed_values = 3;
  string default = 4;
  bool required = 5;
}
`

// LoadSpec reads a chain.pxf file from disk and parses it as a
// chameleon.v1.ChainSpec. Returns the parsed message ready to pass
// to [Expand] or [resolver.New].
//
// The ChainSpec descriptor is compiled at runtime — no codegen step
// is required. This is the convenience entry point for callers that
// don't already have a parsed spec.
func LoadSpec(path string) (proto.Message, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("chain: read %s: %w", path, err)
	}
	return ParseSpec(data)
}

// ParseSpec parses chain.pxf bytes as a ChainSpec. Useful when the
// spec lives in an embed.FS, a network response, or otherwise isn't
// a filesystem path. [LoadSpec] is the file convenience.
func ParseSpec(data []byte) (proto.Message, error) {
	desc, err := chainSpecDescriptor()
	if err != nil {
		return nil, err
	}
	msg := dynamicpb.NewMessage(desc)
	if err := pxf.Unmarshal(data, msg); err != nil {
		return nil, fmt.Errorf("chain: parse: %w", err)
	}
	return msg, nil
}

// chainSpecDescriptor compiles chainProtoSrc once per process and
// caches the descriptor. Safe for concurrent first-callers — the
// prior unsynchronized-package-variable cache (cachedDesc /
// cachedErr / cachedSet) had two races on first-use: (1) two
// concurrent callers could both run the expensive protocompile pass,
// and (2) a caller that observed cachedSet=true before the writer's
// assignment to cachedDesc had been flushed could return (nil, nil).
// sync.OnceValues handles both: exactly one call runs the body,
// concurrent callers block on it, and the (desc, err) tuple is
// published atomically.
var chainSpecDescriptor = sync.OnceValues(func() (protoreflect.MessageDescriptor, error) {
	comp := protocompile.Compiler{
		Resolver: protocompile.WithStandardImports(
			&protocompile.SourceResolver{Accessor: protocompile.SourceAccessorFromMap(
				map[string]string{"chain.proto": chainProtoSrc},
			)},
		),
	}
	files, err := comp.Compile(context.Background(), "chain.proto")
	if err != nil {
		return nil, fmt.Errorf("chain: compile descriptor: %w", err)
	}
	d, err := files.AsResolver().FindDescriptorByName("chameleon.v1.ChainSpec")
	if err != nil {
		return nil, fmt.Errorf("chain: resolve ChainSpec descriptor: %w", err)
	}
	md, ok := d.(protoreflect.MessageDescriptor)
	if !ok {
		return nil, fmt.Errorf("chain: ChainSpec is not a message descriptor")
	}
	return md, nil
})

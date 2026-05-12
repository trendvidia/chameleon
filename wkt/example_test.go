// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package wkt_test

import (
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/trendvidia/chameleon/wkt"
)

// ExampleAtomic demonstrates how the chameleon merge / interpolate /
// defaults / validate pipeline decides whether to recurse into a
// nested message or treat it as an atomic value. WKTs that model
// values (Timestamp, Duration, Secret, etc.) get replaced wholesale
// rather than field-by-field merged; user-defined messages and
// non-atomic WKTs like Struct are descended.
func ExampleAtomic() {
	atomic := []protoreflect.MessageDescriptor{
		(&timestamppb.Timestamp{}).ProtoReflect().Descriptor(),
		(&durationpb.Duration{}).ProtoReflect().Descriptor(),
	}
	nonAtomic := []protoreflect.MessageDescriptor{
		(&structpb.Struct{}).ProtoReflect().Descriptor(),
	}

	for _, d := range atomic {
		fmt.Printf("%-30s atomic=%v\n", d.FullName(), wkt.Atomic(d))
	}
	for _, d := range nonAtomic {
		fmt.Printf("%-30s atomic=%v\n", d.FullName(), wkt.Atomic(d))
	}
	// Output:
	// google.protobuf.Timestamp      atomic=true
	// google.protobuf.Duration       atomic=true
	// google.protobuf.Struct         atomic=false
}

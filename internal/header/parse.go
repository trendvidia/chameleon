// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package header consumes `@header chameleon.v1.LayerHeader { ... }`
// preambles attached to layer files.
//
// protowire-go's PXF lexer recognizes `@<directive>` natively (since
// v0.72.0) and records each directive's body bytes on
// `pxf.Result.Directives()`. chameleon iterates the recorded
// directives, parses each as a `chameleon.v1.LayerHeader`, and runs
// sanity checks against the chain entry the layer was loaded for.
package header

import (
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"

	chameleonv1 "github.com/trendvidia/chameleon/proto/chameleon/v1"
	"github.com/trendvidia/protowire-go/encoding/pxf"
)

// TypeName is the only @header type chameleon currently understands.
const TypeName = "chameleon.v1.LayerHeader"

// DirectiveName is the directive keyword (the bit after `@`). Always
// "header" in chameleon's vocabulary.
const DirectiveName = "header"

// Parsed is the result of decoding an @header inner block.
type Parsed struct {
	msg    protoreflect.Message
	result *pxf.Result
}

// Ref carries the chain-side facts a header is checked against. Mirrors
// the relevant fields of chain.LayerRef without taking a package
// dependency (which would also pull the resolver into a cycle via
// loadAndParse → header → chain).
type Ref struct {
	EntryID   string
	Env       string
	Encrypted bool
}

// Parse decodes header body bytes — the bytes between the braces of a
// `@header <type> { ... }` block. Typically obtained from
// `pxf.Result.Directives()[i].Body` after a layer-file unmarshal.
func Parse(inner []byte) (*Parsed, error) {
	msg := dynamicpb.NewMessage(descriptor())
	res, err := pxf.UnmarshalFull(inner, msg)
	if err != nil {
		return nil, fmt.Errorf("header: parse: %w", err)
	}
	return &Parsed{msg: msg.ProtoReflect(), result: res}, nil
}

// Check enforces the header's sanity-check assertions against the
// chain-side facts the layer was loaded for. A field that the author
// omitted is not checked (three-state presence semantics).
//
// `notes` is informational and not checked. `content_sha256` is parsed
// but not yet verified at resolve time — see chameleon ROADMAP.md and
// the `chameleon verify` CLI subcommand for on-demand verification.
func (p *Parsed) Check(ref Ref) error {
	fields := p.msg.Descriptor().Fields()

	if p.result.IsSet("id") {
		got := p.msg.Get(fields.ByName("id")).String()
		if got != ref.EntryID {
			return fmt.Errorf("@header.id = %q does not match chain entry id %q", got, ref.EntryID)
		}
	}
	if p.result.IsSet("environment") {
		got := p.msg.Get(fields.ByName("environment")).String()
		if got != ref.Env {
			return fmt.Errorf("@header.environment = %q does not match resolved env %q", got, ref.Env)
		}
	}
	if p.result.IsSet("encrypted") {
		got := p.msg.Get(fields.ByName("encrypted")).Bool()
		if got != ref.Encrypted {
			return fmt.Errorf("@header.encrypted = %v does not match chain entry encrypted = %v", got, ref.Encrypted)
		}
	}
	return nil
}

// HandleDirectives walks a list of directives recorded by the PXF
// decoder, parses each @header against the canonical LayerHeader
// shape, and runs Check against ref. Unknown directives (anything
// other than @header) are an error: chameleon owns the layer-file
// directive vocabulary.
func HandleDirectives(dirs []pxf.Directive, ref Ref) error {
	for _, dir := range dirs {
		if dir.Name != DirectiveName {
			return fmt.Errorf("unknown directive @%s at %v", dir.Name, dir.Pos)
		}
		if dir.Type != TypeName {
			return fmt.Errorf("@header type must be %q, got %q", TypeName, dir.Type)
		}
		p, err := Parse(dir.Body)
		if err != nil {
			return fmt.Errorf("@header parse: %w", err)
		}
		if err := p.Check(ref); err != nil {
			return err
		}
	}
	return nil
}

// descriptor returns the canonical chameleon.v1.LayerHeader descriptor
// from the generated bindings.
//
// This used to compile an inline copy of the message at runtime via
// bufbuild/protocompile, mirroring what chain/load.go did at the time
// (f14776d, 2026-05-11) — the stated reason was to avoid a codegen step
// for chameleon's own protos. That reason expired one day later:
// ea17e9a migrated chain to protoc-gen-go, so the repo has a codegen
// step and proto/chameleon/v1 is generated regardless. internal/header
// was simply not migrated along with it. Since the resolve path already
// links proto/chameleon/v1 (it is in `go list -deps ./resolver` either
// way), reading the descriptor from the generated type costs a consumer
// nothing and drops protocompile — 13 packages, ~3 MB — from every
// binary that resolves plaintext layers. See #137.
//
// Descriptor equivalence with the old inline mirror is pinned by
// TestDescriptorMatchesGeneratedShape.
func descriptor() protoreflect.MessageDescriptor {
	return layerHeaderDesc
}

// layerHeaderDesc is resolved once, at init.
//
// Reading the generated descriptor is cheap but not free:
// `(&chameleonv1.LayerHeader{}).ProtoReflect()` heap-allocates a
// message on every call — escape analysis confirms it
// (`go build -gcflags=-m ./internal/header`) — and it would allocate
// one solely to reach a process-global descriptor. Parse is on the
// resolve path, called once per @header per layer, so this keeps the
// memoization the sync.Once above it used to provide, for free.
var layerHeaderDesc = (&chameleonv1.LayerHeader{}).ProtoReflect().Descriptor()

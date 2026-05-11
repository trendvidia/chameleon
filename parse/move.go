// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package parse contains chameleon's post-parse hooks. The headline
// entry point is [Move], which walks a parsed proto.Message produced
// by the PXF codec, finds every field whose message type is pxf.Secret,
// and moves the inner `value` bytes into a memguard-backed
// [secret.Secret].
//
// Move is idempotent and operates on stock proto.Message — it does not
// require the chameleon codegen plugin. (When the plugin lands and
// emits typed *secret.Secret accessors, Move remains the underlying
// primitive; the plugin layers ergonomics on top.)
package parse

import (
	"errors"
	"fmt"

	"github.com/trendvidia/chameleon/internal/pathfmt"
	"github.com/trendvidia/chameleon/secret"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// secretFullName is the canonical FullName of the pxf.Secret well-known
// type. Definitive descriptor lives in trendvidia/protowire @
// proto/pxf/secret.proto.
const secretFullName protoreflect.FullName = "pxf.Secret"

// ErrInvalidSecret is returned when a pxf.Secret-typed field has a
// descriptor that's missing the inner `value` field — i.e. a corrupted
// or non-canonical Secret schema. Tests against this error catch
// "loaded the wrong descriptor" mistakes early.
var ErrInvalidSecret = errors.New("parse: pxf.Secret descriptor missing `value` field")

// Move walks msg recursively. For every field whose message type is
// pxf.Secret it (1) reads the inner `value` string, (2) seals those
// bytes into a memguard Enclave wrapped in a [secret.Secret], (3)
// stores the Secret in the returned Map keyed by dotted field path,
// and (4) clears the `value` field on the proto message. The `hint`
// and `fingerprint` fields are preserved — they are diagnostic, not
// sensitive.
//
// Move recurses into nested messages, repeated message fields, and
// map<*, message> values. Scalars, enums, and map<*, scalar> fields
// are skipped.
//
// Best-effort caveat. Go strings are immutable; the plaintext that
// landed in pxf.Secret.value during PXF parse is allocated in the
// regular GC heap and cannot be reliably wiped by Move alone — only
// the post-Move memguard representation is mlocked + encrypted in
// process. Closing the pre-Move window requires a custom protoreflect
// implementation that routes parser bytes directly to the Enclave;
// see cmd/protoc-gen-chameleon-go.
//
// Move is idempotent: a second call on the same msg finds nothing,
// because the first call cleared every value field.
//
// A nil msg returns (nil, nil).
func Move(msg proto.Message) (*secret.Map, error) {
	return MoveWithOptions(msg)
}

// MoveWithOptions is [Move] with caller-supplied [secret.MapOption]s.
// Used by the resolver to wire per-Open observability
// ([secret.WithInstruments]) without changing [Move]'s simple
// signature.
func MoveWithOptions(msg proto.Message, opts ...secret.MapOption) (*secret.Map, error) {
	if msg == nil {
		return nil, nil
	}
	out := secret.NewMap(opts...)
	w := walker{secrets: out}
	if err := w.message(msg.ProtoReflect(), ""); err != nil {
		// Partial Map handed back to the caller; let them Destroy what
		// did get extracted before bubbling the error up.
		return out, err
	}
	return out, nil
}

type walker struct {
	secrets *secret.Map
}

func (w *walker) message(m protoreflect.Message, path string) error {
	if !m.IsValid() {
		return nil
	}
	var rangeErr error
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if err := w.field(m, fd, v, path); err != nil {
			rangeErr = err
			return false
		}
		return true
	})
	return rangeErr
}

func (w *walker) field(parent protoreflect.Message, fd protoreflect.FieldDescriptor, _ protoreflect.Value, path string) error {
	name := joinPath(path, string(fd.Name()))

	switch {
	case fd.IsMap():
		return w.mapField(parent, fd, name)
	case fd.IsList():
		return w.listField(parent, fd, name)
	case fd.Kind() == protoreflect.MessageKind:
		return w.messageField(parent, fd, name)
	default:
		return nil // scalar — uninteresting
	}
}

func (w *walker) messageField(parent protoreflect.Message, fd protoreflect.FieldDescriptor, path string) error {
	mdesc := fd.Message()
	if mdesc == nil {
		return nil
	}
	sub := parent.Mutable(fd).Message()
	if mdesc.FullName() == secretFullName {
		return w.takeSecret(sub, path)
	}
	return w.message(sub, path)
}

func (w *walker) listField(parent protoreflect.Message, fd protoreflect.FieldDescriptor, path string) error {
	if fd.Kind() != protoreflect.MessageKind {
		return nil
	}
	mdesc := fd.Message()
	if mdesc == nil {
		return nil
	}
	isSecret := mdesc.FullName() == secretFullName
	list := parent.Mutable(fd).List()
	for i, n := 0, list.Len(); i < n; i++ {
		elem := list.Get(i).Message()
		elemPath := fmt.Sprintf("%s[%d]", path, i)
		if isSecret {
			if err := w.takeSecret(elem, elemPath); err != nil {
				return err
			}
			continue
		}
		if err := w.message(elem, elemPath); err != nil {
			return err
		}
	}
	return nil
}

func (w *walker) mapField(parent protoreflect.Message, fd protoreflect.FieldDescriptor, path string) error {
	valFd := fd.MapValue()
	if valFd.Kind() != protoreflect.MessageKind {
		return nil
	}
	mdesc := valFd.Message()
	if mdesc == nil {
		return nil
	}
	isSecret := mdesc.FullName() == secretFullName
	m := parent.Mutable(fd).Map()
	var rangeErr error
	m.Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		keyPath := fmt.Sprintf("%s[%s]", path, pathfmt.MapKey(k))
		sub := v.Message()
		if isSecret {
			if err := w.takeSecret(sub, keyPath); err != nil {
				rangeErr = err
				return false
			}
			return true
		}
		if err := w.message(sub, keyPath); err != nil {
			rangeErr = err
			return false
		}
		return true
	})
	return rangeErr
}

func (w *walker) takeSecret(secMsg protoreflect.Message, path string) error {
	desc := secMsg.Descriptor()
	valueFd := desc.Fields().ByName("value")
	if valueFd == nil {
		return fmt.Errorf("%w: at path %q", ErrInvalidSecret, path)
	}
	plaintext := secMsg.Get(valueFd).String()
	if plaintext == "" {
		// Unset secret (or already-moved). Skip — don't insert empty
		// Secrets that would surprise consumers calling Get().
		return nil
	}
	var hint, fingerprint string
	if hf := desc.Fields().ByName("hint"); hf != nil {
		hint = secMsg.Get(hf).String()
	}
	if ff := desc.Fields().ByName("fingerprint"); ff != nil {
		fingerprint = secMsg.Get(ff).String()
	}
	w.secrets.Set(path, secret.FromString(plaintext, hint, fingerprint))
	secMsg.Clear(valueFd)
	return nil
}

func joinPath(parent, name string) string {
	if parent == "" {
		return name
	}
	return parent + "." + name
}


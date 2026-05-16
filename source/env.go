// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package source

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/trendvidia/protowire-go/encoding/pxf"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	durationpb "google.golang.org/protobuf/types/known/durationpb"
)

// EnvOption customizes an env [Source].
type EnvOption func(*envSource)

// WithPrefix sets a string that must precede every env var name the
// source consults. The remainder of the env name is matched against
// the dotted field path, uppercased with `.` → `_`. Example:
//
//	WithPrefix("STEWARD_") + field path "anthropic.api_key"
//	→ reads STEWARD_ANTHROPIC_API_KEY
//
// Empty prefix (default) matches the bare uppercase form.
func WithPrefix(prefix string) EnvOption {
	return func(s *envSource) { s.prefix = prefix }
}

// Env constructs a [Source] backed by process environment variables.
// At Open() time it walks `desc` once, derives an env-var name for
// each scalar leaf, reads each one, coerces to the field's typed
// value, populates a partial [dynamicpb.Message], and marshals the
// result as a PXF document. The resolver's existing parse+merge
// pipeline consumes the bytes like any other layer.
//
// Discovery rules:
//
//   - Descends into singular message fields recursively. Reaches all
//     scalar leaves.
//   - Skips repeated and map fields. Env vars are flat; structured
//     overrides should go through pxf files.
//   - Treats well-known leaves (pxf.Secret, google.protobuf.Duration,
//     google.protobuf.Timestamp) as scalar — populates them from a
//     single env var rather than descending into seconds/nanos /
//     value sub-fields.
//
// Coercion failures (e.g. "abc" → int32, malformed duration) are
// loud: [Source.Open] returns a non-[ErrNotFound] error so the
// resolver fails the chain rather than silently dropping the override.
// Unset env vars are silent — that's "no override," not an error.
//
// The `ref` argument to [Source.Open] is ignored; env doesn't need
// a path. The chain spec still requires a non-empty LayerEntry.path
// (chain/expand validation is path-shape-agnostic), so author the
// layer with any sentinel string — e.g. `path = "env"`. The source
// will never look at it.
func Env(desc protoreflect.MessageDescriptor, opts ...EnvOption) Source {
	s := &envSource{desc: desc}
	for _, o := range opts {
		o(s)
	}
	return s
}

type envSource struct {
	desc   protoreflect.MessageDescriptor
	prefix string
}

func (s *envSource) Open(ctx context.Context, _ string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	msg := dynamicpb.NewMessage(s.desc)
	if err := s.populate(msg.ProtoReflect(), nil); err != nil {
		return nil, fmt.Errorf("source/env: %w", err)
	}
	raw, err := pxf.MarshalOptions{}.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("source/env: marshal pxf: %w", err)
	}
	return io.NopCloser(bytes.NewReader(raw)), nil
}

// Stat reports the env source as always present. The resolver uses
// Stat to short-circuit optional missing layers; for env, the layer
// is conceptually always there even when no env var is set (the
// resulting PXF is just empty and merges as a no-op).
func (s *envSource) Stat(ctx context.Context, _ string) (Info, error) {
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	return Info{Exists: true, Size: -1}, nil
}

const (
	secretFullName    protoreflect.FullName = "pxf.Secret"
	durationFullName  protoreflect.FullName = "google.protobuf.Duration"
	timestampFullName protoreflect.FullName = "google.protobuf.Timestamp"
)

// populate walks m's fields and applies any matching env overrides.
// pathPrefix is the dotted field path of m's parent (empty at root).
func (s *envSource) populate(m protoreflect.Message, pathPrefix []string) error {
	fields := m.Descriptor().Fields()
	for i := range fields.Len() {
		fd := fields.Get(i)
		if fd.IsList() || fd.IsMap() {
			continue
		}

		path := append(append([]string{}, pathPrefix...), string(fd.Name()))

		if fd.Kind() == protoreflect.MessageKind {
			// Well-known leaf types: treated as scalars sourced from
			// a single env var, not recursed into.
			switch fd.Message().FullName() {
			case secretFullName, durationFullName, timestampFullName:
				envName := s.envNameFor(path)
				raw, ok := os.LookupEnv(envName)
				if !ok {
					continue
				}
				sub := m.Mutable(fd).Message()
				if err := coerceMessageLeaf(fd.Message().FullName(), sub, raw); err != nil {
					return fmt.Errorf("env=%q field=%q: %w", envName, strings.Join(path, "."), err)
				}
				continue
			}

			// Ordinary nested message — recurse into a fresh child;
			// attach only if any field got populated.
			sub := dynamicpb.NewMessage(fd.Message())
			if err := s.populate(sub.ProtoReflect(), path); err != nil {
				return err
			}
			if anyPopulated(sub.ProtoReflect()) {
				m.Set(fd, protoreflect.ValueOfMessage(sub.ProtoReflect()))
			}
			continue
		}

		envName := s.envNameFor(path)
		raw, ok := os.LookupEnv(envName)
		if !ok {
			continue
		}
		v, err := coerceScalar(fd, raw)
		if err != nil {
			return fmt.Errorf("env=%q field=%q: %w", envName, strings.Join(path, "."), err)
		}
		m.Set(fd, v)
	}
	return nil
}

func (s *envSource) envNameFor(path []string) string {
	return s.prefix + strings.ToUpper(strings.Join(path, "_"))
}

func anyPopulated(m protoreflect.Message) bool {
	set := false
	m.Range(func(_ protoreflect.FieldDescriptor, _ protoreflect.Value) bool {
		set = true
		return false
	})
	return set
}

// coerceMessageLeaf populates a well-known leaf message from a single
// raw env value. The dispatch covers pxf.Secret (set value field),
// google.protobuf.Duration (parse Go duration syntax), and
// google.protobuf.Timestamp (parse RFC 3339).
func coerceMessageLeaf(name protoreflect.FullName, m protoreflect.Message, raw string) error {
	switch name {
	case secretFullName:
		fd := m.Descriptor().Fields().ByName("value")
		if fd == nil {
			return fmt.Errorf("pxf.Secret descriptor missing value field")
		}
		m.Set(fd, protoreflect.ValueOfString(raw))
		return nil
	case durationFullName:
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("parse duration: %w", err)
		}
		dp := durationpb.New(d)
		secondsFd := m.Descriptor().Fields().ByName("seconds")
		nanosFd := m.Descriptor().Fields().ByName("nanos")
		if secondsFd == nil || nanosFd == nil {
			return fmt.Errorf("google.protobuf.Duration descriptor missing seconds/nanos")
		}
		m.Set(secondsFd, protoreflect.ValueOfInt64(dp.GetSeconds()))
		m.Set(nanosFd, protoreflect.ValueOfInt32(dp.GetNanos()))
		return nil
	case timestampFullName:
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			return fmt.Errorf("parse timestamp (RFC 3339): %w", err)
		}
		secondsFd := m.Descriptor().Fields().ByName("seconds")
		nanosFd := m.Descriptor().Fields().ByName("nanos")
		if secondsFd == nil || nanosFd == nil {
			return fmt.Errorf("google.protobuf.Timestamp descriptor missing seconds/nanos")
		}
		m.Set(secondsFd, protoreflect.ValueOfInt64(t.Unix()))
		m.Set(nanosFd, protoreflect.ValueOfInt32(int32(t.Nanosecond())))
		return nil
	}
	return fmt.Errorf("unknown well-known leaf %q", name)
}

func coerceScalar(fd protoreflect.FieldDescriptor, raw string) (protoreflect.Value, error) {
	switch fd.Kind() {
	case protoreflect.StringKind:
		return protoreflect.ValueOfString(raw), nil
	case protoreflect.BoolKind:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("parse bool: %w", err)
		}
		return protoreflect.ValueOfBool(b), nil
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("parse int32: %w", err)
		}
		return protoreflect.ValueOfInt32(int32(n)), nil
	case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("parse int64: %w", err)
		}
		return protoreflect.ValueOfInt64(n), nil
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
		n, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("parse uint32: %w", err)
		}
		return protoreflect.ValueOfUint32(uint32(n)), nil
	case protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("parse uint64: %w", err)
		}
		return protoreflect.ValueOfUint64(n), nil
	case protoreflect.FloatKind:
		f, err := strconv.ParseFloat(raw, 32)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("parse float32: %w", err)
		}
		return protoreflect.ValueOfFloat32(float32(f)), nil
	case protoreflect.DoubleKind:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("parse float64: %w", err)
		}
		return protoreflect.ValueOfFloat64(f), nil
	case protoreflect.BytesKind:
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return protoreflect.Value{}, fmt.Errorf("parse bytes (base64): %w", err)
		}
		return protoreflect.ValueOfBytes(b), nil
	}
	return protoreflect.Value{}, fmt.Errorf("unsupported field kind %s for env override", fd.Kind())
}

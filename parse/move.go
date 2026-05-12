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
	"github.com/trendvidia/protowire-go/encoding/pxf"
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

// MoveInto walks msg and folds its block-form pxf.Secret values into
// secrets — a caller-managed [secret.Map] already populated with
// hook-routed secrets via [pxf.UnmarshalOptions.OnSecretField]. Used
// by the chameleon resolver as a defensive walker that (a) catches
// any leftover block-form `value` bytes on the proto message
// (none in practice with protowire-go v0.77+ since the hook routes
// block form too), (b) folds hint/fingerprint metadata from the
// proto message onto hook-populated Map entries via
// [secret.Secret.SetMetadata], and (c) drops orphan entries whose
// path was nulled by a higher layer.
//
// MoveInto narrows the residency of plaintext on `*pxfv1.Secret.Value`
// to "zero" (with the hook wired). It does NOT close the transient
// Go heap window that the PXF lexer string + the hook closure's
// `value` parameter occupy from token construction through the next
// GC — those bytes live on an immutable Go string and cannot be
// wiped. Closing that requires either a byte-slice hook variant or
// a lexer-level Secret-aware path; both are protowire-go-side work
// tracked separately.
//
// Semantics:
//
//   - For every Secret-typed field reached by the walker, if its
//     inner `value` is non-empty, MoveInto seals it into a new
//     [secret.Secret] and inserts it into secrets (overwriting any
//     hook-routed entry at the same path; the merged message is
//     authoritative for the final value).
//
//   - For every Secret-typed field the walker reaches, the path is
//     recorded as "visited" regardless of value emptiness — a Secret
//     message that the hook routed (so value is empty) is still
//     visited, which marks its hook-routed entry as still valid in
//     the merged result.
//
//   - After the walk completes, MoveInto Deletes any entry in
//     secrets whose path was NOT visited. This is the orphan-cleanup
//     pass: when a higher layer nulls a Secret a lower layer set via
//     scalar shorthand, merge clears the Secret message from dst, the
//     walker never visits the path, and the hook entry would
//     otherwise outlive its null override. The cleanup ensures
//     consumers never see a secret the merged config says is gone.
//
// MoveInto is idempotent on a non-error second call: visited paths
// stay, orphans were already dropped on the first pass.
//
// A nil msg returns nil without touching secrets. A nil secrets
// returns an error — the caller must own a Map to fold into.
func MoveInto(msg proto.Message, secrets *secret.Map) error {
	return MoveIntoWithOptions(msg, secrets, MoveOptions{})
}

// MoveOptions configures [MoveIntoWithOptions]. The zero value is
// equivalent to calling [MoveInto] without options.
type MoveOptions struct {
	// LayerResults is the per-layer [pxf.Result] list in layer order
	// (lowest-priority layer first, terminal layer last). When set,
	// MoveIntoWithOptions distinguishes "hook-routed Secret value"
	// from "WKT atomic-merge wholesale replacement" by consulting
	// the most-recent layer that touched each Secret-typed path:
	//
	//   - If the most-recent layer marked `<path>.value` present,
	//     the hook routed the value into the Map. Keep the existing
	//     entry; this is the scalar-shorthand or block-form-with-
	//     value case.
	//
	//   - If the most-recent layer set the Secret field itself
	//     (e.g. `pw { hint = "h" }` or `pw { }`) but did NOT mark
	//     `<path>.value` present, the merge was a WKT atomic-merge
	//     wholesale replacement. Per the documented semantics —
	//     "higher layer replaces the Secret as a unit" — the prior
	//     hook entry is cleared via [secret.Map.Delete].
	//
	// Without LayerResults (the [MoveInto] zero-options shim), the
	// walker preserves prior hook entries when the merged Secret
	// has an empty value. This matches pre-v0.11 behavior and is
	// what consumers calling [MoveInto] directly should expect.
	LayerResults []*pxf.Result
}

// MoveIntoWithOptions is [MoveInto] with caller-supplied
// [MoveOptions]. Used by the chameleon resolver to thread per-layer
// [pxf.Result] data through to the walker, enabling the WKT
// atomic-merge clear semantics described on [MoveOptions.LayerResults].
//
// All [MoveInto] semantics carry over verbatim; this entry point
// is purely additive.
func MoveIntoWithOptions(msg proto.Message, secrets *secret.Map, opts MoveOptions) error {
	if msg == nil {
		return nil
	}
	if secrets == nil {
		return errors.New("parse: MoveInto requires a non-nil *secret.Map")
	}
	visited := make(map[string]struct{})
	w := walker{secrets: secrets, visited: visited, layerResults: opts.LayerResults}
	if err := w.message(msg.ProtoReflect(), ""); err != nil {
		return err
	}
	// Orphan cleanup: any hook-routed entry whose path the walker
	// didn't reach was cleared by a higher layer's null. Delete it
	// so consumers can't fetch a secret the merged config disowns.
	var toDrop []string
	secrets.Range(func(path string, _ *secret.Secret) bool {
		if _, ok := visited[path]; !ok {
			toDrop = append(toDrop, path)
		}
		return true
	})
	for _, p := range toDrop {
		secrets.Delete(p)
	}
	return nil
}

type walker struct {
	secrets *secret.Map
	// visited records every Secret-typed path the walker reaches,
	// even when the message has an empty inner `value` (the
	// hook-routed case). Used by [MoveInto] for orphan cleanup.
	// Nil for the legacy [Move] / [MoveWithOptions] entry points,
	// which have no hook-routed entries to reconcile.
	visited map[string]struct{}
	// layerResults is the per-layer [pxf.Result] list passed via
	// [MoveOptions.LayerResults]. Nil for legacy entry points and
	// for [MoveInto] (zero-options shim); non-nil only when the
	// caller wants WKT atomic-merge clearing semantics on
	// empty-value Secrets. See [lastWriterDidNotSetValue].
	layerResults []*pxf.Result
}

// lastWriterDidNotSetValue reports whether the most-recent layer
// that touched the Secret at path set the Secret field itself but
// NOT the `.value` subfield. That is the "WKT atomic-merge
// wholesale replacement" case: a layer wrote `pw { }` or
// `pw { hint = "h" }`, replacing any prior `pw` as a unit — and
// since the hook didn't fire (no value subfield), the prior
// hook-routed entry in the Map represents stale state.
//
// Returns false when:
//
//   - layerResults is nil (caller opted out of atomic-merge
//     clearing semantics);
//   - no layer touched path (shouldn't happen for a path the
//     walker is visiting — the path is on the merged dst, so SOME
//     layer must have set it);
//   - the most-recent toucher set `.value` (hook-routed; keep
//     entry).
//
// Returns true when the most-recent toucher set the Secret field
// itself but not its value subfield — the cleared case.
func (w *walker) lastWriterDidNotSetValue(path string) bool {
	if w.layerResults == nil {
		return false
	}
	for i := len(w.layerResults) - 1; i >= 0; i-- {
		r := w.layerResults[i]
		if r == nil {
			continue
		}
		// Did THIS layer touch any subfield of path? A Secret block
		// `pw { ... }` markPresents `pw` itself plus any inner
		// subfields it sets; the hook-routed value also calls
		// markInnerPresent for `pw.value`. So we ask: is the parent
		// `pw` set in this layer's result? If not, this layer didn't
		// write the Secret message itself, and we should look at
		// earlier layers.
		if !r.IsSet(path) {
			continue
		}
		// This layer set the Secret field. Did it also set value?
		// If yes → hook-routed, keep. If no → atomic-merge clear.
		return !r.IsSet(path + ".value")
	}
	return false
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
	// Always mark the path visited — even an empty Secret message
	// (the hook-routed case) is "still here" from the merged
	// message's perspective, which keeps its hook-populated entry
	// from being dropped by [MoveInto]'s orphan sweep.
	if w.visited != nil {
		w.visited[path] = struct{}{}
	}
	plaintext := secMsg.Get(valueFd).String()
	var hint, fingerprint string
	if hf := desc.Fields().ByName("hint"); hf != nil {
		hint = secMsg.Get(hf).String()
	}
	if ff := desc.Fields().ByName("fingerprint"); ff != nil {
		fingerprint = secMsg.Get(ff).String()
	}
	if plaintext == "" {
		// Empty inner `value`. Three sub-cases:
		//
		//   1) Hook-routed value (scalar shorthand, or block-form
		//      with `value`): the most-recent layer touching path
		//      marked `<path>.value` present, and the hook took the
		//      bytes. Existing Map entry stays; if the same layer
		//      also supplied hint/fingerprint, fold them in.
		//
		//   2) WKT atomic-merge wholesale replacement: the most-
		//      recent layer wrote `pw { hint = "h" }` or `pw { }` —
		//      the Secret was set as a unit but `value` was not.
		//      Per the documented "higher layer replaces the Secret
		//      as a unit" semantic, drop the prior hook entry.
		//      Detected via [MoveOptions.LayerResults]; without
		//      that, fall through to case (1)'s behavior.
		//
		//   3) Genuinely-unset Secret (never written). No
		//      Map entry to touch; no-op.
		if w.lastWriterDidNotSetValue(path) {
			if w.secrets != nil {
				w.secrets.Delete(path)
			}
			return nil
		}
		// Skip-when-both-empty is intentional: a layer that doesn't
		// supply hint/fingerprint must NOT clobber whatever a lower
		// layer (or an earlier hook call at the same path) already
		// fed onto the Map's Secret. SetMetadata itself overwrites
		// unconditionally — see its godoc — so the empty-check
		// happens here at the call site.
		if (hint != "" || fingerprint != "") && w.secrets != nil {
			if existing := w.secrets.Get(path); existing != nil {
				existing.SetMetadata(hint, fingerprint)
			}
		}
		return nil
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

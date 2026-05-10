// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

// Package upstream is not built-imported by chameleon. It exists as a
// permanent record of the cross-repo contract between chameleon and
// github.com/trendvidia/protowire-go: chameleon's PXF integration
// requires that protowire-go recognize pxf.Secret as a
// well-known type. If the codec in protowire-go is ever refactored,
// THESE INVARIANTS MUST BE PRESERVED — otherwise chameleon's secret
// shorthand, round-trip, and resolver pipeline silently break.
//
// CONTRACT (currently implemented in protowire-go @ encoding/pxf)
//
// 1) wellknown.go exposes (package-private):
//
//      isSecret(d) bool                         // FullName() == "pxf.Secret"
//      secretHasMetadata(msg) bool              // hint != "" || fingerprint != ""
//
//    Secret is treated as a value-shaped well-known type but NOT added
//    to the wrapperTypes map: it carries optional hint/fingerprint
//    metadata that pure wrappers don't, so the encode path needs the
//    secretHasMetadata branch.
//
//    Deliberate asymmetry vs. pxf.BigInt / Decimal / BigFloat: those
//    WKTs need setSecretValue/readSecretValue-style helpers because
//    their inner representation is multi-field (abs + negative bytes,
//    etc.). pxf.Secret.value is a plain string — the codec's existing
//    scalar paths (consumeScalar / fastSet on decode, writeScalar on
//    encode) handle it directly. Adding wrapper helpers that route
//    through them would either bypass UTF-8 validation or duplicate
//    logic, AND lose fastSet's fast-path optimization. So the helpers
//    don't exist; the codec talks to mdesc.Fields().ByName("value")
//    inline at every Secret call site.
//
// 1b) decode_fast.go exposes (package-private):
//
//      directDecoder.markInnerPresent(fd, names ...string)
//
//    Helper used by every WKT scalar-shorthand decoder in
//    decodeMsgValue to mark the inner fields present after setting
//    them. Without this, scalar-shorthand parsing produced a Result
//    where the parent path was marked present but inner paths were
//    not — block-form parsing always marked both. Chameleon's merge
//    layer treats WKTs as atomic regardless of presence (it's the
//    correct value-type semantic), but the inconsistent Result was
//    a real bug for any consumer doing presence-aware analysis on
//    WKT inner fields.
//
// 2) decode_fast.go: scalar shorthand is accepted in three contexts.
//
//    decodeMsgValue (top-level message field):
//      after the BigFloat branch, before the "expected '{'" error —
//      if isSecret(mdesc) && d.current.Kind == STRING, consume the
//      scalar into the inner `value` field. Block form falls through
//      to the generic decoder, which handles `pw { value=... hint=... }`
//      via the underlying descriptor.
//
//    consumeListMsg (repeated message element):
//      same insertion point; lets `repeated pxf.Secret` accept
//      a plain string entry in a list literal alongside block entries.
//
//    decodeMapInline (map<*, message> value position):
//      mirrors the same WKT-aware shortcut block as the other two,
//      with `m.NewValue().Message()` as the sub-message factory and
//      `fastMapSet` as the result setter. Handles every value-shaped
//      WKT — Timestamp / Duration / wrappers / BigInt / Decimal /
//      BigFloat / Secret — not just Secret. Each branch ends with
//      `continue` because we're inside the for-loop iterating map
//      entries.
//
// 3) encode.go: round-trip preserves authoring metadata.
//
//    encodeMessageField:
//      if isSecret(mdesc) && !secretHasMetadata(sub), emit scalar
//      shorthand (`pw = "x"`). Otherwise fall through to the generic
//      block encoder. This is load-bearing — emitting scalar form when
//      hint or fingerprint is set would silently drop them on re-emit.
//
//    encodeListField:
//      same branch in the if/else chain alongside the other
//      well-known scalar shortcut emitters.
//
//    encodeMapField:
//      mirrors the same WKT scalar emission for map values, so
//      `tenants = { "acme": "x" }` round-trips as scalar shorthand.
//      Secret falls back to block form when secretHasMetadata(sub) is
//      true. Each branch ends with `continue` to advance the entries
//      loop without falling through.
//
// SECRET .PROTO PLACEMENT
//
// The canonical pxf.Secret descriptor lives in the protowire spec at
// proto/pxf/secret.proto, alongside pxf.BigInt / pxf.Decimal /
// pxf.BigFloat. It is NOT duplicated in chameleon — chameleon
// consumers import the spec's secret.proto from their own .proto files
// just like they would for any other protowire-family WKT.
//
// Protowire-go itself does not import the descriptor at compile time.
// The codec matches on FullName() ("pxf.Secret") at runtime; the
// descriptor reaches it via the consumer's compiled schema. Tests
// compile a synthetic copy inline via bufbuild/protocompile so the
// PXF test suite has no dependency on the protowire spec or chameleon.
//
// MEMORY-PROTECTION BOUNDARY
//
// protowire-go intentionally has zero knowledge of memguard or any
// other memory-protection library. The codec routes bytes between PXF
// and the proto message; chameleon's resolver is responsible for
// moving Secret.value bytes into a memguard.Enclave (and out again on
// Open). Keeping that boundary clean lets protowire-go stay a pure
// serialization library and lets sibling-language ports adopt
// pxf.Secret with whatever memory-safety primitive is idiomatic for
// their language.
//
// TEST COVERAGE (encoding/pxf/secret_test.go)
//
//   TestSecretScalarShorthand                 decode `pw = "x"`
//   TestSecretBlockForm                       decode block w/ hint+fp
//   TestSecretMixedFormsCoexist               two fields, mixed forms
//   TestSecretRepeatedScalarShorthand         list of plain strings
//   TestSecretRepeatedMixedForms              list mixing shorthand + block
//   TestSecretEncodeScalarWhenNoMetadata      empty meta -> scalar
//   TestSecretEncodeBlockWhenHintSet          hint -> block
//   TestSecretEncodeBlockWhenFingerprintSet   fp -> block
//   TestSecretRepeatedEncodeScalarWhenNoMetadata  list of empty-meta
//   TestNonSecretMessageRejectsScalar         regression: vanilla
//                                             message fields still
//                                             reject bare strings
//
// VERSIONING
//
// The change adds a new well-known type — semver-minor in protowire-go.
// Chameleon's go.mod must require the bumped minor as the floor.
package upstream

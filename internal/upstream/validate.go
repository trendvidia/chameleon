// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package upstream

// ValidateFile and ValidateDescriptor are the one question chameleon
// asks of a schema before it binds: everything [pxf.ValidateFile]
// reports, plus the rule that the protowire-go chameleon pins does not
// check yet.
//
// That rule is draft -01 §annotation-extensions, "Required and Default":
// pxf.required and pxf.default must not both be set on one field. One
// says an absent field is an error and the other says it takes a value,
// so a document omitting the field has no single meaning. chameleon used
// to read the pair as "the default satisfies the requirement", because
// its defaults pass runs before its required pass, while protowire-go
// checks required first, so one schema meant two things
// (trendvidia/chameleon#216).
//
// protowire-go#204 adds the rule to [pxf.ValidateFile]. Once chameleon
// pins a release carrying it, this file goes away and its callers go
// back to calling pxf directly. Until then both may run: a pair upstream
// already reported is not reported twice.
//
// It lives here rather than in a package of its own because cmd/chameleon
// names the library by its released tag, and `go mod tidy` there fails on
// a package that tag does not contain. This one it does.

import (
	"cmp"
	"slices"
	"strings"

	"github.com/trendvidia/protowire-go/encoding/pxf"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// exclusive is the Detail protowire-go#204 words this violation with,
// kept identical so operators read one message before and after the
// bump. Its first half is also how an upstream report is recognized.
const (
	exclusive = "are exclusive on one field"
	detail    = "(pxf.required) and (pxf.default) " + exclusive +
		": the first rejects a document where the field is absent, and the second treats it as supplied"
)

// ValidateDescriptor is [ValidateFile] on the file declaring desc,
// mirroring [pxf.ValidateDescriptor]. The scope is that file's whole
// import closure, not desc alone.
func ValidateDescriptor(desc protoreflect.MessageDescriptor) []pxf.Violation {
	if desc == nil {
		return nil
	}
	return ValidateFile(desc.ParentFile())
}

// ValidateFile returns every bind-time violation in fd's transitive
// import closure, sorted by declaring file and then by element, as
// [pxf.ValidateFile] sorts its own. An empty result means the schema may
// bind.
//
// The result names the bracket spelling even for a field written
// @required or @default(v): rendering the spelling the author used is
// upstream's unexported surface field. Both spellings are checked; only
// the wording of an annotation-spelled report is off, and only until
// this file goes away.
func ValidateFile(fd protoreflect.FileDescriptor) []pxf.Violation {
	if fd == nil {
		return nil
	}
	out := pxf.ValidateFile(fd)
	seen := map[string]struct{}{}
	reported := make(map[string]struct{}, len(out))
	for _, v := range out {
		if v.Kind == pxf.ViolationRequiredOption && strings.Contains(v.Detail, exclusive) {
			reported[v.Element] = struct{}{}
		}
	}
	added := false
	var walkFile func(protoreflect.FileDescriptor)
	walkFile = func(f protoreflect.FileDescriptor) {
		// A placeholder stands in for an import that did not resolve: it
		// declares nothing and imports nothing. Deduplicated by path, so
		// the diamond reports a shared file once, the way upstream does.
		if f == nil || f.IsPlaceholder() {
			return
		}
		if _, dup := seen[f.Path()]; dup {
			return
		}
		seen[f.Path()] = struct{}{}
		walkMessages(f.Path(), f.Messages(), func(v pxf.Violation) {
			if _, dup := reported[v.Element]; !dup {
				out = append(out, v)
				added = true
			}
		})
		imports := f.Imports()
		for i, n := 0, imports.Len(); i < n; i++ {
			walkFile(imports.Get(i).FileDescriptor)
		}
	}
	walkFile(fd)
	if added {
		slices.SortStableFunc(out, func(a, b pxf.Violation) int {
			return cmp.Or(cmp.Compare(a.File, b.File), cmp.Compare(a.Element, b.Element))
		})
	}
	return out
}

func walkMessages(path string, msgs protoreflect.MessageDescriptors, report func(pxf.Violation)) {
	for i, n := 0, msgs.Len(); i < n; i++ {
		md := msgs.Get(i)
		fields := md.Fields()
		for j, m := 0, fields.Len(); j < m; j++ {
			f := fields.Get(j)
			// Both accessors read the bracket option and the @ annotation.
			// IsRequired is false for (pxf.required) = false, which demands
			// nothing and so does not conflict with a default.
			if _, hasDefault := pxf.Default(f); hasDefault && pxf.IsRequired(f) {
				report(pxf.Violation{
					File:    path,
					Element: string(f.FullName()),
					Kind:    pxf.ViolationRequiredOption,
					Detail:  detail,
				})
			}
		}
		walkMessages(path, md.Messages(), report)
	}
}

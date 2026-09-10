// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package main

import (
	"github.com/trendvidia/protocompile/linker"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// findVisibleDescriptor looks name up in everything f can see: f itself,
// its direct imports, and their transitive *public* imports — the set a
// name written inside f could denote. Returns protoregistry.NotFound
// when nothing in that set declares it.
//
// This is the visibility rule upstream protocompile's
// linker.ResolverFromFile implemented. The trendvidia fork carries no
// per-file resolver (trendvidia/protocompile#222): linker.Files.AsResolver
// searches only the files named to the compiler, so a message declared
// in an imported common.proto would come back not-found from a closure
// that plainly contains it.
func findVisibleDescriptor(f linker.File, name protoreflect.FullName) (protoreflect.Descriptor, error) {
	if d := resolveVisible(f, name, false, map[string]bool{}); d != nil {
		return d, nil
	}
	return nil, protoregistry.NotFound
}

// resolveVisible is the walk. publicOnly is false for the root file,
// whose every import is visible, and true below it, where only public
// imports re-export. seen prunes a file reached by two paths; a file
// that did not declare name the first time will not the second.
func resolveVisible(f linker.File, name protoreflect.FullName, publicOnly bool, seen map[string]bool) protoreflect.Descriptor {
	if f == nil || seen[f.Path()] {
		return nil
	}
	seen[f.Path()] = true
	if d := f.FindDescriptorByName(name); d != nil {
		return d
	}
	imports := f.Imports()
	for i := range imports.Len() {
		imp := imports.Get(i)
		if publicOnly && !imp.IsPublic {
			continue
		}
		if d := resolveVisible(f.FindImportByPath(imp.Path()), name, true, seen); d != nil {
			return d
		}
	}
	return nil
}

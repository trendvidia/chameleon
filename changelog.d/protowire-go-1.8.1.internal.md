- **protowire-go v1.8.1 and protocompile v0.34.0; the pins written against
  the fork's v0.33.1 gaps flipped and are gone.** The three CLI pins for
  trendvidia/protocompile#222/#223/#224 and the `defaults` pin for
  protowire-go#135 each failed on the bump exactly as their comments said
  they would; the upstream-style assertions are restored, and the local
  `findVisibleDescriptor` walk is deleted in favour of the restored
  `linker.ResolverFromFile`.

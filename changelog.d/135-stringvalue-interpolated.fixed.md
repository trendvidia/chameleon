- **A `(pxf.default)` on a `google.protobuf.StringValue` is now template-expanded, like one on a plain `string`** ([#135](https://github.com/trendvidia/chameleon/issues/135)). The defaults pass runs before interpolation specifically so a default may itself be a template — `"api.${base_url}"` — and since [#129](https://github.com/trendvidia/chameleon/issues/129) a default can land on an atomic WKT. Interpolation stopped at the atomic boundary, so the two features did not compose: the raw `${...}` reached the resolved config verbatim, with no error and no warning.

  ```proto
  string                      base  = 1 [(pxf.default) = "example.com"];
  string                      api_s = 2 [(pxf.default) = "api.${base}"];
  google.protobuf.StringValue api_w = 3 [(pxf.default) = "api.${base}"];
  ```

  `api_s` resolved to `api.example.com` and `api_w.value` to the literal `api.${base}`. Both now resolve to `api.example.com`.

  **The boundary this crosses is narrower than it looks.** `wkt.Atomic` is unchanged, and merge, defaults and validate still treat every WKT as one whole value. Only interpolate's *write* walk descends, and only into `StringValue` — the new `wkt.TemplatableText` predicate, which is exactly one type wide and pinned that way per type. Interpolate's *lookup* walk is untouched: `${some_wrapper.value}` remains an `ErrInvalidReference`, as it does for a `pxf.Secret`. Writing into a WKT leaks nothing; reading out of one is the vector that boundary exists for, and it stays shut.

  The rewrite is reported to `OnApplied` — and so to `resolver.Trace` and `chameleon explain` — at the WKT field's own path rather than an inner `.value`, matching how every other pass keys a WKT.

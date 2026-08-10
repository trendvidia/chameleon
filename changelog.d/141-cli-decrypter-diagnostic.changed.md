- **`chameleon get/explain/reveal` say what to do when a layer is encrypted and `--decrypter` is `"none"`** ([#141](https://github.com/trendvidia/chameleon/issues/141)). The refusal is unchanged and still correct — `"none"` is the fail-closed default, and it is what stops the CLI reaching for ambient KMS/age credentials just because the same binary also ships `chameleon bundle`. What you got told about it was the problem:

  ```
  chameleon get: resolver: encrypted layer without Decrypter: layer="base": decryption disabled (WithoutDecrypter)
  ```

  `WithoutDecrypter` is a Go option from `chameleon/resolver`. Someone running the binary never called it, disabled nothing knowingly, and finds it in no `--help` output. Now:

  ```
  chameleon get: layer "base" is encrypted: --decrypter is "none" (the fail-closed default); pass --decrypter=sops to decrypt it, or set decrypter = "sops" on this app in /home/you/.chameleon
  ```

  and, when the flag was actually typed, the statement without the instruction — being refused is the flag doing its job:

  ```
  chameleon get: layer "base" is encrypted: --decrypter=none was passed on the command line
  ```

  That second message **echoes the value you passed** rather than naming `"none"`. `--decrypter=` — what `--decrypter=$DECRYPTER` expands to with the variable unset — disables decryption exactly as `"none"` does, so it reports `--decrypter= was passed on the command line`. Seeing the empty value is also the answer to "why was the `decrypter` in my `~/.chameleon` ignored": a typed flag outranks the config file, empty value included.

  The `~/.chameleon` path in the first message is the one the loader would really read, `CHAMELEON_USER_CONFIG` included.

  **If you match on stderr.** Scripts keying on `decryption disabled` or `WithoutDecrypter` will stop matching; both CLI phrasings share the substring `is encrypted: --decrypter`. The Go-facing wording from `resolver` is untouched for programs using the library directly.

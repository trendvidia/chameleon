- **`chameleon get/explain/reveal` say what to do when a layer is encrypted and `--decrypter` is `"none"`** ([#141](https://github.com/trendvidia/chameleon/issues/141)). The refusal is unchanged and still correct — `"none"` is the fail-closed default, and it is what stops the CLI reaching for ambient KMS/age credentials just because the same binary also ships `chameleon bundle`. What you got told about it was the problem:

  ```
  chameleon get: resolver: encrypted layer without Decrypter: layer="base": decryption disabled (WithoutDecrypter)
  ```

  `WithoutDecrypter` is a Go option from `chameleon/resolver`. Someone running the binary never called it, disabled nothing knowingly, and finds it in no `--help` output. Now:

  ```
  chameleon get: layer "base" is encrypted: --decrypter is "none" (the fail-closed default); pass --decrypter=sops to decrypt it, or set decrypter = "sops" on this app in /home/you/.chameleon
  ```

  and, when `--decrypter=none` was actually typed, the statement without the instruction — being refused is the flag doing its job:

  ```
  chameleon get: layer "base" is encrypted: --decrypter=none was passed on the command line
  ```

  The `~/.chameleon` path in the first message is the one the loader would really read, `CHAMELEON_USER_CONFIG` included.

  **If you match on stderr.** Scripts keying on `decryption disabled` or `WithoutDecrypter` will stop matching; both CLI phrasings share the substring `is encrypted: --decrypter`. The Go-facing wording from `resolver` is untouched for programs using the library directly.

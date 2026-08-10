// SPDX-License-Identifier: MIT
// Copyright (c) 2026 TrendVidia, LLC.

package bundler

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/awnumar/memguard"

	"github.com/trendvidia/chameleon/crypt"
)

// fakeCrypto stands in for a decryption backend in the bundler's own
// tests. Since v0.23.0 the library module does not link one — reaching
// for chameleon/sops here is what put the whole KMS tree in every
// consumer's module graph (trendvidia/chameleon#130) — and the
// pipeline only ever sees the crypt interfaces, so a fake exercises
// exactly the same code path.
//
// "Ciphertext" is the plaintext behind a marker prefix. That is enough
// to prove the decrypt → narrow → re-encrypt sequence ran in order and
// on the right bytes, and it lets a test assert which recipients each
// layer was encrypted to, and under which crypt.Format — neither of
// which the real-age version of this test could check, because sops's
// envelope hides the recipients behind a key exchange and swallows the
// format hint. The real round-trip against sops is covered in the sops
// module (sops/bundle_test.go).
//
// The wipe contract is honored deliberately: both halves scribble over
// their input on success, exactly as chameleon/sops does, so a
// pipeline that reuses a buffer after handing it over fails here
// rather than in production.
type fakeCrypto struct {
	mu sync.Mutex
	// encrypted records what each Encrypt call was given, in call
	// order; decryptFormats does the same for Decrypt. Both halves
	// record the crypt.Format so a regression that hardcodes one
	// (instead of deriving it from the layer path) is visible here —
	// the real-age version of this test could not see it either, it
	// just happened to fail on the round trip.
	encrypted      []encryptCall
	decryptFormats []crypt.Format
}

// encryptCall is one Encrypt invocation as the fake saw it.
type encryptCall struct {
	Recipients crypt.Recipients
	Format     crypt.Format
}

const fakeCipherMarker = "FAKE-CIPHERTEXT:"

func (f *fakeCrypto) Encrypt(_ context.Context, plaintext []byte, format crypt.Format, r crypt.Recipients) ([]byte, error) {
	if r.IsEmpty() {
		return nil, fmt.Errorf("fakeCrypto: no recipients")
	}
	out := append([]byte(fakeCipherMarker), plaintext...)

	f.mu.Lock()
	f.encrypted = append(f.encrypted, encryptCall{Recipients: r, Format: format})
	f.mu.Unlock()

	memguard.WipeBytes(plaintext)
	return out, nil
}

func (f *fakeCrypto) Decrypt(_ context.Context, ciphertext []byte, format crypt.Format) (*memguard.LockedBuffer, error) {
	marker := []byte(fakeCipherMarker)
	if !bytes.HasPrefix(ciphertext, marker) {
		// Deliberately no excerpt of the input: a layer mis-marked
		// `encrypted = true` arrives here as plaintext, and the
		// fixtures in this package are secret-shaped on purpose. The
		// caller (processLayer) names the layer in its wrapping.
		return nil, fmt.Errorf("fakeCrypto: not fake ciphertext (%d bytes)", len(ciphertext))
	}
	buf := memguard.NewBufferFromBytes(ciphertext[len(marker):])

	f.mu.Lock()
	f.decryptFormats = append(f.decryptFormats, format)
	f.mu.Unlock()

	memguard.WipeBytes(ciphertext)
	return buf, nil
}

// recipientsSeen returns the recipient sets passed to Encrypt so far.
func (f *fakeCrypto) recipientsSeen() []crypt.Recipients {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]crypt.Recipients, 0, len(f.encrypted))
	for _, c := range f.encrypted {
		out = append(out, c.Recipients)
	}
	return out
}

// formatsSeen returns the crypt.Formats passed to Encrypt and to
// Decrypt so far, in call order.
func (f *fakeCrypto) formatsSeen() (encrypt, decrypt []crypt.Format) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.encrypted {
		encrypt = append(encrypt, c.Format)
	}
	return encrypt, append([]crypt.Format(nil), f.decryptFormats...)
}

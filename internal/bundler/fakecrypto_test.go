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
// layer was encrypted to — something the real-age version of this test
// could not check, because sops's envelope hides them behind a key
// exchange. The real round-trip against sops is covered in the sops
// module (sops/bundle_test.go).
//
// The wipe contract is honored deliberately: both halves scribble over
// their input on success, exactly as chameleon/sops does, so a
// pipeline that reuses a buffer after handing it over fails here
// rather than in production.
type fakeCrypto struct {
	mu sync.Mutex
	// encrypted records the recipients each Encrypt call was given,
	// in call order.
	encrypted []crypt.Recipients
}

const fakeCipherMarker = "FAKE-CIPHERTEXT:"

func (f *fakeCrypto) Encrypt(_ context.Context, plaintext []byte, _ crypt.Format, r crypt.Recipients) ([]byte, error) {
	if r.IsEmpty() {
		return nil, fmt.Errorf("fakeCrypto: no recipients")
	}
	out := append([]byte(fakeCipherMarker), plaintext...)

	f.mu.Lock()
	f.encrypted = append(f.encrypted, r)
	f.mu.Unlock()

	memguard.WipeBytes(plaintext)
	return out, nil
}

func (f *fakeCrypto) Decrypt(_ context.Context, ciphertext []byte, _ crypt.Format) (*memguard.LockedBuffer, error) {
	marker := []byte(fakeCipherMarker)
	if !bytes.HasPrefix(ciphertext, marker) {
		return nil, fmt.Errorf("fakeCrypto: not fake ciphertext (%.20q…)", ciphertext)
	}
	buf := memguard.NewBufferFromBytes(ciphertext[len(marker):])

	memguard.WipeBytes(ciphertext)
	return buf, nil
}

// recipientsSeen returns the recipient sets passed to Encrypt so far.
func (f *fakeCrypto) recipientsSeen() []crypt.Recipients {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]crypt.Recipients(nil), f.encrypted...)
}

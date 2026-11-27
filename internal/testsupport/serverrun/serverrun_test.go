// SPDX-License-Identifier: Apache-2.0

package serverrun_test

import (
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/chan-labs/quizzotic/internal/config"
	"github.com/chan-labs/quizzotic/internal/testsupport/serverrun"
)

// fatalTB records a Fatalf instead of failing the real test.
type fatalTB struct {
	testing.TB
	msg string
}

func (f *fatalTB) Helper() {}

func (f *fatalTB) Fatalf(format string, args ...any) {
	f.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func (f *fatalTB) Fatal(args ...any) {
	f.msg = fmt.Sprint(args...)
	runtime.Goexit()
}

// A config server.Run rejects makes Start fail at once instead of hanging on
// a listener nobody serves.
func TestStartFailsWhenRunReturnsEarly(t *testing.T) {
	cfg := config.Default() // no public_url, no issuer: Run returns an error
	tb := &fatalTB{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		serverrun.Start(tb, &cfg)
		t.Error("Start returned")
	}()
	<-done
	if !strings.Contains(tb.msg, "server.Run returned before serving") {
		t.Fatalf("Fatalf message = %q", tb.msg)
	}
}

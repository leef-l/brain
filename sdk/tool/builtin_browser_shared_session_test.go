package tool

import (
	"testing"

	"github.com/leef-l/brain/sdk/tool/cdp"
)

func TestCurrentSharedBrowserSessionPredictableWithoutCreation(t *testing.T) {
	sharedBrowserSessionMu.Lock()
	prevOwner := sharedBrowserSessionOwner
	prevAccessor := sharedBrowserSessionAccessor
	sharedBrowserSessionOwner = nil
	sharedBrowserSessionAccessor = nil
	sharedBrowserSessionMu.Unlock()
	t.Cleanup(func() {
		sharedBrowserSessionMu.Lock()
		sharedBrowserSessionOwner = prevOwner
		sharedBrowserSessionAccessor = prevAccessor
		sharedBrowserSessionMu.Unlock()
	})

	if sess, ok := CurrentSharedBrowserSession(); ok || sess != nil {
		t.Fatalf("CurrentSharedBrowserSession() = (%v, %v), want (nil, false)", sess, ok)
	}

	holder := newBrowserSessionHolder()
	registerSharedBrowserSessionAccessor(holder)
	t.Cleanup(func() { unregisterSharedBrowserSessionAccessor(holder) })

	if sess, ok := CurrentSharedBrowserSession(); ok || sess != nil {
		t.Fatalf("CurrentSharedBrowserSession() before creation = (%v, %v), want (nil, false)", sess, ok)
	}

	created := &cdp.BrowserSession{}
	holder.session = created

	got, ok := CurrentSharedBrowserSession()
	if !ok {
		t.Fatal("CurrentSharedBrowserSession() ok = false, want true after session creation")
	}
	if got != created {
		t.Fatalf("CurrentSharedBrowserSession() = %p, want %p", got, created)
	}
}

func TestNewBrowserToolsRegistersSharedSessionAccessor(t *testing.T) {
	sharedBrowserSessionMu.Lock()
	prevOwner := sharedBrowserSessionOwner
	prevAccessor := sharedBrowserSessionAccessor
	sharedBrowserSessionOwner = nil
	sharedBrowserSessionAccessor = nil
	sharedBrowserSessionMu.Unlock()
	t.Cleanup(func() {
		sharedBrowserSessionMu.Lock()
		sharedBrowserSessionOwner = prevOwner
		sharedBrowserSessionAccessor = prevAccessor
		sharedBrowserSessionMu.Unlock()
	})

	tools := NewBrowserTools()
	if len(tools) == 0 {
		t.Fatal("NewBrowserTools() returned no tools")
	}
	if sharedBrowserSessionAccessor == nil {
		t.Fatal("NewBrowserTools() did not register shared browser session accessor")
	}
	if sess, ok := CurrentSharedBrowserSession(); ok || sess != nil {
		t.Fatalf("CurrentSharedBrowserSession() after NewBrowserTools() = (%v, %v), want (nil, false) before first use", sess, ok)
	}

	CloseBrowserSession(tools)
	if sess, ok := CurrentSharedBrowserSession(); ok || sess != nil {
		t.Fatalf("CurrentSharedBrowserSession() after CloseBrowserSession() = (%v, %v), want (nil, false)", sess, ok)
	}
}

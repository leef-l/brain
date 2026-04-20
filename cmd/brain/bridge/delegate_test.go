package bridge

import (
	"context"
	"testing"

	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/protocol"
)

func TestBuildSubtaskContext_UsesCallerUtteranceAndRenderMode(t *testing.T) {
	ctx := kernel.WithSubtaskContext(context.Background(), &protocol.SubtaskContext{
		UserUtterance: "我要能看到你的操作，打开浏览器登录后台",
		ParentRunID:   "run-1",
		TurnIndex:     3,
	})

	got := buildSubtaskContext(ctx, "headed")
	if got == nil {
		t.Fatal("buildSubtaskContext() = nil")
	}
	if got.UserUtterance != "我要能看到你的操作，打开浏览器登录后台" {
		t.Fatalf("user_utterance = %q", got.UserUtterance)
	}
	if got.RenderMode != "headed" {
		t.Fatalf("render_mode = %q, want headed", got.RenderMode)
	}
	if got.ParentRunID != "run-1" || got.TurnIndex != 3 {
		t.Fatalf("unexpected passthrough metadata: %+v", got)
	}
}

func TestBuildSubtaskContext_ReturnsNilWhenEmpty(t *testing.T) {
	if got := buildSubtaskContext(context.Background(), ""); got != nil {
		t.Fatalf("buildSubtaskContext() = %+v, want nil", got)
	}
}

func TestResolveBrowserRenderMode_PrefersExplicitMode(t *testing.T) {
	got := resolveBrowserRenderMode(context.Background(), "browser", "我要看到浏览器", "headless")
	if got != "headless" {
		t.Fatalf("resolveBrowserRenderMode() = %q, want headless", got)
	}
}

func TestResolveBrowserRenderMode_InfersHeadedFromInstruction(t *testing.T) {
	got := resolveBrowserRenderMode(context.Background(), "browser", "打开浏览器，我要看到你的操作", "")
	if got != "headed" {
		t.Fatalf("resolveBrowserRenderMode() = %q, want headed", got)
	}
}

func TestResolveBrowserRenderMode_InfersHeadedFromUserUtterance(t *testing.T) {
	ctx := kernel.WithSubtaskContext(context.Background(), &protocol.SubtaskContext{
		UserUtterance: "我需要看到浏览器窗口",
	})
	got := resolveBrowserRenderMode(ctx, "browser", "登录后台", "")
	if got != "headed" {
		t.Fatalf("resolveBrowserRenderMode() = %q, want headed", got)
	}
}

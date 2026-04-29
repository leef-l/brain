package agent

import (
	"testing"
)

func TestBuiltinKinds(t *testing.T) {
	kinds := BuiltinKinds()
	if len(kinds) != 8 {
		t.Fatalf("expected 8 builtin kinds, got %d", len(kinds))
	}

	expected := map[Kind]bool{
		KindCode:     true,
		KindBrowser:  true,
		KindVerifier: true,
		KindFault:    true,
		KindData:     true,
		KindQuant:    true,
		KindDesktop:  true,
		KindEasyMVP:  true,
	}

	for _, k := range kinds {
		if !expected[k] {
			t.Fatalf("unexpected kind %s in BuiltinKinds", k)
		}
		delete(expected, k)
	}
	if len(expected) != 0 {
		for k := range expected {
			t.Fatalf("missing kind %s in BuiltinKinds", k)
		}
	}
}

func TestKindConstants(t *testing.T) {
	if KindCentral != "central" {
		t.Fatalf("expected KindCentral=central, got %s", KindCentral)
	}
	if KindCode != "code" {
		t.Fatalf("expected KindCode=code, got %s", KindCode)
	}
	if KindBrowser != "browser" {
		t.Fatalf("expected KindBrowser=browser, got %s", KindBrowser)
	}
	if KindVerifier != "verifier" {
		t.Fatalf("expected KindVerifier=verifier, got %s", KindVerifier)
	}
	if KindFault != "fault" {
		t.Fatalf("expected KindFault=fault, got %s", KindFault)
	}
	if KindData != "data" {
		t.Fatalf("expected KindData=data, got %s", KindData)
	}
	if KindQuant != "quant" {
		t.Fatalf("expected KindQuant=quant, got %s", KindQuant)
	}
	if KindDesktop != "desktop" {
		t.Fatalf("expected KindDesktop=desktop, got %s", KindDesktop)
	}
	if KindEasyMVP != "easymvp" {
		t.Fatalf("expected KindEasyMVP=easymvp, got %s", KindEasyMVP)
	}
}

func TestLLMAccessModes(t *testing.T) {
	if LLMAccessProxied != "proxied" {
		t.Fatalf("expected LLMAccessProxied=proxied, got %s", LLMAccessProxied)
	}
	if LLMAccessDirect != "direct" {
		t.Fatalf("expected LLMAccessDirect=direct, got %s", LLMAccessDirect)
	}
	if LLMAccessHybrid != "hybrid" {
		t.Fatalf("expected LLMAccessHybrid=hybrid, got %s", LLMAccessHybrid)
	}
}

func TestDescriptor(t *testing.T) {
	d := Descriptor{
		Kind:           KindCode,
		Version:        "1.0.0",
		LLMAccess:      LLMAccessProxied,
		SupportedTools: []string{"read_file", "write_file"},
		Capabilities:   map[string]bool{"streaming": true},
	}
	if d.Kind != KindCode {
		t.Fatalf("expected Kind=code, got %s", d.Kind)
	}
	if d.Version != "1.0.0" {
		t.Fatalf("expected Version=1.0.0, got %s", d.Version)
	}
	if d.LLMAccess != LLMAccessProxied {
		t.Fatalf("expected LLMAccess=proxied, got %s", d.LLMAccess)
	}
	if len(d.SupportedTools) != 2 {
		t.Fatalf("expected 2 supported tools, got %d", len(d.SupportedTools))
	}
	if !d.Capabilities["streaming"] {
		t.Fatal("expected streaming capability")
	}
}

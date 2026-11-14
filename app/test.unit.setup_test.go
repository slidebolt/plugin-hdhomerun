package app

import "testing"

func TestHelloUsesMediaDependencyWithoutOwningTools(t *testing.T) {
	hello := New().Hello()
	if err := hello.Validate(); err != nil {
		t.Fatal(err)
	}
	if hello.Setup == nil || len(hello.Setup.Tools) != 0 {
		t.Fatalf("setup tools = %#v", hello.Setup)
	}
	foundMedia := false
	for _, dependency := range hello.DependsOn {
		foundMedia = foundMedia || dependency == "media"
	}
	if !foundMedia {
		t.Fatalf("dependencies = %v", hello.DependsOn)
	}
}

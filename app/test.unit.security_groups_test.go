package app

import (
	"testing"

	domain "github.com/slidebolt/sb-domain"
)

func TestSecurityGroupContract(t *testing.T) {
	hello := New().Hello()
	if err := hello.Validate(); err != nil {
		t.Fatalf("plugin contract: %v", err)
	}
	if err := domain.ValidatePluginSecurityGroups(hello.Definitions); err != nil {
		t.Fatalf("plugin security groups: %v", err)
	}
}

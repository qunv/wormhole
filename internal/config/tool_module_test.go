package config

import (
	"strings"
	"testing"
)

func TestToolExposureAcceptsExternalModuleNames(t *testing.T) {
	cfg := Default()
	cfg.Tools.AllowedGroups = []string{"database", "kubernetes", "cloud_aws"}
	if err := cfg.Validate(false); err != nil {
		t.Fatalf("valid external module names rejected: %v", err)
	}
}

func TestToolExposureRejectsInvalidModuleNames(t *testing.T) {
	cfg := Default()
	cfg.Tools.AllowedGroups = []string{"custom/module"}
	if err := cfg.Validate(false); err == nil || !strings.Contains(err.Error(), "valid module name") {
		t.Fatalf("invalid module name error = %v", err)
	}
}

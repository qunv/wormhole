package factory

import "testing"

func TestRegistryContainsSupportedSQLDrivers(t *testing.T) {
	registered := map[string]bool{}
	for _, name := range Names() {
		registered[name] = true
	}
	for _, name := range []string{"mysql", "postgres", "sqlite"} {
		if !registered[name] {
			t.Fatalf("%s database constructor is not registered", name)
		}
	}
}

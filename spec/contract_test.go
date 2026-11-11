package spec

import "testing"

func TestSpecIsValid(t *testing.T) {
	if _, err := Load(); err != nil {
		t.Fatal(err)
	}
}

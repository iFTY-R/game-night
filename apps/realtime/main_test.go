package main

import "testing"

func TestRunRejectsNilLogger(t *testing.T) {
	if err := run(nil); err == nil {
		t.Fatal("run accepted a nil logger")
	}
}

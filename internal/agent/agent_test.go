package agent

import "testing"

func TestPriorityOf(t *testing.T) {
	if priorityOf("canton-participant") != 0 {
		t.Error("canton-participant should be 0")
	}
	if priorityOf("validator-app") != 1 {
		t.Error("validator-app should be 1")
	}
	if priorityOf("wallet-web-ui") != 2 {
		t.Error("unlisted image should be 2")
	}
	if !(priorityOf("canton-participant") < priorityOf("validator-app") &&
		priorityOf("validator-app") < priorityOf("frontend")) {
		t.Error("order participant < validator < rest is broken")
	}
}

package ctfplatform_test

import (
	"encoding/json"
	"testing"

	"hotmouse/backend/internal/ctfplatform"
	"hotmouse/backend/internal/ctfplatform/tsec"
)

func TestRegistrationAndTaskScope(t *testing.T) {
	registry := ctfplatform.NewRegistry()
	if err := tsec.Register(registry); err != nil {
		t.Fatal(err)
	}
	if err := tsec.Register(registry); err == nil {
		t.Fatal("duplicate registration accepted")
	}
	if _, err := registry.Connect("missing", ctfplatform.Config{}); err == nil {
		t.Fatal("unknown platform connected")
	}
	config := ctfplatform.Config{BaseURL: "https://platform.example", Token: "task-one"}
	client, err := registry.Connect("tsec", config)
	if err != nil || client.Descriptor().MaxActiveInstances != 3 {
		t.Fatalf("connect: %v %v", client, err)
	}
	first := config.ScopeID("tsec")
	config.Token = "task-two"
	if first == config.ScopeID("tsec") || first == config.ScopeID("another") {
		t.Fatal("bindings not isolated by task/platform")
	}
	config.Options = map[string]string{"task_id": "one", "username": "user"}
	optionsScope := config.ScopeID("tsec")
	config.Options = map[string]string{"username": "user", "task_id": "one"}
	if config.ScopeID("tsec") != optionsScope {
		t.Fatal("scope depends on option ordering")
	}
	config.Options["task_id"] = "two"
	if config.ScopeID("tsec") == optionsScope {
		t.Fatal("scope ignores platform authentication/task options")
	}
	data, _ := json.Marshal(config)
	if string(data) != "{}" {
		t.Fatalf("configuration serialized: %s", data)
	}
}

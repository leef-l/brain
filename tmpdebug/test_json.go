package main

import (
	"encoding/json"
	"fmt"
)

func main() {
	data := []byte(`{"draft_tasks_json":[{"task_key":"project_setup","name":"Project Setup","phase":"setup","task_kind":"implementation","summary":"Initialize project","brain_kind":"code","role_type":"developer"}]}`)
	var draft struct {
		DraftTasks []struct {
			TaskKey   string `json:"task_key"`
			Name      string `json:"name"`
			Phase     string `json:"phase"`
			TaskKind  string `json:"task_kind"`
			Summary   string `json:"summary"`
			BrainKind string `json:"brain_kind"`
			RoleType  string `json:"role_type"`
		} `json:"draft_tasks_json"`
	}
	err := json.Unmarshal(data, &draft)
	fmt.Printf("err=%v, len=%d\n", err, len(draft.DraftTasks))
	if len(draft.DraftTasks) > 0 {
		fmt.Printf("first=%+v\n", draft.DraftTasks[0])
	}
}

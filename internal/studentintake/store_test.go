package studentintake

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"standings-edu/internal/domain"
)

func TestStoreSubmitWithGroup(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "groups", "group_10a"), 0o755); err != nil {
		t.Fatalf("mkdir groups: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "students.json"), []byte("[]\n"), 0o644); err != nil {
		t.Fatalf("write students.json: %v", err)
	}
	groupJSON := `{
  "title": "10A",
  "update": true,
  "student_ids": []
}
`
	if err := os.WriteFile(filepath.Join(dataDir, "groups", "group_10a", "group.json"), []byte(groupJSON), 0o644); err != nil {
		t.Fatalf("write group.json: %v", err)
	}

	intakePath := filepath.Join(dataDir, "student_intake.json")
	store := NewStore(intakePath, dataDir)

	student, err := store.Submit(map[string]string{
		"full_name":  "Иванов Иван Иванович",
		"codeforces": "tourist",
		"group":      "group_10a",
	})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	if len(student.Groups) != 1 || student.Groups[0] != "group_10a" {
		t.Fatalf("student groups = %#v, want [group_10a]", student.Groups)
	}

	intakeStudents, err := LoadStudentsFile(intakePath)
	if err != nil {
		t.Fatalf("LoadStudentsFile(intake): %v", err)
	}
	if len(intakeStudents) != 1 {
		t.Fatalf("len(intakeStudents) = %d, want 1", len(intakeStudents))
	}
	if len(intakeStudents[0].Groups) != 1 || intakeStudents[0].Groups[0] != "group_10a" {
		t.Fatalf("intake student groups = %#v, want [group_10a]", intakeStudents[0].Groups)
	}

	sourceStudents, err := LoadStudentsFile(filepath.Join(dataDir, "students.json"))
	if err != nil {
		t.Fatalf("LoadStudentsFile(students): %v", err)
	}
	if len(sourceStudents) != 1 {
		t.Fatalf("len(sourceStudents) = %d, want 1", len(sourceStudents))
	}
	if sourceStudents[0].ID != student.ID {
		t.Fatalf("source student id = %q, want %q", sourceStudents[0].ID, student.ID)
	}
	if len(sourceStudents[0].Groups) != 1 || sourceStudents[0].Groups[0] != "group_10a" {
		t.Fatalf("source student groups = %#v, want [group_10a]", sourceStudents[0].Groups)
	}

	groupFilePath := filepath.Join(dataDir, "groups", "group_10a", "group.json")
	groupBytes, err := os.ReadFile(groupFilePath)
	if err != nil {
		t.Fatalf("read group file: %v", err)
	}
	var groupFile domain.GroupFile
	if err := json.Unmarshal(groupBytes, &groupFile); err != nil {
		t.Fatalf("decode group file: %v", err)
	}
	if len(groupFile.StudentIDs) != 1 || groupFile.StudentIDs[0] != student.ID {
		t.Fatalf("group student_ids = %#v, want [%s]", groupFile.StudentIDs, student.ID)
	}
}

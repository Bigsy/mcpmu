package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectSkillTargets(t *testing.T) {
	tests := []struct {
		name      string
		dirs      []string // dirs to create under temp home
		wantLen   int
		wantNames []string
	}{
		{
			name:      "claude only",
			dirs:      []string{".claude"},
			wantLen:   2,
			wantNames: []string{"Claude Code", "Cross-agent"},
		},
		{
			name:      "codex only",
			dirs:      []string{".codex"},
			wantLen:   2,
			wantNames: []string{"Codex CLI", "Cross-agent"},
		},
		{
			name:      "both agents",
			dirs:      []string{".claude", ".codex"},
			wantLen:   3,
			wantNames: []string{"Claude Code", "Codex CLI", "Cross-agent"},
		},
		{
			name:      "no agents",
			dirs:      nil,
			wantLen:   1,
			wantNames: []string{"Cross-agent"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			for _, d := range tt.dirs {
				if err := os.MkdirAll(filepath.Join(home, d), 0755); err != nil {
					t.Fatal(err)
				}
			}

			targets := detectSkillTargets(home)
			if len(targets) != tt.wantLen {
				t.Fatalf("got %d targets, want %d", len(targets), tt.wantLen)
			}

			for i, name := range tt.wantNames {
				if targets[i].Name != name {
					t.Errorf("target[%d].Name = %q, want %q", i, targets[i].Name, name)
				}
			}
		})
	}
}

func TestInstallSkill(t *testing.T) {
	home := t.TempDir()
	content := []byte("# test skill content")

	targets := []skillTarget{
		{Name: "Claude Code", Dir: filepath.Join(home, ".claude", "skills", "mcpmu")},
		{Name: "Cross-agent", Dir: filepath.Join(home, ".agents", "skills", "mcpmu")},
	}

	installed, errs := installSkill(targets, content)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(installed) != 2 {
		t.Fatalf("got %d installed, want 2", len(installed))
	}

	for _, p := range installed {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("cannot read %s: %v", p, err)
		}
		if string(data) != string(content) {
			t.Errorf("content mismatch at %s", p)
		}
	}
}

func TestInstallSkillIdempotent(t *testing.T) {
	home := t.TempDir()
	content := []byte("# test skill content")

	targets := []skillTarget{
		{Name: "Cross-agent", Dir: filepath.Join(home, ".agents", "skills", "mcpmu")},
	}

	// Install twice
	_, errs := installSkill(targets, content)
	if len(errs) != 0 {
		t.Fatalf("first install errors: %v", errs)
	}

	installed, errs := installSkill(targets, content)
	if len(errs) != 0 {
		t.Fatalf("second install errors: %v", errs)
	}
	if len(installed) != 1 {
		t.Fatalf("got %d installed, want 1", len(installed))
	}

	data, err := os.ReadFile(installed[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(content) {
		t.Error("content mismatch after second install")
	}
}

func TestUninstallSkill(t *testing.T) {
	home := t.TempDir()
	content := []byte("# test skill content")

	targets := []skillTarget{
		{Name: "Claude Code", Dir: filepath.Join(home, ".claude", "skills", "mcpmu")},
		{Name: "Cross-agent", Dir: filepath.Join(home, ".agents", "skills", "mcpmu")},
	}

	// Install first
	_, errs := installSkill(targets, content)
	if len(errs) != 0 {
		t.Fatalf("install errors: %v", errs)
	}

	// Uninstall
	removed, errs := uninstallSkill(targets)
	if len(errs) != 0 {
		t.Fatalf("uninstall errors: %v", errs)
	}
	if len(removed) != 2 {
		t.Fatalf("got %d removed, want 2", len(removed))
	}

	// Verify files and dirs are gone
	for _, target := range targets {
		skillPath := filepath.Join(target.Dir, "SKILL.md")
		if _, err := os.Stat(skillPath); !os.IsNotExist(err) {
			t.Errorf("SKILL.md still exists at %s", skillPath)
		}
		if _, err := os.Stat(target.Dir); !os.IsNotExist(err) {
			t.Errorf("directory still exists at %s", target.Dir)
		}
	}
}

func TestUninstallSkillNoOp(t *testing.T) {
	home := t.TempDir()

	targets := []skillTarget{
		{Name: "Cross-agent", Dir: filepath.Join(home, ".agents", "skills", "mcpmu")},
	}

	removed, errs := uninstallSkill(targets)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(removed) != 0 {
		t.Fatalf("got %d removed, want 0", len(removed))
	}
}

func TestUninstallSkillPreservesUserFiles(t *testing.T) {
	home := t.TempDir()
	content := []byte("# test skill content")

	target := skillTarget{
		Name: "Cross-agent",
		Dir:  filepath.Join(home, ".agents", "skills", "mcpmu"),
	}

	// Install
	_, errs := installSkill([]skillTarget{target}, content)
	if len(errs) != 0 {
		t.Fatalf("install errors: %v", errs)
	}

	// Add a user file to the directory
	userFile := filepath.Join(target.Dir, "custom.md")
	if err := os.WriteFile(userFile, []byte("user content"), 0644); err != nil {
		t.Fatal(err)
	}

	// Uninstall
	removed, errs := uninstallSkill([]skillTarget{target})
	if len(errs) != 0 {
		t.Fatalf("uninstall errors: %v", errs)
	}
	if len(removed) != 1 {
		t.Fatalf("got %d removed, want 1", len(removed))
	}

	// SKILL.md should be gone
	skillPath := filepath.Join(target.Dir, "SKILL.md")
	if _, err := os.Stat(skillPath); !os.IsNotExist(err) {
		t.Error("SKILL.md still exists")
	}

	// Directory should still exist (non-empty)
	if _, err := os.Stat(target.Dir); os.IsNotExist(err) {
		t.Error("directory was removed despite containing user files")
	}

	// User file should still exist
	if _, err := os.Stat(userFile); os.IsNotExist(err) {
		t.Error("user file was removed")
	}
}

func TestSkillDataInSync(t *testing.T) {
	canonical, err := os.ReadFile("skill_data/SKILL.md")
	if err != nil {
		t.Fatalf("cannot read canonical skill_data/SKILL.md: %v", err)
	}

	mirror, err := os.ReadFile("../../.claude/skills/mcpmu/SKILL.md")
	if err != nil {
		t.Fatalf("cannot read mirror .claude/skills/mcpmu/SKILL.md: %v", err)
	}

	if string(canonical) != string(mirror) {
		t.Fatal("skill_data/SKILL.md and .claude/skills/mcpmu/SKILL.md are out of sync — update one to match the other")
	}
}
